// webserver.go — the M10 unified web portal (ADR 2026-08-20-m10-web-portal.md
// D-WEB-1~7): a single net/http server carrying the dsh-style session/event
// browsing entry (M10a), the dashboard stats API (M10c) and later KB admin
// (M10b). The API is read-only (D-WEB-4): it never writes the session log.
// Every route sits behind the bearer-token middleware; the frontend is vanilla
// JS embedded into the binary (go:embed) — zero new dependencies.
package webserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"personal-agent/internal/session"
	"personal-agent/internal/store"
)

//go:embed static
var staticFS embed.FS

// maxSummary is the rune cap on the bounded per-event summary the events API
// exposes (防超大载荷 / 防泄露完整日志正文, D-WEB-4).
const maxSummary = 200

// Server is the M10 web portal: a bearer-authenticated net/http server over
// the read-only session store.
type Server struct {
	store     store.Store
	tokenHash [32]byte // sha256 of the configured token; the plaintext never survives New
	addr      string
	srv       *http.Server

	// M10 W1 interactive wiring (ADR D-WEB2-A/B/C): the optional handlers the
	// composition root injects after New. All three are nil until a Setter is
	// called; a nil handler makes its API answer 501.
	msgFn  func(ctx context.Context, sessionID, text string) error
	sessFn func(ctx context.Context, action, id string) (string, error)
	evSrc  func(sessionID string, sink func(session.Event)) func()
}

// New validates the wiring and builds the portal handler. token is required
// (fail-closed: an empty token refuses to start rather than serving bare) and
// only its SHA-256 digest is retained. addr defaults to "127.0.0.1:8080".
func New(st store.Store, token, addr string) (*Server, error) {
	if st == nil {
		return nil, errors.New("webserver: store is required")
	}
	if token == "" {
		return nil, errors.New("webserver: token required (set web_server.token)")
	}
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	s := &Server{
		store:     st,
		tokenHash: sha256.Sum256([]byte(token)),
		addr:      addr,
	}
	// The static shell (login view + frontend assets) is public so a fresh
	// browser can load the page and present the token form (D-WEB-2): it holds
	// no data. Every /api route sits behind the bearer middleware.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /static/{file...}", s.handleStatic)
	mux.Handle("GET /api/health", s.requireAuth(http.HandlerFunc(s.handleHealth)))
	mux.Handle("GET /api/stats", s.requireAuth(http.HandlerFunc(s.handleStats)))
	mux.Handle("GET /api/kb", s.requireAuth(http.HandlerFunc(s.handleKBStub)))
	mux.Handle("GET /api/kb/{rest...}", s.requireAuth(http.HandlerFunc(s.handleKBStub)))
	mux.Handle("GET /api/sessions", s.requireAuth(http.HandlerFunc(s.handleSessions)))
	mux.Handle("GET /api/sessions/{id}/events", s.requireAuth(http.HandlerFunc(s.handleEvents)))
	// M10 W1 interactive API (ADR D-WEB2): session new/resume, message dispatch
	// and the SSE event stream all sit behind the same bearer middleware.
	mux.Handle("POST /api/sessions", s.requireAuth(http.HandlerFunc(s.handleSessionCreate)))
	mux.Handle("POST /api/sessions/{id}/resume", s.requireAuth(http.HandlerFunc(s.handleSessionResume)))
	mux.Handle("POST /api/sessions/{id}/message", s.requireAuth(http.HandlerFunc(s.handleMessage)))
	mux.Handle("GET /api/sessions/{id}/events/stream", s.requireAuth(http.HandlerFunc(s.handleEventStream)))
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s, nil
}

// Handler returns the authenticated HTTP handler (for httptest).
func (s *Server) Handler() http.Handler { return s.srv.Handler }

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.addr }

// Serve blocks serving the portal until Close.
func (s *Server) Serve() error {
	err := s.srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Close shuts the server down (idempotent).
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// SetMessageHandler wires the message dispatch API (POST
// /api/sessions/{id}/message). Called by the composition root (cmd/pa) at
// registration time; nil (the default) makes the API answer 501.
func (s *Server) SetMessageHandler(fn func(ctx context.Context, sessionID, text string) error) {
	s.msgFn = fn
}

// SetSessionManager wires the session new/resume API (POST /api/sessions and
// POST /api/sessions/{id}/resume). Called by the composition root; nil makes
// those APIs answer 501.
func (s *Server) SetSessionManager(fn func(ctx context.Context, action, id string) (string, error)) {
	s.sessFn = fn
}

// SetEventSource wires the real-time event stream (GET
// /api/sessions/{id}/events/stream): the source subscribes a session and calls
// sink for each new event; the returned func unsubscribes. Called by the
// composition root; nil makes the stream answer 501.
func (s *Server) SetEventSource(fn func(sessionID string, sink func(session.Event)) func()) {
	s.evSrc = fn
}

// InteractiveHandlers is a snapshot of the currently injected interactive
// wiring (M10 W1, ADR D-WEB2). The composition root reads it in its wiring
// tests; nil fields mean the corresponding API answers 501.
type InteractiveHandlers struct {
	Message func(ctx context.Context, sessionID, text string) error
	Session func(ctx context.Context, action, id string) (string, error)
	Event   func(sessionID string, sink func(session.Event)) func()
}

// Handlers returns the current interactive wiring.
func (s *Server) Handlers() InteractiveHandlers {
	return InteractiveHandlers{Message: s.msgFn, Session: s.sessFn, Event: s.evSrc}
}

// requireAuth wraps an /api handler with the bearer-token check (D-WEB-2): the
// presented token's SHA-256 must match the stored digest under a constant-time
// compare. Only the API routes are gated; the static shell stays public so the
// login view can load (data never leaves the API).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		sum := sha256.Sum256([]byte(strings.TrimPrefix(auth, prefix)))
		if subtle.ConstantTimeCompare(sum[:], s.tokenHash[:]) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON encodes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleIndex serves the embedded single-page shell. In the ServeMux the
// pattern "GET /" matches every unmatched path, so a strict path check keeps
// unknown routes a 404 rather than serving the shell.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// handleStatic serves embedded static assets under /static/ (StripPrefix
// removes the route prefix so the FileServer resolves inside the static dir).
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "static missing", http.StatusInternalServerError)
		return
	}
	http.StripPrefix("/static/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sessionView is the API's minimal owned session metadata (no store refs).
type sessionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	EventCount int       `json:"event_count"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	metas, err := s.store.ListSessions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]sessionView, 0, len(metas))
	for _, m := range metas {
		out = append(out, sessionView{ID: m.ID, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, EventCount: m.EventCount})
	}
	writeJSON(w, http.StatusOK, out)
}

// statsView is the /api/stats aggregate (D-WEB-5: a read-only in-memory rollup
// of the session log, never persisted). last_active is the newest event time,
// zero when the store holds no events.
type statsView struct {
	SessionsTotal   int            `json:"sessions_total"`
	EventsTotal     int            `json:"events_total"`
	LastActive      time.Time      `json:"last_active"`
	EventTypeCounts map[string]int `json:"event_type_counts"`
	ToolCalls       int            `json:"tool_calls"`
}

// handleStats aggregates every session's events into the dashboard view. It is
// deliberately O(all events): ListSessions then one LoadSession per session,
// summing the type counts, tool/result calls and the newest event time. Fine
// for a personal portal; a huge log would want paging/denormalization, which
// M10 accepts not adding (dispatch-m10 §M10c, 诚实记录限制).
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	metas, err := s.store.ListSessions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	st := statsView{SessionsTotal: len(metas), EventTypeCounts: map[string]int{}}
	for _, m := range metas {
		events, err := s.store.LoadSession(r.Context(), m.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		for _, ev := range events {
			st.EventsTotal++
			st.EventTypeCounts[ev.Type]++
			if ev.Type == "tool/result" {
				st.ToolCalls++
			}
			if ev.At.After(st.LastActive) {
				st.LastActive = ev.At
			}
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// handleKBStub is the M10b KB-admin placeholder (ADR D-WEB-6): the /api/kb/*
// routes return 501 until the KB 全量 (content layer) lands and the real admin
// data/API is mounted — the shell exists so the frontend can navigate, the
// backend is honestly "not implemented".
func (s *Server) handleKBStub(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "KB admin not implemented (KB 全量后挂)"})
}

// eventView is one event's bounded public summary (D-WEB-4: data is never
// exposed wholesale).
type eventView struct {
	Seq     uint64    `json:"seq"`
	Type    string    `json:"type"`
	Time    time.Time `json:"time"`
	Summary string    `json:"summary"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := s.store.LoadSession(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]eventView, 0, len(events))
	for _, ev := range events {
		out = append(out, eventView{Seq: ev.Seq, Type: ev.Type, Time: ev.At, Summary: summarize(ev)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSessionCreate implements POST /api/sessions (M10 W1, ADR D-WEB2-C):
// it asks the injected session manager to start a fresh session and returns
// its id. An unwired manager answers 501.
func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if s.sessFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "session manager not wired"})
		return
	}
	id, err := s.sessFn(r.Context(), "new", "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleSessionResume implements POST /api/sessions/{id}/resume: it asks the
// injected session manager to resume the session and returns its id.
func (s *Server) handleSessionResume(w http.ResponseWriter, r *http.Request) {
	if s.sessFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "session manager not wired"})
		return
	}
	id := r.PathValue("id")
	newID, err := s.sessFn(r.Context(), "resume", id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": newID})
}

// handleMessage implements POST /api/sessions/{id}/message (M10 W1, ADR
// D-WEB2-A): it dispatches one user message to the injected handler, which runs
// the turn (the streaming process arrives on the SSE stream). The response 200
// {"ok":true} means the Run has completed. An empty text answers 400; an
// unwired handler answers 501.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if s.msgFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "message handler not wired"})
		return
	}
	id := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
		return
	}
	if err := s.msgFn(r.Context(), id, body.Text); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleEventStream implements GET /api/sessions/{id}/events/stream — the SSE
// real-time event flow (M10 W1, ADR D-WEB2-B): it first replays the session's
// stored events as frames (snapshot), then subscribes the injected event source
// and forwards every new event as a frame. Each frame is
// `id: <seq>\ndata: {seq,type,time,summary}\n\n` and is flushed immediately
// (http.Flusher). The handler returns when the request context is cancelled
// (client disconnect), unsubscribing the event source. It does not use
// writeJSON once the stream has started.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.evSrc == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "event source not wired"})
		return
	}
	events, err := s.store.LoadSession(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n")
	for _, ev := range events {
		writeSSEEvent(w, ev)
	}
	fl.Flush()
	unsub := s.evSrc(id, func(ev session.Event) {
		writeSSEEvent(w, ev)
		fl.Flush()
	})
	defer unsub()
	<-r.Context().Done()
}

// writeSSEEvent writes one SSE frame for an event and returns. Writes to a
// disconnected client fail silently (the handler exits on context cancellation).
func writeSSEEvent(w http.ResponseWriter, ev session.Event) {
	b, err := json.Marshal(eventView{Seq: ev.Seq, Type: ev.Type, Time: ev.At, Summary: summarize(ev)})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, b)
}

// summarize extracts a bounded, safe one-line summary for an event by
// unmarshalling only the leaf fields the known types carry (未知类型 → ""; 前端
// 忽略空 summary). The raw Data blob is never exposed.
func summarize(ev session.Event) string {
	switch ev.Type {
	case "user/message":
		var d struct{ Text string }
		if json.Unmarshal(ev.Data, &d) == nil {
			return boundRunes(d.Text, maxSummary)
		}
	case "assistant/message":
		var d struct{ Text string }
		if json.Unmarshal(ev.Data, &d) == nil {
			return boundRunes(d.Text, maxSummary)
		}
	case "tool/result":
		var d struct {
			CallID string
			Name   string
			Output string
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			return "tool " + d.Name + " → " + boundRunes(d.Output, maxSummary)
		}
	case "tool/error":
		var d struct {
			CallID string
			Name   string
			Err    string
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			return "tool " + d.Name + " error → " + boundRunes(d.Err, maxSummary)
		}
	}
	return ""
}

// boundRunes truncates s to at most max runes, appending "…" when cut.
func boundRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
