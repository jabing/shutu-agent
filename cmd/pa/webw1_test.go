// webw1_test.go — the M10 W1 composition-root tests (docs/dispatch-m10-web2.md
// §5): runTurn serialization (D5), webMessage dispatch with implicit resume,
// the eventHub publish/subscribe semantics (including the drop-slow-subscriber
// policy), and the registerWebServer injection assertion. The webserver-side
// API tests live in internal/webserver.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"personal-agent/internal/config"
	"personal-agent/internal/llm"
	"personal-agent/internal/prompt"
	"personal-agent/internal/session"
	"personal-agent/internal/store"
	"personal-agent/internal/tools"
)

// turnLLM is a scripted llm.LLM for the W1 turn tests: it records how many
// Stream calls are in flight at once (maxActive) so a test can assert that
// turnMu serializes, and returns a fixed single-step answer.
type turnLLM struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
}

func (l *turnLLM) Stream(ctx context.Context, req llm.ChatRequest) (llm.StreamReader, error) {
	l.mu.Lock()
	l.calls++
	l.active++
	if l.active > l.maxActive {
		l.maxActive = l.active
	}
	l.mu.Unlock()
	time.Sleep(30 * time.Millisecond) // widen the overlap window so a race is visible
	l.mu.Lock()
	l.active--
	l.mu.Unlock()
	return &turnReader{events: []llm.StreamEvent{
		{Kind: llm.StreamTextDelta, Text: "hello"},
		{Kind: llm.StreamFinish, FinishReason: "stop"},
	}}, nil
}

type turnReader struct {
	events []llm.StreamEvent
	i      int
}

func (r *turnReader) Next() (llm.StreamEvent, error) {
	if r.i >= len(r.events) {
		return llm.StreamEvent{}, io.EOF
	}
	ev := r.events[r.i]
	r.i++
	return ev, nil
}

// makeTurnApp builds a minimal app able to run a real loop turn: only the
// fields newLoop touches (cfg.Model, llm, log, reg, prompt) are set; all the
// optional seams stay nil so preStepInjectors contributes nothing.
func makeTurnApp() *app {
	return &app{
		cfg:    config.Config{Model: "m"},
		llm:    &turnLLM{},
		reg:    tools.New(),
		prompt: prompt.New("You are a test agent."),
		log:    session.New(),
	}
}

// TestRunTurnSerial verifies D5 (M10 W1): concurrent runTurn calls share the
// global turnMu, so at most one loop Run (one LLM Stream) is in flight at any
// moment and every message still produces its own turn's events.
func TestRunTurnSerial(t *testing.T) {
	llm := &turnLLM{}
	a := makeTurnApp()
	a.llm = llm
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := a.runTurn(context.Background(), fmt.Sprintf("msg-%d", n), false); err != nil {
				t.Errorf("runTurn: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if llm.maxActive != 1 {
		t.Fatalf("max concurrent LLM streams = %d, want 1 (turnMu must serialize)", llm.maxActive)
	}
	if llm.calls != 5 {
		t.Fatalf("LLM calls = %d, want 5", llm.calls)
	}
	if n := len(a.log.Events()); n != 15 { // 5 turns × (user/message + assistant/chunk + assistant/message)
		t.Fatalf("log events = %d, want 15", n)
	}
}

// TestWebMessageRunsTurn verifies webMessage dispatches a turn on the current
// session: the log gains user/message, assistant/chunk and assistant/message.
func TestWebMessageRunsTurn(t *testing.T) {
	a := makeTurnApp()
	a.currentID = "s-a"
	if err := a.webMessage(context.Background(), "s-a", "hi"); err != nil {
		t.Fatalf("webMessage: %v", err)
	}
	for _, typ := range []string{session.EventUserMessage, session.EventAssistantChunk, session.EventAssistantMessage} {
		if !hasEvent(a.log, typ) {
			t.Fatalf("log missing %s after webMessage", typ)
		}
	}
}

// TestWebMessageResumesOtherSession verifies webMessage implicitly resumes a
// target session that differs from the current one (D-WEB2-A): the turn runs on
// the resumed session (its store gains the events) and the previous session
// stays untouched.
func TestWebMessageResumesOtherSession(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, id := range []string{"s-a", "s-other"} {
		if err := st.CreateSession(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	a := makeTurnApp()
	a.store = st
	a.currentID = "s-a"
	if err := a.webMessage(ctx, "s-other", "hi"); err != nil {
		t.Fatalf("webMessage: %v", err)
	}
	if a.currentID != "s-other" {
		t.Fatalf("currentID = %q, want s-other (resumed)", a.currentID)
	}
	events, err := st.LoadSession(ctx, "s-other")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("the resumed session must hold the turn's events")
	}
	prev, err := st.LoadSession(ctx, "s-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(prev) != 0 {
		t.Fatalf("s-a events = %d, want 0 (the turn must run on the resumed session)", len(prev))
	}
}

// TestEventHubPublishSubscribe verifies the hub delivers an event to the
// subscribers of the session only, and that unsubscribe closes the channel.
func TestEventHubPublishSubscribe(t *testing.T) {
	h := NewEventHub()
	ch, unsub := h.Subscribe("s-1")
	h.Publish("s-1", session.Event{Seq: 1, Type: session.EventUserMessage})
	select {
	case got := <-ch:
		if got.Seq != 1 {
			t.Fatalf("got seq %d, want 1", got.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the published event")
	}
	// A different session id must not leak into this subscriber.
	select {
	case got := <-ch:
		t.Fatalf("received an event for the wrong session: %+v", got)
	default:
	}
	unsub()
	if _, ok := <-ch; ok {
		t.Fatal("channel must be closed after unsubscribe")
	}
}

// TestEventHubDropsSlowSubscriber verifies the drop policy (dispatch-m10-web2
// §2): publishing to a subscriber whose buffer is full never blocks the caller.
func TestEventHubDropsSlowSubscriber(t *testing.T) {
	h := NewEventHub()
	_, unsub := h.Subscribe("s-1")
	defer unsub()
	for i := 0; i < eventHubBuffer; i++ {
		h.Publish("s-1", session.Event{Seq: uint64(i)})
	}
	start := time.Now()
	h.Publish("s-1", session.Event{Seq: 999})
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("publish to a full-buffer subscriber must not block")
	}
}

// TestRegisterWebServerInjectsHandlers verifies registerWebServer injects the
// message handler, session manager and event source into the webserver
// (dispatch-m10-web2 §2/§5): all three Handlers() fields must be non-nil.
func TestRegisterWebServerInjectsHandlers(t *testing.T) {
	a, st := makeWebServerApp(t, true, "tok")
	defer st.Close()
	if err := a.registerWebServer(); err != nil {
		t.Fatalf("registerWebServer: %v", err)
	}
	defer a.webserver.Close()
	h := a.webserver.Handlers()
	if h.Message == nil || h.Session == nil || h.Event == nil {
		t.Fatalf("injected handlers = %+v, want all three non-nil", h)
	}
}

// TestWebConfigRedacts verifies webConfig (M10 W2, ADR D-WEB2-D): the sanitized
// view never leaks the web_server token plaintext, the model/provider/mode and
// the capability gates are correct, the tool whitelist carries its count plus a
// bounded list, and registerWebServer wires the provider into the webserver
// (Handlers().Config non-nil).
func TestWebConfigRedacts(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := &app{
		cfg: config.Config{
			Model:   "deepseek-chat",
			BaseURL: "https://api.example.com/v1",
			Mode:    "standard",
			LLM: config.LLMConfig{
				Provider:   "openai",
				Multimodal: config.MultimodalConfig{Enabled: true},
			},
			Tools:      config.ToolsConfig{Enabled: []string{"get_time", "read_file", "web_search"}},
			Web:        config.WebConfig{Enabled: true},
			KB:         config.KBConfig{Enabled: true},
			Compaction: config.CompactionConfig{Enabled: true},
			Eval:       config.EvalConfig{Enabled: false},
			WebServer:  config.WebServerConfig{Enabled: true, Addr: "127.0.0.1:0", Token: "super-secret"},
		},
		store: st,
	}
	if err := a.registerWebServer(); err != nil {
		t.Fatalf("registerWebServer: %v", err)
	}
	defer a.webserver.Close()

	v := a.webConfig()

	// Redaction: the token key is absent (webConfig omits it) or masked; the
	// plaintext never appears anywhere in the serialized view.
	if tok, ok := v["web_server.token"]; ok && tok != "***" {
		t.Fatalf("web_server.token = %v, want absent or \"***\"", tok)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "super-secret") {
		t.Fatal("webConfig leaks the token plaintext")
	}

	// Model / provider / mode / base_url.
	if v["model"] != "deepseek-chat" || v["llm_provider"] != "openai" || v["mode"] != "standard" {
		t.Fatalf("model/provider/mode = %v/%v/%v, want deepseek-chat/openai/standard",
			v["model"], v["llm_provider"], v["mode"])
	}
	if v["base_url"] != "https://api.example.com/v1" {
		t.Fatalf("base_url = %v, want https://api.example.com/v1", v["base_url"])
	}

	// Capability gates.
	for key, want := range map[string]bool{
		"web_enabled": true, "kb_enabled": true, "compaction_enabled": true,
		"multimodal_enabled": true, "eval_enabled": false, "jobs_enabled": false,
	} {
		if got, _ := v[key].(bool); got != want {
			t.Fatalf("%s = %v, want %v", key, v[key], want)
		}
	}

	// Web server address.
	if v["web_server_addr"] != "127.0.0.1:0" {
		t.Fatalf("web_server_addr = %v, want 127.0.0.1:0", v["web_server_addr"])
	}

	// Tool whitelist: count + bounded list.
	if v["tools_enabled_count"] != 3 {
		t.Fatalf("tools_enabled_count = %v, want 3", v["tools_enabled_count"])
	}
	tl, ok := v["tools_enabled"].([]string)
	if !ok || len(tl) != 3 || tl[0] != "get_time" {
		t.Fatalf("tools_enabled = %#v, want the whitelist list", v["tools_enabled"])
	}

	// The config provider is wired into the webserver.
	h := a.webserver.Handlers()
	if h.Config == nil {
		t.Fatal("registerWebServer must wire SetConfigProvider(a.webConfig)")
	}
	if got := h.Config(); got["model"] != "deepseek-chat" {
		t.Fatalf("wired cfgFn returned model %v, want deepseek-chat", got["model"])
	}
}

// TestWebSessionNewThenMessageAfterRequestCtxCancelled is the M10 W3 regression
// for the real-smoke catch: the persist sink must survive the request ctx that
// created/resumed the session. webSessionManager/webMessage run with an HTTP
// request ctx (r.Context()) that is cancelled the instant its handler returns;
// if the sink captured that ctx, every later append failed with "store: begin
// append: context canceled" and the web message returned 500. The sink persists
// under the process-level baseCtx (Background in tests), so a later message
// still lands.
func TestWebSessionNewThenMessageAfterRequestCtxCancelled(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a := makeTurnApp()
	a.store = st
	a.baseCtx = context.Background() // the process-lifetime ctx (main sets it)
	a.currentID = ""

	// Simulate POST /api/sessions: a request-scoped ctx that is cancelled when
	// the handler returns (r.Context() semantics).
	reqCtx, cancel := context.WithCancel(context.Background())
	sid, err := a.webSessionManager(reqCtx, "new", "")
	cancel() // handler returned -> r.Context() is now cancelled
	if err != nil {
		t.Fatalf("webSessionManager new: %v", err)
	}

	// A later, unrelated request's message must still persist (the sink must
	// NOT be bound to the cancelled request ctx).
	if err := a.webMessage(context.Background(), sid, "hello"); err != nil {
		t.Fatalf("webMessage after request ctx cancelled: %v (persist sink must use baseCtx)", err)
	}

	evs, err := st.LoadSession(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{session.EventUserMessage, session.EventAssistantChunk, session.EventAssistantMessage} {
		found := false
		for _, ev := range evs {
			if ev.Type == typ {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("store missing %s after message (persist failed under cancelled ctx)", typ)
		}
	}
}
