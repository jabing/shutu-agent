// tools.go — the D-GAP-1 fs_search consumer tool (docs/dispatch-gap-1.md §3).
// FsSearchTool implements the tools.Tool method set structurally (Go structural
// typing), so this package never imports the tools package — the seam stays
// decoupled (D2). The composition root (cmd/pa) registers it when
// fs_search.enabled, and config.applyDefaults auto-whitelists the name the same
// way the fs_*/web_*/terminal_* tools are.
package fssearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// FsSearchToolName is the file-content-search tool name (whitelisted when
// fs_search.enabled; see config.fsSearchToolNames).
const FsSearchToolName = "fs_search"

// SearchFunc is the injectable search backend (production wires Search; tests
// substitute a fake to assert the output format without touching the disk).
type SearchFunc func(ctx context.Context, query string, opts Options) ([]Hit, error)

// FsSearchTool searches file contents under a directory tree (D-GAP-1). cwd is
// the default search root when the model omits path and the base for
// relative-path display; searchFn defaults to Search and is injectable for
// tests.
type FsSearchTool struct {
	cwd      string
	searchFn SearchFunc
}

// NewFsSearchTool returns the fs_search tool bound to the agent working
// directory (used as the default search root and the display base).
func NewFsSearchTool(cwd string) FsSearchTool {
	return FsSearchTool{cwd: cwd, searchFn: Search}
}

func (FsSearchTool) Name() string { return FsSearchToolName }

func (FsSearchTool) Description() string {
	return "搜索目录下文件内容（子串或正则），返回匹配文件与行"
}

func (FsSearchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "root directory to search; defaults to the agent working directory",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "substring (or regular expression when regex:true) to search for in file contents",
			},
			"pattern": map[string]any{
				"type":        "string",
				"description": "optional glob restricting files by base name, e.g. \"*.go\" (filepath.Match)",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "treat query as a regular expression (default false: plain substring)",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "cap on total hits; <=0 means the default 50",
			},
			"case_sensitive": map[string]any{
				"type":        "boolean",
				"description": "case-sensitive match (default false: case-insensitive)",
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

// Execute runs a bounded file-content search and formats the hits as
// "path:line: text" lines (path shown relative to the agent cwd when possible)
// followed by the match count; a no-hit search reports the query and the root,
// and a cap-hit search (ErrLimit) keeps the partial result with a
// " (limit reached)" suffix. Read-only — never writes.
func (t FsSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Path          string `json:"path"`
		Query         string `json:"query"`
		Pattern       string `json:"pattern"`
		Regex         bool   `json:"regex"`
		MaxResults    int    `json:"max_results"`
		CaseSensitive bool   `json:"case_sensitive"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("fs_search: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("fs_search: empty query")
	}
	root := a.Path
	if strings.TrimSpace(root) == "" {
		root = t.cwd
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("fs_search: no search path (pass path or configure the agent working directory)")
	}
	maxResults := a.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	hits, err := t.searchFn(ctx, a.Query, Options{
		Path:          root,
		FilePattern:   a.Pattern,
		Regex:         a.Regex,
		MaxResults:    maxResults,
		CaseSensitive: a.CaseSensitive,
	})
	if err != nil && !errors.Is(err, ErrLimit) {
		return "", fmt.Errorf("fs_search: %w", err)
	}
	out := formatHits(t, hits, a.Query, root)
	if errors.Is(err, ErrLimit) {
		out += " (limit reached)"
	}
	return out, nil
}

// formatHits renders hits as one "path:line: text" line each (path relative to
// the agent cwd when possible) followed by "N matches"; a no-hit search
// reports the query and the searched root.
func formatHits(t FsSearchTool, hits []Hit, query, root string) string {
	if len(hits) == 0 {
		return fmt.Sprintf("no matches for %q in %s", query, root)
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%s:%d: %s\n", t.displayPath(h.Path), h.Line, h.Text)
	}
	fmt.Fprintf(&sb, "%d matches", len(hits))
	return sb.String()
}

// displayPath renders an absolute hit path relative to the agent cwd when it
// lies under it (more readable for the model); anything else stays absolute.
func (t FsSearchTool) displayPath(p string) string {
	if t.cwd == "" {
		return p
	}
	rel, err := filepath.Rel(t.cwd, p)
	if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return p
}
