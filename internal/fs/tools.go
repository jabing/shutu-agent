// tools.go — the M6f-3 Consumer half of the safe-file-operation seam
// (design.md §8 Consumer / D2, dispatch-m6f-3 §4): fs_read, fs_write and
// fs_list are registered into the tools.Registry by the composition root
// (cmd/pa) when fs.enabled, and auto-whitelisted by config.applyDefaults the
// same way the job_*/subagent_*/skill_*/schedule_*/plan_*/spill_*/interact_*/
// code_*/mcp_* tools are. The tools implement the tools.Tool method set
// structurally (Go structural typing), so this package never imports the
// tools package — the seam stays decoupled (D2).
//
// D7 is enforced by the registry: every Execute validates the model-generated
// arguments against the compiled JSON Schema below (additionalProperties:
// false; path/dir/content as plain strings) before this code runs; the checks
// are repeated here so a direct call can never bypass them.
//
// D3 event logging follows the M6e-2 tool-layer decision (ADR 决策 M6f /
// dispatch-m6f-3 §4): fs_read emits fs/read (path + returned byte size) on a
// successful read, fs_write emits fs/write (path) on a successful write, and
// fs_list emits fs/list (dir + entry count) on a successful listing — each
// through the injected onEvent sink (the composition root wires it to the
// session log), inside a tool Execute on the serial main-loop path (D5). A
// failed operation (a path escaping the allowed root, a missing file, a file
// over the 1MiB read cap, a missing directory) returns an error message to the
// model and logs nothing — the loop surfaces it as tool/error. Failures are
// never a panic (dispatch-m6f-3 §4: 路径越界/不存在返回错误消息，非 panic).
package fs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jabing/shutu-agent/internal/session"
)

// Tool names — the fs_* consumer tools (whitelisted when fs.enabled; see
// config.fsToolNames).
const (
	ToolReadName  = "fs_read"
	ToolWriteName = "fs_write"
	ToolListName  = "fs_list"
)

// FsTools bundles the shared state of the three fs_* tools: the FileService
// and the event sink. Keeping the bundle as fields keeps the constructor's
// signature the seam contract and the tool package decoupled from config (D2).
type FsTools struct {
	f       FileService
	onEvent func(typ string, data any)
}

// NewFsTools returns the fs_* tool bundle bound to a FileService. onEvent,
// when non-nil, receives the fs/* event payloads; the composition root wires
// it to the session log (D3).
func NewFsTools(f FileService, onEvent func(typ string, data any)) *FsTools {
	return &FsTools{f: f, onEvent: onEvent}
}

// emit forwards one fs/* event payload to the injected sink (D3).
func (t *FsTools) emit(typ string, data any) {
	if t.onEvent != nil {
		t.onEvent(typ, data)
	}
}

// Read returns the fs_read tool.
func (t *FsTools) Read() FsReadTool { return FsReadTool{t: t} }

// Write returns the fs_write tool.
func (t *FsTools) Write() FsWriteTool { return FsWriteTool{t: t} }

// List returns the fs_list tool.
func (t *FsTools) List() FsListTool { return FsListTool{t: t} }

// FsReadTool reads a file inside the allowed fs root (capped at the 1MiB
// read limit, DefaultMaxReadSize) and returns its content. The content is the
// model-facing result; the fs/read event records the path and the returned
// byte size as a log fact.
type FsReadTool struct {
	t *FsTools
}

func (FsReadTool) Name() string { return ToolReadName }

func (FsReadTool) Description() string {
	return "read a text file inside the allowed fs root (content capped at 1MiB to protect the model context); returns the file content"
}

func (FsReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "file path inside the allowed fs root (relative to fs.root, or an absolute path within it)",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func (t FsReadTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("fs_read: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("fs_read: empty path")
	}
	content, err := t.t.f.Read(ctx, a.Path, 0)
	if err != nil {
		return "", fmt.Errorf("fs_read: %w", err)
	}
	// fs/read is a log-only fact (D3) carrying the path and the returned byte
	// size; the content itself lives in the tool/result the loop logs.
	t.t.emit(session.EventFsRead, session.NewFsRead(a.Path, len(content)))
	return content, nil
}

// FsWriteTool creates or overwrites a text file inside the allowed fs root
// (missing parent directories are created on demand) and returns the written
// path. A path that escapes the root is rejected before anything is touched.
type FsWriteTool struct {
	t *FsTools
}

func (FsWriteTool) Name() string { return ToolWriteName }

func (FsWriteTool) Description() string {
	return "create or overwrite a text file inside the allowed fs root (missing parent directories are created); returns the written path"
}

func (FsWriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "file path inside the allowed fs root (relative to fs.root, or an absolute path within it)",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "the text content to write (creates or overwrites the file)",
			},
		},
		"required":             []string{"path", "content"},
		"additionalProperties": false,
	}
}

func (t FsWriteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path    string  `json:"path"`
		Content *string `json:"content"` // nil = key absent (rejected); *"" = an explicitly empty file (valid)
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("fs_write: %w", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", fmt.Errorf("fs_write: empty path")
	}
	if a.Content == nil {
		return "", fmt.Errorf("fs_write: missing content")
	}
	if err := t.t.f.Write(ctx, a.Path, *a.Content); err != nil {
		return "", fmt.Errorf("fs_write: %w", err)
	}
	t.t.emit(session.EventFsWrite, session.NewFsWrite(a.Path))
	return fmt.Sprintf("wrote %s (%d bytes)", a.Path, len(*a.Content)), nil
}

// FsListTool lists the direct (non-recursive) children of a directory inside
// the allowed fs root, sorted by name, and returns a formatted table. The
// returned paths are relative to the root so they round-trip into
// fs_read/fs_write/fs_list.
type FsListTool struct {
	t *FsTools
}

func (FsListTool) Name() string { return ToolListName }

func (FsListTool) Description() string {
	return "list the direct children of a directory inside the allowed fs root (non-recursive, sorted by name)"
}

func (FsListTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"dir": map[string]any{
				"type":        "string",
				"description": "directory path inside the allowed fs root (use \".\" for the root itself)",
			},
		},
		"required":             []string{"dir"},
		"additionalProperties": false,
	}
}

func (t FsListTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("fs_list: %w", err)
	}
	if strings.TrimSpace(a.Dir) == "" {
		return "", fmt.Errorf("fs_list: empty dir")
	}
	entries, err := t.t.f.List(ctx, a.Dir)
	if err != nil {
		return "", fmt.Errorf("fs_list: %w", err)
	}
	t.t.emit(session.EventFsList, session.NewFsList(a.Dir, len(entries)))
	return formatEntries(a.Dir, entries), nil
}

// formatEntries renders one listing as model-facing text: a header with the
// directory and entry count followed by one line per entry (name, kind, and
// byte size for files).
func formatEntries(dir string, entries []Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] %d entries\n", dir, len(entries))
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&sb, "%s  dir\n", e.Name)
		} else {
			fmt.Fprintf(&sb, "%s  file  %d bytes\n", e.Name, e.Size)
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
