package fs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"personal-agent/internal/session"
)

// eventRec is one event emitted through the FsTools onEvent sink.
type eventRec struct {
	typ  string
	data any
}

// newToolsWithEvents returns a FileService and an FsTools bundle wired to a
// slice that records every emitted fs/* event (the composition root wires the
// same sink to the session log in cmd/pa, D3).
func newToolsWithEvents(t *testing.T) (FileService, *FsTools, *[]eventRec) {
	t.Helper()
	svc := NewLocalFS(t.TempDir())
	t.Cleanup(func() { svc.Close() })
	recs := &[]eventRec{}
	return svc, NewFsTools(svc, func(typ string, data any) {
		*recs = append(*recs, eventRec{typ: typ, data: data})
	}), recs
}

// decodeEvent unmarshals a captured event payload into T (the session payloads
// are plain JSON-serializable data).
func decodeEvent[T any](t *testing.T, ev eventRec) T {
	t.Helper()
	raw, err := json.Marshal(ev.data)
	if err != nil {
		t.Fatalf("marshal %s event data: %v", ev.typ, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s event data %s: %v", ev.typ, raw, err)
	}
	return out
}

// eventTypes returns the emitted event types in order.
func eventTypes(recs []eventRec) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.typ)
	}
	return out
}

// TestFsToolSchemas verifies the D7 shapes the registry compiles and sends to
// the model (dispatch-m6f-3 §4): additionalProperties false and the required
// fields for each fs_* tool.
func TestFsToolSchemas(t *testing.T) {
	_, ft, _ := newToolsWithEvents(t)
	read := ft.Read().Schema()
	if read["type"] != "object" || read["additionalProperties"] != false {
		t.Fatalf("fs_read schema = %+v, want type object / additionalProperties false", read)
	}
	req, _ := read["required"].([]string)
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("fs_read required = %v, want [path]", req)
	}
	props, _ := read["properties"].(map[string]any)
	if _, ok := props["path"]; !ok {
		t.Fatal("fs_read path property missing")
	}

	write := ft.Write().Schema()
	if write["type"] != "object" || write["additionalProperties"] != false {
		t.Fatalf("fs_write schema = %+v, want type object / additionalProperties false", write)
	}
	wreq, _ := write["required"].([]string)
	if len(wreq) != 2 || wreq[0] != "path" || wreq[1] != "content" {
		t.Fatalf("fs_write required = %v, want [path content]", wreq)
	}
	wprops, _ := write["properties"].(map[string]any)
	if _, ok := wprops["content"]; !ok {
		t.Fatal("fs_write content property missing")
	}

	list := ft.List().Schema()
	if list["type"] != "object" || list["additionalProperties"] != false {
		t.Fatalf("fs_list schema = %+v, want type object / additionalProperties false", list)
	}
	lreq, _ := list["required"].([]string)
	if len(lreq) != 1 || lreq[0] != "dir" {
		t.Fatalf("fs_list required = %v, want [dir]", lreq)
	}
}

// TestFsReadToolReadsAndEmits covers the happy path: fs_read returns the file
// content and lands fs/read (path + byte size) through the event sink
// (dispatch-m6f-3 §4).
func TestFsReadToolReadsAndEmits(t *testing.T) {
	svc, ft, recs := newToolsWithEvents(t)
	ctx := context.Background()
	if err := svc.Write(ctx, "notes.txt", "hello fs"); err != nil {
		t.Fatalf("seed notes.txt: %v", err)
	}
	out, err := ft.Read().Execute(ctx, json.RawMessage(`{"path":"notes.txt"}`))
	if err != nil {
		t.Fatalf("fs_read: %v", err)
	}
	if out != "hello fs" {
		t.Fatalf("fs_read output = %q, want hello fs", out)
	}
	if got := eventTypes(*recs); len(got) != 1 || got[0] != session.EventFsRead {
		t.Fatalf("emitted types = %v, want [fs/read]", got)
	}
	d := decodeEvent[struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}](t, (*recs)[0])
	if d.Path != "notes.txt" || d.Size != len("hello fs") {
		t.Fatalf("fs/read payload = %+v, want path notes.txt / size %d", d, len("hello fs"))
	}
}

// TestFsWriteToolWritesAndEmits covers the happy path: fs_write creates the
// file (and its missing parents), returns the written path, and lands fs/write
// through the event sink.
func TestFsWriteToolWritesAndEmits(t *testing.T) {
	svc, ft, recs := newToolsWithEvents(t)
	ctx := context.Background()
	out, err := ft.Write().Execute(ctx, json.RawMessage(`{"path":"a/b/deep.txt","content":"deep"}`))
	if err != nil {
		t.Fatalf("fs_write: %v", err)
	}
	if !strings.Contains(out, "a/b/deep.txt") {
		t.Fatalf("fs_write output = %q, want it to carry the written path", out)
	}
	got, err := svc.Read(ctx, "a/b/deep.txt", 0)
	if err != nil || got != "deep" {
		t.Fatalf("read back = %q, %v, want deep", got, err)
	}
	if types := eventTypes(*recs); len(types) != 1 || types[0] != session.EventFsWrite {
		t.Fatalf("emitted types = %v, want [fs/write]", types)
	}
	d := decodeEvent[struct {
		Path string `json:"path"`
	}](t, (*recs)[0])
	if d.Path != "a/b/deep.txt" {
		t.Fatalf("fs/write payload = %+v, want path a/b/deep.txt", d)
	}
}

// TestFsListToolListsAndEmits covers the happy path: fs_list returns the
// formatted table and lands fs/list (dir + count) through the event sink.
func TestFsListToolListsAndEmits(t *testing.T) {
	svc, ft, recs := newToolsWithEvents(t)
	ctx := context.Background()
	if err := svc.Write(ctx, "notes.txt", "hello fs"); err != nil {
		t.Fatalf("seed notes.txt: %v", err)
	}
	if err := svc.Write(ctx, "d/inner.txt", "x"); err != nil {
		t.Fatalf("seed nested: %v", err)
	}
	out, err := ft.List().Execute(ctx, json.RawMessage(`{"dir":"."}`))
	if err != nil {
		t.Fatalf("fs_list: %v", err)
	}
	if !strings.Contains(out, "[.] 2 entries") || !strings.Contains(out, "notes.txt") || !strings.Contains(out, "d  dir") {
		t.Fatalf("fs_list output = %q, want the header and both entries", out)
	}
	if types := eventTypes(*recs); len(types) != 1 || types[0] != session.EventFsList {
		t.Fatalf("emitted types = %v, want [fs/list]", types)
	}
	d := decodeEvent[struct {
		Dir   string `json:"dir"`
		Count int    `json:"count"`
	}](t, (*recs)[0])
	if d.Dir != "." || d.Count != 2 {
		t.Fatalf("fs/list payload = %+v, want dir . / count 2", d)
	}
}

// TestFsToolsRejectBadArgs verifies the tools' own argument checks (the
// registry enforces the same via D7): empty path/dir and empty content errors
// are returned, and no fs/* event may be emitted on a failed call.
func TestFsToolsRejectBadArgs(t *testing.T) {
	_, ft, recs := newToolsWithEvents(t)
	ctx := context.Background()
	if _, err := ft.Read().Execute(ctx, json.RawMessage(`{"path":""}`)); err == nil {
		t.Fatal("fs_read with an empty path must error")
	}
	if _, err := ft.Read().Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("fs_read with no path must error")
	}
	if _, err := ft.Write().Execute(ctx, json.RawMessage(`{"path":"","content":"x"}`)); err == nil {
		t.Fatal("fs_write with an empty path must error")
	}
	if _, err := ft.Write().Execute(ctx, json.RawMessage(`{"path":"x.txt"}`)); err == nil {
		t.Fatal("fs_write with no content must error")
	}
	if _, err := ft.List().Execute(ctx, json.RawMessage(`{"dir":""}`)); err == nil {
		t.Fatal("fs_list with an empty dir must error")
	}
	if _, err := ft.List().Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatal("fs_list with no dir must error")
	}
	if len(*recs) != 0 {
		t.Fatalf("no event may be emitted on a failed call, got %v", eventTypes(*recs))
	}
}

// TestFsToolsReturnErrorNotPanicOnBoundaryAndMissing verifies failures are
// error messages to the model, never panics (dispatch-m6f-3 §4): a path
// escaping the root and a missing file both error and emit no event.
func TestFsToolsReturnErrorNotPanicOnBoundaryAndMissing(t *testing.T) {
	_, ft, recs := newToolsWithEvents(t)
	ctx := context.Background()
	if _, err := ft.Read().Execute(ctx, json.RawMessage(`{"path":"../escape.txt"}`)); err == nil {
		t.Fatal("fs_read of an escaping path must error")
	} else if !strings.Contains(err.Error(), "fs_read:") || strings.Contains(err.Error(), "panic") {
		t.Fatalf("fs_read error = %v, want a normal fs_read: error", err)
	}
	if _, err := ft.Read().Execute(ctx, json.RawMessage(`{"path":"nope.txt"}`)); err == nil {
		t.Fatal("fs_read of a missing file must error")
	}
	if _, err := ft.Write().Execute(ctx, json.RawMessage(`{"path":"../../x","content":"x"}`)); err == nil {
		t.Fatal("fs_write of an escaping path must error")
	}
	if _, err := ft.List().Execute(ctx, json.RawMessage(`{"dir":"../.."}`)); err == nil {
		t.Fatal("fs_list of an escaping dir must error")
	}
	if _, err := ft.List().Execute(ctx, json.RawMessage(`{"dir":"missing"}`)); err == nil {
		t.Fatal("fs_list of a missing dir must error")
	}
	if len(*recs) != 0 {
		t.Fatalf("no event may be emitted on a failed call, got %v", eventTypes(*recs))
	}
}
