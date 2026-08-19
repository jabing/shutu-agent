// Shared search helpers — Go ports of dsh-knowledge's toFtsQuery,
// fallbackTerms and rankToScore (src/local-provider.ts). They are shared by
// every KB provider so the seam's retrieval behavior is backend-independent.
package kb

import (
	"regexp"
	"sort"
	"strings"
)

var (
	englishWordRe = regexp.MustCompile(`[a-z0-9][a-z0-9_.-]{1,}`) // lowercased input
	hanRunRe      = regexp.MustCompile(`\p{Han}+`)                // contiguous CJK run
)

// toFtsQuery builds an FTS5 MATCH expression: whitespace-split terms, each
// double-quoted (embedded quotes doubled) and OR-joined, capped at 20 terms
// (dsh-knowledge toFtsQuery).
func toFtsQuery(text string) string {
	fields := strings.Fields(text)
	if len(fields) > 20 {
		fields = fields[:20]
	}
	quoted := make([]string, 0, len(fields))
	for _, f := range fields {
		quoted = append(quoted, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// fallbackTerms extracts the terms used by the Chinese bigram LIKE fallback:
// English words are taken verbatim (lowercased), and every contiguous Han run
// is cut into its adjacent bigrams (a single-character run is kept whole).
// The set is capped at 20 terms (dsh-knowledge fallbackTerms).
func fallbackTerms(text string) []string {
	seen := make(map[string]bool)
	lower := strings.ToLower(text)
	for _, w := range englishWordRe.FindAllString(lower, -1) {
		seen[w] = true
	}
	for _, run := range hanRunRe.FindAllString(text, -1) {
		chars := []rune(run)
		if len(chars) == 1 {
			seen[string(chars[0])] = true
		}
		for i := 0; i+1 < len(chars); i++ {
			seen[string(chars[i])+string(chars[i+1])] = true
		}
	}
	terms := make([]string, 0, len(seen))
	for t := range seen {
		terms = append(terms, t)
	}
	// Deterministic order (Go maps iterate randomly); ordering only affects the
	// OR clause, never the result set.
	sort.Strings(terms)
	if len(terms) > 20 {
		terms = terms[:20]
	}
	return terms
}

// rankToScore maps a BM25 rank to a hit score in (0,1], 1/(1+max(0,rank))
// (dsh-knowledge rankToScore).
func rankToScore(rank float64) float64 {
	if rank < 0 {
		rank = 0
	}
	return 1 / (1 + rank)
}

// escapeLike escapes LIKE wildcards so a term matches literally: backslash,
// percent and underscore are doubled/prefixed (dsh-knowledge searchByTerms).
func escapeLike(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, `%`, `\%`)
	term = strings.ReplaceAll(term, `_`, `\_`)
	return term
}

// normalizeTopK clamps a requested result count to [1, MaxTopK], defaulting to
// DefaultTopK when absent.
func normalizeTopK(topK int) int {
	if topK <= 0 {
		return DefaultTopK
	}
	if topK > MaxTopK {
		return MaxTopK
	}
	return topK
}
