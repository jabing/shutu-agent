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
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"personal-agent/internal/attachment"
	"personal-agent/internal/llm"
	"personal-agent/internal/session"
	"personal-agent/internal/store"
)

//go:embed static
var staticFS embed.FS

// maxSummary is the rune cap on the bounded per-event summary the events API
// exposes (防超大载荷 / 防泄露完整日志正文, D-WEB-4).
const maxSummary = 200

// maxTitle is the rune cap on the session-list title (first user message, M10
// W4 D-WEB2-H).
const maxTitle = 80

// Server is the M10 web portal: a net/http server over the read-only session
// store. Authentication is optional (D-WEB-2 change, user decision 2026-08-20):
// when token == "" every API route is open to the local machine (the
// 127.0.0.1 bind is the trust boundary, like dsh web); when a token is set the
// bearer middleware guards every /api route.
type Server struct {
	store     store.Store
	tokenHash [32]byte // sha256 of the configured token; the plaintext never survives New
	authOn    bool     // token != "" → bearer check enforced
	addr      string
	srv       *http.Server

	// M10 W1 interactive wiring (ADR D-WEB2-A/B/C): the optional handlers the
	// composition root injects after New. All three are nil until a Setter is
	// called; a nil handler makes its API answer 501.
	msgFn  func(ctx context.Context, sessionID, text string, images []llm.ImageRef) error
	sessFn func(ctx context.Context, action, id string) (string, error)
	evSrc  func(sessionID string, sink func(session.Event)) func()

	// cfgFn is the M10 W2 config provider (ADR D-WEB2-D): it returns the
	// sanitized configuration view for GET /api/config. The redaction itself is
	// the composition root's job (cmd/pa's webConfig never exposes web_server.
	// token or any key); the webserver only forwards the provider's map. nil
	// (the default) makes the API answer 501.
	cfgFn func() map[string]any

	// M10 W4 (ADR D-WEB2-H): optional read-only providers for the subagent and
	// background-job panels (GET /api/subagents, GET /api/jobs). Both are nil
	// until a Setter is called; a nil provider makes its API answer 501. Each
	// returns sanitized view maps (id/status/timestamps only — no prompts,
	// outputs or session content).
	subFn  func(ctx context.Context) ([]map[string]any, error)
	jobsFn func(ctx context.Context) ([]map[string]any, error)

	// P5 (ADR D-WEB2-I): the image-attachment store wired by the composition
	// root when multimodal is enabled. nil (the default) makes the attachment
	// APIs answer 501 and message bodies with images answer 400.
	att *attachment.Store
}

// SetAttachmentStore wires the image-attachment store (P5): POST/GET
// /api/sessions/{id}/attachments and the images field of POST /api/sessions/
// {id}/message. Called by the composition root; nil (default) keeps the
// attachment APIs at 501.
func (s *Server) SetAttachmentStore(st *attachment.Store) { s.att = st }

// New validates the wiring and builds the portal handler. token is optional:
// empty opens the portal to the local machine (dsh-style, no login); a token
// turns on bearer auth and only its SHA-256 digest is retained. addr defaults
// to "127.0.0.1:8080".
func New(st store.Store, token, addr string) (*Server, error) {
	if st == nil {
		return nil, errors.New("webserver: store is required")
	}
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	s := &Server{
		store:     st,
		tokenHash: sha256.Sum256([]byte(token)),
		authOn:    token != "",
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
	// M10 P2 (ADR D-WEB2-I): sidebar session management — rename (PATCH) and
	// delete (DELETE). PATCH body is {"title": "..."}; an empty title clears the
	// override back to first-user-message inference.
	mux.Handle("PATCH /api/sessions/{id}/title", s.requireAuth(http.HandlerFunc(s.handleSessionTitle)))
	mux.Handle("DELETE /api/sessions/{id}", s.requireAuth(http.HandlerFunc(s.handleSessionDelete)))
	// M10 P5 (ADR D-WEB2-I): image attachments — multipart upload (POST) and
	// byte echo (GET). Both stay behind the same bearer middleware.
	mux.Handle("POST /api/sessions/{id}/attachments", s.requireAuth(http.HandlerFunc(s.handleAttachmentUpload)))
	mux.Handle("GET /api/sessions/{id}/attachments/{attID}", s.requireAuth(http.HandlerFunc(s.handleAttachmentGet)))
	mux.Handle("GET /api/sessions/{id}/events/stream", s.requireAuth(http.HandlerFunc(s.handleEventStream)))
	// M10 W2 (ADR D-WEB2-D): the read-only sanitized config view.
	mux.Handle("GET /api/config", s.requireAuth(http.HandlerFunc(s.handleConfig)))
	// M10 W4 (ADR D-WEB2-H): the read-only subagent and background-job panels.
	mux.Handle("GET /api/subagents", s.requireAuth(http.HandlerFunc(s.handleSubagents)))
	mux.Handle("GET /api/jobs", s.requireAuth(http.HandlerFunc(s.handleJobs)))
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
// /api/sessions/{id}/message). images carries the parsed image refs of the
// message (P5), nil/empty for text-only turns. Called by the composition root
// (cmd/pa) at registration time; nil (the default) makes the API answer 501.
func (s *Server) SetMessageHandler(fn func(ctx context.Context, sessionID, text string, images []llm.ImageRef) error) {
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

// SetConfigProvider wires the read-only config view (GET /api/config, M10 W2,
// ADR D-WEB2-D). The provider returns a sanitized map — cmd/pa's webConfig
// never includes web_server.token or any key — and the webserver forwards it
// verbatim. Called by the composition root; nil makes the API answer 501.
func (s *Server) SetConfigProvider(fn func() map[string]any) {
	s.cfgFn = fn
}

// SetSubagentProvider wires the read-only subagent panel (GET /api/subagents,
// M10 W4, ADR D-WEB2-H). The provider returns sanitized child-agent views
// (id/status/timestamps only). Called by the composition root; nil makes the
// API answer 501.
func (s *Server) SetSubagentProvider(fn func(ctx context.Context) ([]map[string]any, error)) {
	s.subFn = fn
}

// SetJobsProvider wires the read-only background-job panel (GET /api/jobs, M10
// W4, ADR D-WEB2-H). The provider returns sanitized job views (id/kind/status/
// timestamps only — no outputs). Called by the composition root; nil makes the
// API answer 501.
func (s *Server) SetJobsProvider(fn func(ctx context.Context) ([]map[string]any, error)) {
	s.jobsFn = fn
}

// InteractiveHandlers is a snapshot of the currently injected interactive
// wiring (M10 W1, ADR D-WEB2). The composition root reads it in its wiring
// tests; nil fields mean the corresponding API answers 501.
type InteractiveHandlers struct {
	Message   func(ctx context.Context, sessionID, text string, images []llm.ImageRef) error
	Session   func(ctx context.Context, action, id string) (string, error)
	Event     func(sessionID string, sink func(session.Event)) func()
	Config    func() map[string]any
	Subagents func(ctx context.Context) ([]map[string]any, error)
	Jobs      func(ctx context.Context) ([]map[string]any, error)
}

// Handlers returns the current interactive wiring.
func (s *Server) Handlers() InteractiveHandlers {
	return InteractiveHandlers{
		Message: s.msgFn, Session: s.sessFn, Event: s.evSrc, Config: s.cfgFn,
		Subagents: s.subFn, Jobs: s.jobsFn,
	}
}

// panicSafeWriter tracks whether a response has started so a deferred recover
// can decide if it may still write a 500 body (writing after the header was
// sent panics again). It also forwards Flush for the SSE stream.
type panicSafeWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *panicSafeWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *panicSafeWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *panicSafeWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// requireAuth wraps an /api handler with the bearer-token check (D-WEB-2): the
// presented token's SHA-256 must match the stored digest under a constant-time
// compare. Only the API routes are gated; the static shell stays public so the
// login view can load (data never leaves the API). It also recovers a panicking
// handler into a JSON 500 (M10 W3 robustness): a crashed route must never
// answer a bare connection reset, and the panic + stack is logged for repair.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &panicSafeWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("pa: web handler panic: %v\n%s", rec, debug.Stack())
				if !sw.wrote {
					writeJSON(sw, http.StatusInternalServerError, map[string]any{"error": fmt.Sprintf("internal error: %v", rec)})
				}
			}
		}()
		if !s.authOn {
			next.ServeHTTP(sw, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeJSON(sw, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		sum := sha256.Sum256([]byte(strings.TrimPrefix(auth, prefix)))
		if subtle.ConstantTimeCompare(sum[:], s.tokenHash[:]) != 1 {
			writeJSON(sw, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(sw, r)
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

// handleConfig implements GET /api/config (M10 W2, ADR D-WEB2-D): it serves
// the injected config provider's sanitized map verbatim. The provider (cmd/pa's
// webConfig) is responsible for redaction — web_server.token and any keys are
// never included — so the API boundary never carries a plaintext secret. An
// unwired provider answers 501.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if s.cfgFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "config provider not wired"})
		return
	}
	writeJSON(w, http.StatusOK, s.cfgFn())
}

// sessionView is the API's minimal owned session metadata (no store refs).
// M10 W4 (D-WEB2-H) adds the session-list fields the dsh-style sidebar needs:
// title (first user message, bounded) and blank (no events yet).
type sessionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	EventCount int       `json:"event_count"`
	Title      string    `json:"title,omitempty"`
	Blank      bool      `json:"blank"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	metas, err := s.store.ListSessions(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]sessionView, 0, len(metas))
	for _, m := range metas {
		v := sessionView{ID: m.ID, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, EventCount: m.EventCount, Blank: m.EventCount == 0}
		if m.Title != "" {
			// User-set title (P2 rename) wins over inference.
			v.Title = boundRunes(m.Title, maxTitle)
		} else if m.EventCount > 0 {
			// The sidebar title is the first user message, bounded (a personal
			// portal list is small; this is O(events of each session), same
			// order as the existing stats rollup).
			if evs, err := s.store.LoadSession(r.Context(), m.ID); err == nil {
				for _, ev := range evs {
					if ev.Type == "user/message" {
						var d struct{ Text string }
						if json.Unmarshal(ev.Data, &d) == nil {
							v.Title = boundRunes(d.Text, maxTitle)
							break
						}
					}
				}
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSessionTitle implements PATCH /api/sessions/{id}/title (P2 sidebar
// rename). The request body is {"title":"..."} (UTF-8, bounded); an empty title
// clears the override back to inference.
func (s *Server) handleSessionTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct{ Title string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request: " + err.Error()})
		return
	}
	title := boundRunes(body.Title, maxTitle)
	if err := s.store.SetSessionTitle(r.Context(), id, title); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "title": title})
}

// handleSessionDelete implements DELETE /api/sessions/{id} (P2 sidebar delete).
// It removes the session and all of its events from the durable store.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteSession(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
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

// maxEventImages bounds how many image refs the events API exposes per message
// (P5, aligned with the frontend default 10).
const maxEventImages = 10

// imageView is one image reference in an event's images list (P5).
type imageView struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

// eventView is one event's bounded public summary (D-WEB-4: data is never
// exposed wholesale). M10 W4 (D-WEB2-H) adds the fields the dsh-style message
// stream needs: the assistant's reasoning chain (思维链), the tool-card title
// and its bounded output. P5 adds the image refs carried by user/assistant
// messages (bytes never leave the attachment store; the browser fetches them
// through the authorized echo endpoint).
type eventView struct {
	Seq        uint64      `json:"seq"`
	Type       string      `json:"type"`
	Time       time.Time   `json:"time"`
	Summary    string      `json:"summary"`
	Reasoning  string      `json:"reasoning,omitempty"`   // assistant/message 的思维链（有界）
	ToolName   string      `json:"tool_name,omitempty"`   // tool/result、tool/error 的工具名
	ToolOutput string      `json:"tool_output,omitempty"` // tool/result 的有界输出
	Images     []imageView `json:"images,omitempty"`      // P5: 该消息携带的图片引用
}

// toEventView builds the public view for one event (bounded summary + the W4
// extra fields + the P5 image refs).
func toEventView(ev session.Event) eventView {
	v := eventView{Seq: ev.Seq, Type: ev.Type, Time: ev.At, Summary: summarize(ev)}
	v.Reasoning, v.ToolName, v.ToolOutput = extraFields(ev)
	v.Images = extractImages(ev)
	return v
}

// extractImages pulls the image refs out of a user/assistant message's content
// blocks (only ref metadata — the bytes live in the attachment store). Unknown
// payloads yield nil; the frontend hides history images when absent.
func extractImages(ev session.Event) []imageView {
	if ev.Type != "user/message" && ev.Type != "assistant/message" {
		return nil
	}
	var d struct {
		Content []llm.ContentBlock `json:"content"`
	}
	if json.Unmarshal(ev.Data, &d) != nil {
		return nil
	}
	var out []imageView
	for _, b := range d.Content {
		if b.Kind != llm.BlockImage {
			continue
		}
		out = append(out, imageView{
			ID: b.Image.ID, MediaType: b.Image.MediaType,
			Width: b.Image.Width, Height: b.Image.Height,
		})
		if len(out) >= maxEventImages {
			break
		}
	}
	return out
}

// extraFields extracts the W4 per-type fields from an event's Data blob by
// unmarshalling only the leaf JSON keys the known types carry. Unknown types
// yield empty strings (前端忽略)。
func extraFields(ev session.Event) (reasoning, toolName, toolOutput string) {
	switch ev.Type {
	case "assistant/message":
		var d struct {
			Reasoning string `json:"reasoning"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			reasoning = boundRunes(d.Reasoning, maxSummary)
		}
	case "tool/result":
		var d struct {
			Name   string `json:"name"`
			Output string `json:"output"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			toolName = d.Name
			toolOutput = boundRunes(d.Output, maxSummary)
		}
	case "tool/error":
		var d struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(ev.Data, &d) == nil {
			toolName = d.Name
		}
	}
	return reasoning, toolName, toolOutput
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
		out = append(out, toEventView(ev))
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
// {"ok":true} means the Run has completed. P5 extends the body with an optional
// images list (attachment ids → ImageRef, resolved through the attachment
// store). An empty text without images answers 400; an unwired handler answers
// 501.
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if s.msgFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "message handler not wired"})
		return
	}
	id := r.PathValue("id")
	var body struct {
		Text   string   `json:"text"`
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.Text) == "" && len(body.Images) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
		return
	}
	var images []llm.ImageRef
	if len(body.Images) > 0 {
		if s.att == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "images not supported"})
			return
		}
		for _, imgID := range body.Images {
			ref, err := s.att.GetByID(imgID)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "image " + imgID + " not found"})
				return
			}
			images = append(images, ref)
		}
	}
	if err := s.msgFn(r.Context(), id, body.Text, images); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// maxWebImageBytes caps a single uploaded image via the web portal (P5). The
// frontend enforces the same default (10MB); the backend fails closed so the
// portal never writes a giant file even if the client lies.
const maxWebImageBytes = 10 << 20

// attachmentView is the POST /api/sessions/{id}/attachments response.
type attachmentView struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// handleAttachmentUpload implements POST /api/sessions/{id}/attachments (P5):
// a multipart form with a "file" field. The bytes are validated and stored by
// the attachment store; the session must exist. An unwired store answers 501.
func (s *Server) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if s.att == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "attachment store not wired"})
		return
	}
	id := r.PathValue("id")
	if _, err := s.store.LoadSession(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad multipart form"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "file field required"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxWebImageBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read file failed"})
		return
	}
	// Media type: prefer the filename extension, fall back to content sniffing.
	mediaType := attachment.MediaTypeForExtension(strings.ToLower(filepath.Ext(header.Filename)))
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	ref, err := s.att.SaveImage(mediaType, data, maxWebImageBytes)
	if err != nil {
		switch {
		case errors.Is(err, attachment.ErrUnsupportedType):
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported type"})
		case errors.Is(err, attachment.ErrEmptyData):
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty"})
		case errors.Is(err, attachment.ErrTooLarge):
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "too large"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusCreated, attachmentView{
		ID: ref.ID, MediaType: ref.MediaType, Bytes: ref.Bytes,
		Width: ref.Width, Height: ref.Height,
	})
}

// handleAttachmentGet implements GET /api/sessions/{id}/attachments/{attID}
// (P5): it echoes the stored image bytes with their Content-Type for the
// browser <img> / lightbox. 404 when the session or attachment is unknown.
func (s *Server) handleAttachmentGet(w http.ResponseWriter, r *http.Request) {
	if s.att == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "attachment store not wired"})
		return
	}
	if _, err := s.store.LoadSession(r.Context(), r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	ref, err := s.att.GetByID(r.PathValue("attID"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "attachment not found"})
		return
	}
	data, err := s.att.Read(ref)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "attachment not readable"})
		return
	}
	w.Header().Set("Content-Type", ref.MediaType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
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
	b, err := json.Marshal(toEventView(ev))
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, b)
}

// handleSubagents implements GET /api/subagents (M10 W4, ADR D-WEB2-H): the
// read-only panel for active sub-agents. An unwired provider answers 501; the
// provider's sanitized views are forwarded verbatim.
func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request) {
	if s.subFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "subagent provider not wired"})
		return
	}
	items, err := s.subFn(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subagents": items})
}

// handleJobs implements GET /api/jobs (M10 W4, ADR D-WEB2-H): the read-only
// panel for background jobs. An unwired provider answers 501; the provider's
// sanitized views are forwarded verbatim.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobsFn == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "jobs provider not wired"})
		return
	}
	items, err := s.jobsFn(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": items})
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
