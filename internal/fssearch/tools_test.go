// tools_test.go — the D-GAP-1 fs_search tool tests (docs/dispatch-gap-1.md
// §3). A fake searchFn substitutes the engine so the Execute output format is
// asserted without touching the disk: hit lines ("path:line: text" relative to
// cwd), the match count, the no-match report, the ErrLimit "(limit reached)"
// suffix, and the empty-query rejection.
package fssearch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSearcher records the query and Options it was handed and returns canned
// hits/error, so Execute's argument mapping and formatting are observable.
type fakeSearcher struct {
	fn    SearchFunc
	query string
	opts  Options
}

func (f *fakeSearcher) call(ctx context.Context, query string, opts Options) ([]Hit, error) {
	f.query = query
	f.opts = opts
	return f.fn(ctx, query, opts)
}

// newFakeTool returns an FsSearchTool whose searchFn records calls and returns
// the given fn result.
func newFakeTool(cwd string, fn SearchFunc) (*FsSearchTool, *fakeSearcher) {
	f := &fakeSearcher{fn: fn}
	return &FsSearchTool{cwd: cwd, searchFn: f.call}, f
}

// TestFsSearchExecuteFormatsHits asserts the hit output shape: each hit as
// "path:line: text" (relative to cwd), the trailing "N matches" line, and the
// defaulted root/max_results mapping.
func TestFsSearchExecuteFormatsHits(t *testing.T) {
	tool, f := newFakeTool(`C:\work`, func(ctx context.Context, query string, opts Options) ([]Hit, error) {
		return []Hit{
			{Path: `C:\work\a.txt`, Line: 2, Text: "needle one"},
			{Path: `C:\work\sub\b.go`, Line: 5, Text: "needle two"},
		}, nil
	})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"needle"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// The display is relative to cwd using the platform separator.
	relB := filepath.Join("sub", "b.go")
	want := "a.txt:2: needle one\n" + relB + ":5: needle two\n2 matches"
	if out != want {
		t.Fatalf("Execute output = %q, want %q", out, want)
	}
	// No path argument → the search ran against the tool's cwd with the
	// default result cap.
	if f.query != "needle" {
		t.Errorf("query = %q, want needle", f.query)
	}
	if f.opts.Path != `C:\work` {
		t.Errorf("opts.Path = %q, want the tool cwd", f.opts.Path)
	}
	if f.opts.MaxResults != DefaultMaxResults {
		t.Errorf("opts.MaxResults = %d, want %d (defaulted)", f.opts.MaxResults, DefaultMaxResults)
	}
}

// TestFsSearchExecuteHonorsExplicitPathAndMaxResults verifies the explicit
// path and max_results arguments are forwarded to the search.
func TestFsSearchExecuteHonorsExplicitPathAndMaxResults(t *testing.T) {
	tool, f := newFakeTool(`C:\work`, func(ctx context.Context, query string, opts Options) ([]Hit, error) {
		return nil, nil
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"C:\\tree","query":"x","max_results":7}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.opts.Path != `C:\tree` || f.opts.MaxResults != 7 {
		t.Fatalf("opts = %+v, want path C:\\tree / max_results 7", f.opts)
	}
}

// TestFsSearchExecuteNoMatches asserts the no-match report format.
func TestFsSearchExecuteNoMatches(t *testing.T) {
	tool, _ := newFakeTool(`C:\work`, func(ctx context.Context, query string, opts Options) ([]Hit, error) {
		return nil, nil
	})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"needle","path":"C:\\tree"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != `no matches for "needle" in C:\tree` {
		t.Fatalf("Execute output = %q, want the no-matches report", out)
	}
}

// TestFsSearchExecuteLimitReached asserts the ErrLimit suffix on a partial
// result.
func TestFsSearchExecuteLimitReached(t *testing.T) {
	tool, _ := newFakeTool(`C:\work`, func(ctx context.Context, query string, opts Options) ([]Hit, error) {
		return []Hit{{Path: `C:\work\a.txt`, Line: 1, Text: "needle"}}, ErrLimit
	})
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"needle"}`))
	if err != nil {
		t.Fatalf("Execute must not surface ErrLimit as an error: %v", err)
	}
	if !strings.HasSuffix(out, "1 matches (limit reached)") {
		t.Fatalf("Execute output = %q, want the (limit reached) suffix", out)
	}
}

// TestFsSearchExecuteRejectsEmptyQuery asserts an empty/blank query is
// rejected before the search runs (the registry D7 gate also rejects a missing
// query; the tool checks again so a direct call can never bypass it).
func TestFsSearchExecuteRejectsEmptyQuery(t *testing.T) {
	ran := false
	tool, _ := newFakeTool(`C:\work`, func(ctx context.Context, query string, opts Options) ([]Hit, error) {
		ran = true
		return nil, nil
	})
	for _, args := range []string{`{}`, `{"query":""}`, `{"query":"   "}`} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("Execute with args %s must error", args)
		}
	}
	if ran {
		t.Fatal("the search must not run for an empty query")
	}
}

// TestFsSearchExecuteRejectsEmptyPath asserts a call with no path and no tool
// cwd fails closed.
func TestFsSearchExecuteRejectsEmptyPath(t *testing.T) {
	tool, _ := newFakeTool("", func(ctx context.Context, query string, opts Options) ([]Hit, error) {
		return nil, nil
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"needle"}`)); err == nil {
		t.Fatal("Execute with no path and no cwd must error")
	}
}
