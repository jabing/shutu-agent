// Package fssearch searches file contents under a directory tree
// (D-GAP-1). It is a read-only, bounded capability: ignored VCS/dependency
// directories and binary files are skipped, per-file and aggregate limits
// bound the scan, and the default match is a plain substring
// (regex:true switches to a regular expression). It never writes.
package fssearch

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Hit is one matching line.
type Hit struct {
	Path string // absolute path
	Line int    // 1-based line number
	Text string // matching line (trailing newline trimmed)
}

// Options bounds a Search. Zero values fall back to the defaults.
type Options struct {
	Path          string // root directory; "" → the caller-supplied default (组合根注入 cwd)
	FilePattern   string // optional glob restricting files, e.g. "*.go" (filepath.Match on base name)
	Regex         bool   // treat Query as a regular expression
	MaxResults    int    // cap total hits; <=0 → DefaultMaxResults
	MaxFileBytes  int64  // skip files larger than this; <=0 → DefaultMaxFileBytes
	MaxFiles      int    // cap files scanned; <=0 → DefaultMaxFiles
	CaseSensitive bool   // default false (case-insensitive match)
}

// Defaults (D-GAP-1 有界与安全).
const (
	DefaultMaxResults   = 50
	DefaultMaxFileBytes = 1 << 20 // 1 MiB
	DefaultMaxFiles     = 20000
)

// ErrLimit is returned by Search when a scan cap is reached (MaxFiles or
// MaxResults); the returned hits are the partial results collected so far.
var ErrLimit = errors.New("fssearch: search limit reached")

// ignoredDirs are the VCS/dependency directory names whose subtrees are
// skipped entirely (D-GAP-1 有界与安全).
var ignoredDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true,
}

// Search finds Query in file contents under opts.Path and returns hits in
// file-then-line order. ErrLimit is returned when MaxFiles/MaxResults caps are
// hit (the caller may still use the partial hits). Query must be non-empty.
func Search(ctx context.Context, query string, opts Options) ([]Hit, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("fssearch: empty query")
	}
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("fssearch: empty path")
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	maxFileBytes := opts.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = DefaultMaxFileBytes
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}

	// Walk from an absolute, cleaned root so every Hit.Path is absolute and
	// the containment of the scan is stable.
	root, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("fssearch: resolve %s: %w", opts.Path, err)
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("fssearch: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fssearch: %s is not a directory", root)
	}

	var re *regexp.Regexp
	if opts.Regex {
		re, err = regexp.Compile(query)
		if err != nil {
			return nil, fmt.Errorf("fssearch: compile regex %q: %w", query, err)
		}
	}

	var (
		hits         []Hit
		filesScanned int
	)
	// walk visits every entry. The callback doubles as the limit/cancel signal:
	// returning ErrLimit or ctx.Err() stops the walk (filepath.WalkDir
	// propagates the callback's error), which lets Search hand the partial hits
	// back together with the reason.
	walk := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable entry (permission, vanished mid-walk) is skipped,
			// never a hard failure or a panic (dispatch-m6f-3 §4 原则).
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if path != root && ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if opts.FilePattern != "" {
			matched, mErr := filepath.Match(opts.FilePattern, d.Name())
			if mErr != nil {
				return nil // a malformed pattern matches nothing
			}
			if !matched {
				return nil
			}
		}
		if filesScanned >= maxFiles {
			return ErrLimit
		}
		filesScanned++
		fileHits, fErr := scanFile(ctx, path, d, query, opts, re, maxFileBytes, maxResults-len(hits))
		if fErr != nil {
			return fErr
		}
		hits = append(hits, fileHits...)
		if len(hits) >= maxResults {
			return ErrLimit
		}
		return nil
	}
	err = filepath.WalkDir(root, walk)
	if err != nil && !errors.Is(err, ErrLimit) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return hits, ctxErr
		}
		return hits, err
	}
	if errors.Is(err, ErrLimit) {
		return hits, ErrLimit
	}
	return hits, nil
}

// scanFile searches one file for matching lines. Unreadable or binary or
// oversized files are skipped (returning nil, nil); a context cancellation is
// the only error that aborts the whole search.
func scanFile(ctx context.Context, path string, d fs.DirEntry, query string, opts Options, re *regexp.Regexp, maxFileBytes int64, remaining int) ([]Hit, error) {
	info, err := d.Info()
	if err != nil {
		return nil, nil // an unreadable file is skipped (bounded, never a panic)
	}
	if info.Size() > maxFileBytes {
		return nil, nil // oversized files are skipped (DefaultMaxFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil // unreadable files are skipped
	}
	defer f.Close()

	// Binary detection (D-GAP-1): a NUL byte in the first 8 KiB marks the file
	// as binary and it is skipped without reading further.
	head := make([]byte, 8192)
	n, readErr := f.Read(head)
	if readErr != nil && readErr != io.EOF {
		return nil, nil
	}
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, nil
	}

	var hits []Hit
	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return hits, ctxErr
		}
		line++
		text := strings.TrimRight(scanner.Text(), "\r\n")
		if matchLine(query, text, opts, re) {
			hits = append(hits, Hit{Path: path, Line: line, Text: text})
			if len(hits) >= remaining {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// A mid-file read error (e.g. an over-long line) keeps the partial
		// hits and does not abort the whole search.
		return hits, nil
	}
	return hits, nil
}

// matchLine reports whether one line matches the query: an exact (as authored)
// regexp MatchString when regex mode is on, otherwise a substring match that is
// case-insensitive by default and honors CaseSensitive.
func matchLine(query, text string, opts Options, re *regexp.Regexp) bool {
	if re != nil {
		return re.MatchString(text)
	}
	if opts.CaseSensitive {
		return strings.Contains(text, query)
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(query))
}
