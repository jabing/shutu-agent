// webserver_test.go — the M10a portal tests (docs/dispatch-m10.md §3): New
// validation, bearer auth, sessions/events JSON API, static hosting, and the
// bounded event summary. The store is a real SQLite backend on a temp dir (the
// same backend the REPL uses), seeded through CreateSession + AppendEvents.
package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"personal-agent/internal/session"
	"personal-agent/internal/store"
)

// newTestServer builds a portal over a fresh temp SQLite store.
func newTestServer(t *testing.T, token string) (*Server, *store.SQLiteStore) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(st, token, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, st
}

func doReq(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func seedSession(t *testing.T, st *store.SQLiteStore, id string, events []session.Event) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateSession(ctx, id, now); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := st.AppendEvents(ctx, id, events); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
}

func TestNewRequiresToken(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := New(nil, "tok", ""); err == nil {
		t.Fatal("New with nil store must fail")
	}
	if _, err := New(st, "", ""); err == nil || !strings.Contains(err.Error(), "token required") {
		t.Fatalf("New with empty token err = %v, want token-required", err)
	}
}

func TestAuthRequired(t *testing.T) {
	srv, _ := newTestServer(t, "secret")
	h := srv.Handler()
	if rec := doReq(t, h, "GET", "/api/sessions", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token → %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, "GET", "/api/sessions", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token → %d, want 401", rec.Code)
	}
	if rec := doReq(t, h, "GET", "/api/sessions", "secret"); rec.Code != http.StatusOK {
		t.Fatalf("right token → %d, want 200", rec.Code)
	}
	// The static shell is gated too (D-WEB-2: full-route auth).
	if rec := doReq(t, h, "GET", "/", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("static / without token → %d, want 401", rec.Code)
	}
}

func TestSessionsList(t *testing.T) {
	srv, st := newTestServer(t, "tok")
	ctx := context.Background()
	now := time.Now()
	if err := st.CreateSession(ctx, "s-1", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, "s-2", now); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, srv.Handler(), "GET", "/api/sessions", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions → %d, want 200", rec.Code)
	}
	var list []sessionView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("sessions = %d, want 2", len(list))
	}
	if list[0].ID != "s-2" {
		t.Fatalf("first session = %q, want s-2 (most recently updated first)", list[0].ID)
	}
}

func TestSessionEvents(t *testing.T) {
	srv, st := newTestServer(t, "tok")
	seedSession(t, st, "s-1", []session.Event{
		{Seq: 1, Type: "user/message", At: time.Now(), Version: 1, Data: mustData(t, map[string]any{"Text": "你好"})},
		{Seq: 2, Type: "assistant/message", At: time.Now(), Version: 1, Data: mustData(t, map[string]any{"Text": "你好！"})},
		{Seq: 3, Type: "tool/result", At: time.Now(), Version: 1, Data: mustData(t, map[string]any{"Name": "get_time", "Output": "2026-08-20"})},
	})
	rec := doReq(t, srv.Handler(), "GET", "/api/sessions/s-1/events", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("events → %d, want 200", rec.Code)
	}
	var evs []eventView
	if err := json.Unmarshal(rec.Body.Bytes(), &evs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3", len(evs))
	}
	if evs[0].Type != "user/message" || evs[0].Summary != "你好" {
		t.Fatalf("ev[0] = %+v, want user/message summary 你好", evs[0])
	}
	if !strings.Contains(evs[2].Summary, "get_time") {
		t.Fatalf("tool/result summary = %q, want it to mention get_time", evs[2].Summary)
	}
	// Unknown session → 404.
	if rec := doReq(t, srv.Handler(), "GET", "/api/sessions/s-nope/events", "tok"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session → %d, want 404", rec.Code)
	}
}

func TestStaticServed(t *testing.T) {
	srv, _ := newTestServer(t, "tok")
	h := srv.Handler()
	if rec := doReq(t, h, "GET", "/", "tok"); rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET / → %d %q, want 200 text/html", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := doReq(t, h, "GET", "/static/app.js", "tok"); rec.Code != http.StatusOK {
		t.Fatalf("GET /static/app.js → %d, want 200", rec.Code)
	}
	if rec := doReq(t, h, "GET", "/nope", "tok"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nope → %d, want 404", rec.Code)
	}
}

func TestSummaryBound(t *testing.T) {
	srv, st := newTestServer(t, "tok")
	long := strings.Repeat("字", 500)
	seedSession(t, st, "s-1", []session.Event{
		{Seq: 1, Type: "user/message", At: time.Now(), Version: 1, Data: mustData(t, map[string]any{"Text": long})},
	})
	rec := doReq(t, srv.Handler(), "GET", "/api/sessions/s-1/events", "tok")
	var evs []eventView
	if err := json.Unmarshal(rec.Body.Bytes(), &evs); err != nil {
		t.Fatal(err)
	}
	s := evs[0].Summary
	if len([]rune(s)) != maxSummary+1 { // 200 runes + "…"
		t.Fatalf("summary runes = %d, want %d+1 (bounded + ellipsis)", len([]rune(s)), maxSummary)
	}
	if !strings.HasSuffix(s, "…") {
		t.Fatalf("summary %q should end with …", s)
	}
}

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t, "tok")
	rec := doReq(t, srv.Handler(), "GET", "/api/health", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("health → %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != true {
		t.Fatalf("health body = %v, want ok:true", body)
	}
}

func mustData(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	return b
}
