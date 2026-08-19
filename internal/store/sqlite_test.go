package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"personal-agent/internal/llm"
	"personal-agent/internal/session"
)

func openSQLite(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "pa.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// buildLog appends a representative mini-conversation through a session.Log
// whose sink forwards to the store, returning the log for later comparison.
func buildLog(t *testing.T, st Store, id string, wantDerived int) *session.Log {
	t.Helper()
	log := session.New()
	log.SetSink(func(ev session.Event) error {
		return st.AppendEvents(context.Background(), id, []session.Event{ev})
	})
	must := func(typ string, data any) {
		if _, err := log.Append(typ, data); err != nil {
			t.Fatalf("append %s: %v", typ, err)
		}
	}
	must(session.EventUserMessage, session.NewUserMessage("what time is it"))
	must(session.EventAssistantChunk, session.NewAssistantChunk("Let "))
	must(session.EventAssistantChunk, session.NewAssistantChunk("me check"))
	must(session.EventAssistantMessage, session.NewAssistantMessage("Let me check", nil, "stop"))
	if wantDerived > 0 && len(log.DeriveHistory()) != wantDerived {
		t.Fatalf("derived %d messages, want %d", len(log.DeriveHistory()), wantDerived)
	}
	return log
}

func assertEventsEqual(t *testing.T, want, got []session.Event) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("event count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Seq != g.Seq {
			t.Errorf("event %d: seq = %d, want %d", i, g.Seq, w.Seq)
		}
		if w.Type != g.Type {
			t.Errorf("event %d: type = %q, want %q", i, g.Type, w.Type)
		}
		if w.Version != g.Version {
			t.Errorf("event %d: version = %d, want %d", i, g.Version, w.Version)
		}
		if w.At.UnixNano() != g.At.UnixNano() {
			t.Errorf("event %d: at = %v, want %v", i, g.At, w.At)
		}
		if !bytes.Equal(w.Data, g.Data) {
			t.Errorf("event %d: data = %s, want %s", i, g.Data, w.Data)
		}
	}
}

// TestReplayEventsConsistent persists events, closes the store, reopens it,
// and verifies the reloaded events match one-by-one and derive the same
// history (dispatch-m2: "事件逐条一致、派生历史一致").
func TestReplayEventsConsistent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pa.db")
	ctx := context.Background()

	st1, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const id = "s-replay"
	if err := st1.CreateSession(ctx, id, time.Now().UTC()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	want := buildLog(t, st1, id, 2) // user + assistant = 2 derived messages
	st1.Close()

	st2, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	events, err := st2.LoadSession(ctx, id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	assertEventsEqual(t, want.Events(), events)

	// Derived history must be identical after replay.
	replayed := session.New()
	if err := replayed.Restore(events); err != nil {
		t.Fatalf("restore: %v", err)
	}
	h1, h2 := want.DeriveHistory(), replayed.DeriveHistory()
	if len(h1) != len(h2) {
		t.Fatalf("derived history len = %d, want %d", len(h2), len(h1))
	}
	for i := range h1 {
		if h1[i].Role != h2[i].Role || h1[i].Content != h2[i].Content {
			t.Errorf("history %d: got %+v, want %+v", i, h2[i], h1[i])
		}
	}
}

// TestMultiSessionRestore verifies two sessions coexist: each loads only its
// own events and /list reports both (dispatch-m2: "多会话恢复").
func TestMultiSessionRestore(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	a := session.New()
	a.SetSink(func(ev session.Event) error { return st.AppendEvents(ctx, "s-a", []session.Event{ev}) })
	if _, err := a.Append(session.EventUserMessage, session.NewUserMessage("hello A")); err != nil {
		t.Fatalf("append A: %v", err)
	}

	b := session.New()
	b.SetSink(func(ev session.Event) error { return st.AppendEvents(ctx, "s-b", []session.Event{ev}) })
	if _, err := b.Append(session.EventUserMessage, session.NewUserMessage("hello B")); err != nil {
		t.Fatalf("append B: %v", err)
	}

	metas, err := st.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("listed %d sessions, want 2: %+v", len(metas), metas)
	}

	evA, err := st.LoadSession(ctx, "s-a")
	if err != nil {
		t.Fatalf("load A: %v", err)
	}
	evB, err := st.LoadSession(ctx, "s-b")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if len(evA) != 1 || len(evB) != 1 {
		t.Fatalf("event counts A=%d B=%d, want 1 each", len(evA), len(evB))
	}
	if !bytes.Contains(evA[0].Data, []byte("hello A")) {
		t.Errorf("session A data = %s", evA[0].Data)
	}
	if !bytes.Contains(evB[0].Data, []byte("hello B")) {
		t.Errorf("session B data = %s", evB[0].Data)
	}

	// Restoring each into a fresh log yields the correct history.
	la := session.New()
	if err := la.Restore(evA); err != nil {
		t.Fatalf("restore A: %v", err)
	}
	h := la.DeriveHistory()
	if len(h) != 1 || h[0].Role != llm.RoleUser || h[0].Content != "hello A" {
		t.Errorf("session A history = %+v", h)
	}
}

// TestLoadNotFound verifies ErrNotFound for an unknown session id.
func TestLoadNotFound(t *testing.T) {
	st := openSQLite(t)
	if _, err := st.LoadSession(context.Background(), "s-nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadSession = %v, want ErrNotFound", err)
	}
}

// TestKBRecallEventPersistsAndReplays verifies the M4a kb/recall event type
// travels the durable append path end to end: session.Log sink → SQLiteStore →
// replay (design.md §3 / D8, D3 机制在 M4a 就位).
func TestKBRecallEventPersistsAndReplays(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()
	const id = "s-kb"
	log := session.New()
	log.SetSink(func(ev session.Event) error {
		return st.AppendEvents(ctx, id, []session.Event{ev})
	})
	if _, err := log.Append(session.EventKBRecall, session.NewKBRecall("架构", []session.RecallHit{
		{ID: "kb-1", Title: "架构决策记录", Snippet: "我们决定采用 SQLite FTS5…", Type: "decision", Score: 0.9},
	})); err != nil {
		t.Fatalf("append: %v", err)
	}
	events, err := st.LoadSession(ctx, id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 1 || events[0].Type != session.EventKBRecall {
		t.Fatalf("replayed = %+v, want one %q event", events, session.EventKBRecall)
	}
	if !bytes.Contains(events[0].Data, []byte("架构决策记录")) {
		t.Errorf("payload lost in round trip: %s", events[0].Data)
	}
}

// TestAppendMaterializesSession verifies appending to a never-created session
// materializes its row (defensive) and it then appears in /list.
func TestAppendMaterializesSession(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()
	ev := session.Event{Seq: 1, Type: session.EventUserMessage, Version: 1, At: time.Now().UTC(), Data: []byte(`{"text":"hi"}`)}
	if err := st.AppendEvents(ctx, "s-auto", []session.Event{ev}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := st.LoadSession(ctx, "s-auto")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
}
