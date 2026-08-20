// search_test.go — the D-GAP-1 search-engine tests (docs/dispatch-gap-1.md
// §2). All ten cases build a throwaway tree with t.TempDir() and exercise
// Search directly: substring hits and ordering, case sensitivity, regex, the
// ignored-directory and binary skips, the MaxResults/MaxFiles caps (ErrLimit +
// partial hits), FilePattern filtering, the MaxFileBytes skip, the error
// paths (missing path / empty query) and context cancellation.
package fssearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file (and any missing parents) with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestSearchSubstringHits covers the happy path (dispatch-gap-1 §2 #1): a
// query matches across multiple files and lines, every Hit carries the
// absolute path, the 1-based line number and the trimmed line, and the result
// is ordered file-then-line (files in lexical walk order, lines ascending).
func TestSearchSubstringHits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hello world\nneedle one\ntail\nneedle two\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "no match\nneedle b\n")

	hits, err := Search(context.Background(), "needle", Options{Path: dir})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := []struct {
		base string
		line int
		text string
	}{
		{"a.txt", 2, "needle one"},
		{"a.txt", 4, "needle two"},
		{"b.txt", 2, "needle b"},
	}
	if len(hits) != len(want) {
		t.Fatalf("hits = %d, want %d (%v)", len(hits), len(want), hits)
	}
	for i, w := range want {
		if filepath.Base(hits[i].Path) != w.base || hits[i].Line != w.line || hits[i].Text != w.text {
			t.Errorf("hit[%d] = %+v, want %+v", i, hits[i], w)
		}
		if !filepath.IsAbs(hits[i].Path) {
			t.Errorf("hit[%d].Path = %q, want absolute", i, hits[i].Path)
		}
	}
}

// TestSearchCaseSensitivity covers #2: the default match is case-insensitive,
// while CaseSensitive distinguishes case.
func TestSearchCaseSensitivity(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f.txt"), "Needle upper\nneedle lower\nneedle Exact\n")

	// Default: case-insensitive, so an uppercase query hits every line.
	hits, err := Search(context.Background(), "NEEDLE", Options{Path: dir})
	if err != nil {
		t.Fatalf("Search (default): %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("case-insensitive hits = %d, want 3", len(hits))
	}

	// CaseSensitive: the uppercase query no longer matches the lowercase lines.
	hits, err = Search(context.Background(), "NEEDLE", Options{Path: dir, CaseSensitive: true})
	if err != nil {
		t.Fatalf("Search (sensitive): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("case-sensitive hits = %d, want 0", len(hits))
	}

	// CaseSensitive: an exact-case query matches only its own line.
	hits, err = Search(context.Background(), "Needle", Options{Path: dir, CaseSensitive: true})
	if err != nil {
		t.Fatalf("Search (sensitive exact): %v", err)
	}
	if len(hits) != 1 || hits[0].Line != 1 {
		t.Fatalf("case-sensitive exact hits = %+v, want line 1 only", hits)
	}
}

// TestSearchRegex covers #3: regex:true matches with a compiled regular
// expression, and a malformed pattern fails closed with an error.
func TestSearchRegex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f.txt"), "apple pie\nbanana split\n")

	hits, err := Search(context.Background(), `spl.+`, Options{Path: dir, Regex: true})
	if err != nil {
		t.Fatalf("Search (regex): %v", err)
	}
	if len(hits) != 1 || hits[0].Line != 2 || hits[0].Text != "banana split" {
		t.Fatalf("regex hits = %+v, want line 2 \"banana split\"", hits)
	}

	if _, err := Search(context.Background(), `(`, Options{Path: dir, Regex: true}); err == nil {
		t.Fatal("an invalid regex must error, not silently match nothing")
	} else if !strings.Contains(err.Error(), "compile regex") {
		t.Errorf("regex error = %v, want it to mention the regex compilation", err)
	}
}

// TestSearchSkipsIgnoredDirs covers #4: the .git and node_modules subtrees are
// skipped while sibling and nested non-ignored files are still searched.
func TestSearchSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "config"), "needle hidden\n")
	writeFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "needle hidden\n")
	writeFile(t, filepath.Join(dir, "keep.txt"), "needle visible\n")
	writeFile(t, filepath.Join(dir, "sub", "ok.go"), "needle nested\n")

	hits, err := Search(context.Background(), "needle", Options{Path: dir})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %v, want only keep.txt and sub/ok.go (ignored dirs skipped)", hits)
	}
	bases := map[string]bool{}
	for _, h := range hits {
		bases[filepath.Base(h.Path)] = true
	}
	if !bases["keep.txt"] || !bases["ok.go"] {
		t.Fatalf("hits = %v, want keep.txt and sub/ok.go", hits)
	}
}

// TestSearchSkipsBinary covers #5: a file containing a NUL byte in its first
// 8 KiB is skipped without producing a hit and without an error.
func TestSearchSkipsBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "binary.bin"), []byte("needle\x00 rest of binary"), 0o644); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	writeFile(t, filepath.Join(dir, "text.txt"), "needle here\n")

	hits, err := Search(context.Background(), "needle", Options{Path: dir})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || filepath.Base(hits[0].Path) != "text.txt" {
		t.Fatalf("hits = %v, want only text.txt (binary file skipped, no error)", hits)
	}
}

// TestSearchLimits covers #6: MaxResults and MaxFiles each stop the scan and
// return ErrLimit together with the partial hits.
func TestSearchLimits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "many.txt"), "needle\nneedle\nneedle\nneedle\nneedle\n")

	// MaxResults: stop after 3 hits and return ErrLimit with the partial set.
	hits, err := Search(context.Background(), "needle", Options{Path: dir, MaxResults: 3})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("Search (MaxResults) error = %v, want ErrLimit", err)
	}
	if len(hits) != 3 {
		t.Fatalf("MaxResults partial hits = %d, want 3", len(hits))
	}
	for i, h := range hits {
		if h.Line != i+1 {
			t.Errorf("partial hit[%d].Line = %d, want %d", i, h.Line, i+1)
		}
	}

	// MaxFiles: scan only the first file (lexical order) and stop with ErrLimit.
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, "a.txt"), "needle in a\n")
	writeFile(t, filepath.Join(dir2, "b.txt"), "needle in b\n")
	hits, err = Search(context.Background(), "needle", Options{Path: dir2, MaxFiles: 1})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("Search (MaxFiles) error = %v, want ErrLimit", err)
	}
	if len(hits) != 1 || filepath.Base(hits[0].Path) != "a.txt" {
		t.Fatalf("MaxFiles partial hits = %v, want only a.txt", hits)
	}
}

// TestSearchFilePattern covers #7: FilePattern restricts scanning to files
// whose base name matches the glob (filepath.Match).
func TestSearchFilePattern(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "needle go\n")
	writeFile(t, filepath.Join(dir, "a.txt"), "needle txt\n")
	writeFile(t, filepath.Join(dir, "sub", "b.go"), "needle nested go\n")

	hits, err := Search(context.Background(), "needle", Options{Path: dir, FilePattern: "*.go"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %v, want only the .go files", hits)
	}
	for _, h := range hits {
		if filepath.Ext(h.Path) != ".go" {
			t.Errorf("hit %+v filtered in despite pattern *.go", h)
		}
	}
}

// TestSearchMaxFileBytes covers #8: files larger than MaxFileBytes are skipped
// while smaller files are still searched.
func TestSearchMaxFileBytes(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 2000) + "needle tail\n"
	writeFile(t, filepath.Join(dir, "big.txt"), big)
	writeFile(t, filepath.Join(dir, "small.txt"), "needle small\n")

	hits, err := Search(context.Background(), "needle", Options{Path: dir, MaxFileBytes: 100})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || filepath.Base(hits[0].Path) != "small.txt" {
		t.Fatalf("hits = %v, want only small.txt (oversized file skipped)", hits)
	}
}

// TestSearchErrors covers #9: a missing path and an empty query both error.
func TestSearchErrors(t *testing.T) {
	if _, err := Search(context.Background(), "needle", Options{Path: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("a nonexistent path must error")
	}
	if _, err := Search(context.Background(), "", Options{Path: t.TempDir()}); err == nil {
		t.Fatal("an empty query must error")
	}
	if _, err := Search(context.Background(), "   ", Options{Path: t.TempDir()}); err == nil {
		t.Fatal("a blank query must error")
	}
}

// TestSearchContextCancel covers #10: a cancelled context aborts the search
// and returns ctx.Err().
func TestSearchContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f.txt"), "needle here\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: the search must bail out immediately
	if _, err := Search(ctx, "needle", Options{Path: dir}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search error = %v, want context.Canceled", err)
	}
}
