package terminal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"personal-agent/internal/session"
	"personal-agent/internal/tools"
)

// fakeAccess implements TerminalAccess for tests. It does not enforce an
// owner fence — owner checks live in the composed root's GetActive, not here.
type fakeAccess struct {
	sess  *Session
	owner string
}

func (f *fakeAccess) Owner() string { return f.owner }

func (f *fakeAccess) GetActive() (*Session, error) {
	if f.sess == nil {
		return nil, fmt.Errorf("no active terminal session")
	}
	return f.sess, nil
}

func (f *fakeAccess) Start(opts SessionOpts) (*Session, error) {
	if f.sess != nil {
		return nil, fmt.Errorf("already active")
	}
	f.sess, _ = NewSession(opts)
	return f.sess, nil
}

func (f *fakeAccess) Stop() error { f.sess = nil; return nil }

func newTestTools(t *testing.T) (tools *TerminalTools, acc *fakeAccess, events *[]string) {
	events = &[]string{}
	acc = &fakeAccess{owner: "s-1"}
	tools = NewTerminalTools(acc, func(typ string, data any) {
		*events = append(*events, typ)
	})
	return tools, acc, events
}

func execTool(t *testing.T, tool tools.Tool, args map[string]any) (string, error) {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), b)
}

func testSessionOpts() SessionOpts {
	return SessionOpts{IdleMS: 100, TimeoutMS: 2000}
}

func containsEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// pollReadOutput repeatedly reads until the tool returns non-empty output or
// the timeout elapses. Terminal output arrives asynchronously from the pty, so
// a single immediate read can race the shell's startup banner.
func pollReadOutput(t *testing.T, tt *TerminalTools, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := execTool(t, tt.Read(), map[string]any{})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if out != "" {
			return out
		}
		time.Sleep(25 * time.Millisecond)
	}
	return ""
}

func TestToolsStartAndStop(t *testing.T) {
	tt, _, evts := newTestTools(t)

	out, err := execTool(t, tt.Start(), map[string]any{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(out, "started terminal session") {
		t.Fatalf("start output = %q, want contains %q", out, "started terminal session")
	}
	if !containsEvent(*evts, session.EventTerminalStart) {
		t.Fatalf("events = %v, want %q", *evts, session.EventTerminalStart)
	}

	out, err = execTool(t, tt.Stop(), map[string]any{})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !strings.Contains(out, "stopped") {
		t.Fatalf("stop output = %q, want contains %q", out, "stopped")
	}
	if !containsEvent(*evts, session.EventTerminalStop) {
		t.Fatalf("events = %v, want %q", *evts, session.EventTerminalStop)
	}
}

func TestToolsStartWithCommand(t *testing.T) {
	tt, _, _ := newTestTools(t)

	out, err := execTool(t, tt.Start(), map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("output = %q, want contains %q", out, "hi")
	}
}

func TestToolsAlreadyActive(t *testing.T) {
	tt, acc, _ := newTestTools(t)
	acc.sess, _ = NewSession(testSessionOpts())

	_, err := execTool(t, tt.Start(), map[string]any{})
	if err == nil {
		t.Fatal("expected error when a session is already active")
	}
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("err = %v, want contains %q", err, "already active")
	}
}

func TestToolsWriteReadSignal(t *testing.T) {
	tt, _, _ := newTestTools(t)
	if _, err := execTool(t, tt.Start(), map[string]any{}); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Read returns only output not yet consumed. The shell's startup banner
	// arrives asynchronously after start, so poll until read surfaces it.
	out := pollReadOutput(t, tt, 2*time.Second)
	if out == "" {
		t.Fatal("read returned no startup output before timeout")
	}

	out, err := execTool(t, tt.Write(), map[string]any{"text": "echo hello"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("write output = %q, want contains %q", out, "hello")
	}

	// A submit write waits for and drains its own command output, so the
	// immediately-following read sees nothing new.
	out, err = execTool(t, tt.Read(), map[string]any{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != "" {
		t.Fatalf("read after drained write = %q, want empty", out)
	}

	out, err = execTool(t, tt.Signal(), map[string]any{"kind": "interrupt"})
	if err != nil {
		t.Fatalf("signal: %v", err)
	}
	if !strings.Contains(out, "interrupt") {
		t.Fatalf("signal output = %q, want contains %q", out, "interrupt")
	}
}

func TestToolsNoActiveSession(t *testing.T) {
	tt, _, _ := newTestTools(t)
	cases := []struct {
		name string
		tool func() tools.Tool
		args map[string]any
	}{
		{"write", func() tools.Tool { return tt.Write() }, map[string]any{"text": "echo x"}},
		{"read", func() tools.Tool { return tt.Read() }, map[string]any{}},
		{"signal", func() tools.Tool { return tt.Signal() }, map[string]any{"kind": "interrupt"}},
		{"stop", func() tools.Tool { return tt.Stop() }, map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := execTool(t, tc.tool(), tc.args)
			if err == nil {
				t.Fatal("expected error when no active terminal session")
			}
			if !strings.Contains(err.Error(), "no active terminal session") {
				t.Fatalf("err = %v, want contains %q", err, "no active terminal session")
			}
		})
	}
}

func TestToolsExitAfterExit(t *testing.T) {
	tt, acc, _ := newTestTools(t)
	if _, err := execTool(t, tt.Start(), map[string]any{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	sess, err := acc.GetActive()
	if err != nil {
		t.Fatalf("get active: %v", err)
	}

	// Send "exit"; tolerate an error on this write since the session may tear
	// down while the write's wait drains. The poll below verifies the exit.
	if _, err := execTool(t, tt.Write(), map[string]any{"text": "exit"}); err != nil {
		t.Logf("write exit returned error (expected during teardown): %v", err)
	}

	// Wait for the session to reach the exited state (max 3s).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Status().Kind == "exited" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if sess.Status().Kind != "exited" {
		t.Fatalf("session did not exit after 'exit' command, status kind = %q", sess.Status().Kind)
	}

	// Writing after exit must fail with either a session-exited or
	// no-active-session error, depending on where the check happens.
	_, err = execTool(t, tt.Write(), map[string]any{"text": "echo after"})
	if err == nil {
		t.Fatal("expected error writing after session exit")
	}
	if !strings.Contains(err.Error(), "exited") && !strings.Contains(err.Error(), "no active") {
		t.Fatalf("err = %v, want contains %q or %q", err, "exited", "no active")
	}
}

func TestToolsTruncate(t *testing.T) {
	tt, _, _ := newTestTools(t)
	if _, err := execTool(t, tt.Start(), map[string]any{}); err != nil {
		t.Fatalf("start: %v", err)
	}

	long := strings.Repeat("x", 9000)
	out, err := execTool(t, tt.Write(), map[string]any{"text": "echo " + long})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "[terminal output truncated]") {
		t.Fatalf("output = %q, want truncation marker %q", out, "[terminal output truncated]")
	}
}

func TestToolsSubmitDefault(t *testing.T) {
	tt, _, _ := newTestTools(t)
	if _, err := execTool(t, tt.Start(), map[string]any{}); err != nil {
		t.Fatalf("start: %v", err)
	}

	out, err := execTool(t, tt.Write(), map[string]any{"text": "echo ok"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("output = %q, want contains %q", out, "ok")
	}
}
