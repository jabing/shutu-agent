// webserver_test.go — the M10 portal tests (docs/dispatch-m10.md §3): New
// validation, bearer auth, sessions/events JSON API, static hosting, the
// bounded event summary, and the /api/stats dashboard rollup (M10c §3). The
// store is a real SQLite backend on a temp dir (the same backend the REPL
// uses), seeded through CreateSession + AppendEvents.
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

func TestStats(t *testing.T) {
	srv, st := newTestServer(t, "tok")
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	seedSession(t, st, "s-1", []session.Event{
		{Seq: 1, Type: "user/message", At: base, Version: 1, Data: mustData(t, map[string]any{"Text": "hi"})},
		{Seq: 2, Type: "tool/result", At: base.Add(time.Minute), Version: 1, Data: mustData(t, map[string]any{"Name": "get_time", "Output": "now"})},
		{Seq: 3, Type: "tool/result", At: base.Add(2 * time.Minute), Version: 1, Data: mustData(t, map[string]any{"Name": "web_search", "Output": "ok"})},
	})
	seedSession(t, st, "s-2", []session.Event{
		{Seq: 1, Type: "assistant/message", At: base.Add(30 * time.Minute), Version: 1, Data: mustData(t, map[string]any{"Text": "hi there"})},
		{Seq: 2, Type: "tool/error", At: base.Add(31 * time.Minute), Version: 1, Data: mustData(t, map[string]any{"Name": "fs_read", "Err": "denied"})},
		{Seq: 3, Type: "user/message", At: base.Add(32 * time.Minute), Version: 1, Data: mustData(t, map[string]any{"Text": "again"})},
	})
	// Auth: /api/stats without a token → 401 (same middleware as the rest).
	if rec := doReq(t, srv.Handler(), "GET", "/api/stats", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stats without token → %d, want 401", rec.Code)
	}
	rec := doReq(t, srv.Handler(), "GET", "/api/stats", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats → %d, want 200", rec.Code)
	}
	var stv statsView
	if err := json.Unmarshal(rec.Body.Bytes(), &stv); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stv.SessionsTotal != 2 || stv.EventsTotal != 6 || stv.ToolCalls != 2 {
		t.Fatalf("stats totals = s%d e%d t%d, want s2 e6 t2", stv.SessionsTotal, stv.EventsTotal, stv.ToolCalls)
	}
	want := map[string]int{"user/message": 2, "assistant/message": 1, "tool/result": 2, "tool/error": 1}
	if len(stv.EventTypeCounts) != len(want) {
		t.Fatalf("event_type_counts = %v, want %v", stv.EventTypeCounts, want)
	}
	for k, v := range want {
		if stv.EventTypeCounts[k] != v {
			t.Fatalf("event_type_counts[%q] = %d, want %d", k, stv.EventTypeCounts[k], v)
		}
	}
	wantActive := base.Add(32 * time.Minute)
	if !stv.LastActive.Equal(wantActive) {
		t.Fatalf("last_active = %v, want %v", stv.LastActive, wantActive)
	}
}

func TestStatsEmpty(t *testing.T) {
	srv, _ := newTestServer(t, "tok")
	rec := doReq(t, srv.Handler(), "GET", "/api/stats", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats → %d, want 200", rec.Code)
	}
	var stv statsView
	if err := json.Unmarshal(rec.Body.Bytes(), &stv); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stv.SessionsTotal != 0 || stv.EventsTotal != 0 || stv.ToolCalls != 0 {
		t.Fatalf("empty stats = s%d e%d t%d, want all 0", stv.SessionsTotal, stv.EventsTotal, stv.ToolCalls)
	}
	if len(stv.EventTypeCounts) != 0 {
		t.Fatalf("event_type_counts = %v, want empty", stv.EventTypeCounts)
	}
	if !stv.LastActive.IsZero() {
		t.Fatalf("last_active = %v, want zero", stv.LastActive)
	}
}

// TestKBAdminStub verifies the M10b placeholder (ADR D-WEB-6): the /api/kb/*
// routes sit behind auth and answer 501 until KB 全量 lands.
func TestKBAdminStub(t *testing.T) {
	srv, _ := newTestServer(t, "tok")
	h := srv.Handler()
	if rec := doReq(t, h, "GET", "/api/kb", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("kb without token → %d, want 401", rec.Code)
	}
	for _, p := range []string{"/api/kb", "/api/kb/bases", "/api/kb/bases/b1/entries"} {
		rec := doReq(t, h, "GET", p, "tok")
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("GET %s → %d, want 501", p, rec.Code)
		}
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
