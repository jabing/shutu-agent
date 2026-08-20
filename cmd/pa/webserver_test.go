// webserver_test.go - the M10a composition-root wiring tests (docs/dispatch-m10
// section 5): the D10 gate (disabled => no server), the fail-closed empty-token
// path, and the enabled path serving health/sessions through the authenticated
// handler. The server goroutine binds 127.0.0.1:0 (ephemeral) so tests never
// collide with a real port; assertions go through Handler() on httptest.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"personal-agent/internal/config"
	"personal-agent/internal/store"
)

func makeWebServerApp(t *testing.T, enabled bool, token string) (*app, *store.SQLiteStore) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	return &app{
		cfg: config.Config{WebServer: config.WebServerConfig{
			Enabled: enabled,
			Addr:    "127.0.0.1:0", // ephemeral in tests; production default is 127.0.0.1:8080
			Token:   token,
		}},
		store: st,
	}, st
}

// TestRegisterWebServerDisabledRegistersNothing verifies the D10 gate: with
// web_server.enabled=false the composition root leaves a.webserver nil and
// starts no listener (dispatch-m10 section 5).
func TestRegisterWebServerDisabledRegistersNothing(t *testing.T) {
	a, st := makeWebServerApp(t, false, "")
	defer st.Close()
	if err := a.registerWebServer(); err != nil {
		t.Fatalf("registerWebServer: %v", err)
	}
	if a.webserver != nil {
		t.Fatal("webserver must be nil when web_server.enabled=false")
	}
}

// TestRegisterWebServerEmptyTokenServesOpen verifies the D-WEB-2 change (user
// decision 2026-08-20): enabled with an empty token starts and serves open to
// the local machine (dsh-style, no login) — the old fail-closed stance is gone.
func TestRegisterWebServerEmptyTokenServesOpen(t *testing.T) {
	a, st := makeWebServerApp(t, true, "")
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateSession(ctx, "s-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.registerWebServer(); err != nil {
		t.Fatalf("registerWebServer: %v", err)
	}
	if a.webserver == nil {
		t.Fatal("webserver must be set when web_server.enabled=true (empty token = open)")
	}
	defer a.webserver.Close()

	ts := httptest.NewServer(a.webserver.Handler())
	defer ts.Close()
	// No token configured → an anonymous request is served.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health without token (no token configured) -> %d, want 200", resp.StatusCode)
	}
}

// TestRegisterWebServerEnabledServes verifies the enabled path: the server is
// set, the authenticated health/sessions APIs respond, and an unauthenticated
// request is rejected.
func TestRegisterWebServerEnabledServes(t *testing.T) {
	a, st := makeWebServerApp(t, true, "tok")
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateSession(ctx, "s-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.registerWebServer(); err != nil {
		t.Fatalf("registerWebServer: %v", err)
	}
	if a.webserver == nil {
		t.Fatal("webserver must be set when web_server.enabled=true")
	}
	defer a.webserver.Close()

	ts := httptest.NewServer(a.webserver.Handler())
	defer ts.Close()

	get := func(path, token string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("/api/health", "tok"); code != http.StatusOK {
		t.Fatalf("health with token -> %d, want 200", code)
	}
	if code := get("/api/health", ""); code != http.StatusUnauthorized {
		t.Fatalf("health without token -> %d, want 401", code)
	}
	if code := get("/api/health", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("health with wrong token -> %d, want 401", code)
	}
	if code := get("/api/sessions", "tok"); code != http.StatusOK {
		t.Fatalf("sessions with token -> %d, want 200", code)
	}
	if code := get("/", "tok"); code != http.StatusOK {
		t.Fatalf("static index with token -> %d, want 200", code)
	}
}
