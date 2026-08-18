package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fakeTool records whether it was executed, so tests can prove schema
// validation happens before dispatch (D7).
type fakeTool struct {
	name     string
	schema   map[string]any
	executed bool
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake tool" }
func (f *fakeTool) Schema() map[string]any {
	if f.schema != nil {
		return f.schema
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (f *fakeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	f.executed = true
	return "ok", nil
}

func TestRegisterDuplicate(t *testing.T) {
	r := New()
	if err := r.Register(&fakeTool{name: "x"}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(&fakeTool{name: "x"}); err == nil {
		t.Fatal("duplicate register should fail")
	}
}

func TestExecuteUnknownTool(t *testing.T) {
	r := New()
	if _, err := r.Execute(context.Background(), "nope", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown tool should fail")
	}
}

func TestExecuteValidatesArgumentsBeforeDispatch(t *testing.T) {
	ft := &fakeTool{
		name: "needs_path",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	}
	r := New()
	r.Register(ft)

	// Missing required field: must be rejected, tool must not run.
	if _, err := r.Execute(context.Background(), "needs_path", json.RawMessage(`{}`)); err == nil {
		t.Fatal("invalid args should be rejected")
	}
	if ft.executed {
		t.Fatal("tool executed despite invalid arguments (D7 violated)")
	}

	// Valid arguments: tool runs.
	out, err := r.Execute(context.Background(), "needs_path", json.RawMessage(`{"path":"/a"}`))
	if err != nil {
		t.Fatalf("valid args: %v", err)
	}
	if out != "ok" || !ft.executed {
		t.Fatalf("out=%q executed=%v", out, ft.executed)
	}
}

func TestExecuteMalformedJSON(t *testing.T) {
	r := New()
	r.Register(&fakeTool{name: "x"})
	if _, err := r.Execute(context.Background(), "x", json.RawMessage(`not json`)); err == nil {
		t.Fatal("malformed JSON should be rejected")
	}
}

func TestSpecsSorted(t *testing.T) {
	r := New()
	r.Register(&fakeTool{name: "zebra"})
	r.Register(&fakeTool{name: "alpha"})
	specs := r.Specs()
	if len(specs) != 2 {
		t.Fatalf("specs len = %d, want 2", len(specs))
	}
	if specs[0].Name != "alpha" || specs[1].Name != "zebra" {
		t.Fatalf("specs not sorted: %+v", specs)
	}
}

func TestGetTime(t *testing.T) {
	r := New()
	r.Register(GetTime{})
	out, err := r.Execute(context.Background(), "get_time", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("get_time: %v", err)
	}
	if out == "" {
		t.Fatal("get_time returned empty")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello agent"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	r := New()
	r.Register(ReadFile{})
	args, _ := json.Marshal(map[string]string{"path": path})
	out, err := r.Execute(context.Background(), "read_file", args)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out != "hello agent" {
		t.Fatalf("read_file out = %q", out)
	}
}

func TestReadFileMissing(t *testing.T) {
	r := New()
	r.Register(ReadFile{})
	if _, err := r.Execute(context.Background(), "read_file", json.RawMessage(`{"path":"/definitely/not/here"}`)); err == nil {
		t.Fatal("missing file should fail")
	}
}
