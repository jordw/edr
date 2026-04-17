package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

const defaultFilesBudget = 2000

// runFiles handles "edr files <pattern>".
// Returns file paths that the trigram index says might contain the pattern.
// Falls back to a full walk + grep if no index exists or pattern < 3 chars.
func runFiles(_ context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("files requires a search pattern argument")
	}
	pattern := args[0]
	edrDir := db.EdrDir()
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultFilesBudget
	}

	// Regex mode: explicit via --regex, or auto-detect BRE alternation \|
	// which users type expecting alternation (silently does nothing in literal mode).
	regexMode := flagBool(flags, "regex", false)
	var warnings []string
	if !regexMode && strings.Contains(pattern, `\|`) {
		regexMode = true
		pattern = strings.ReplaceAll(pattern, `\|`, `|`)
		warnings = append(warnings, `pattern contains \| (BRE alternation); treating as regex with |`)
	}

	var re *regexp.Regexp
	// For prefiltering: each inner []string is a set of literals that should
	// be UNIONed (any can match). Outer slice is treated as OR — we union
	// candidate sets across all groups. Empty means full scan.
	var trigramLiterals []string
	if regexMode {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
		}
		re = compiled
		// Extract literal substrings for trigram prefiltering.
		// For alternations, all branches must be literal-extractable.
		trigramLiterals = extractTrigramLiterals(pattern)
	} else {
		trigramLiterals = []string{pattern}
	}

	// Detect case sensitivity: mixed case → case-sensitive
	caseSensitive := false
	for _, r := range pattern {
		if r >= 'A' && r <= 'Z' {
			caseSensitive = true
			break
		}
	}

	searchBytes := []byte(pattern)
	searchLower := []byte(strings.ToLower(pattern))

	// Unified match function — regex or literal.
	matchFn := func(data []byte) bool {
		if re != nil {
			return re.Match(data)
		}
		return fileMatches(data, searchBytes, searchLower, caseSensitive)
	}

	// Query trigram index + stat-check for changed files.
	var matches []string
	source := "scan"

	// Filter out literals too short for trigrams.
	useTrigram := false
	for _, lit := range trigramLiterals {
		if len(lit) >= 3 {
			useTrigram = true
			break
		}
	}
	h, _ := idx.ReadHeader(edrDir)
	hasIndex := h != nil

	// Stat all indexed files to find modifications, deletions, and new files.
	// ~150ms on 93K-file repos — always correct, no stale dirty tracking.
	var changes *idx.Changes
	if hasIndex {
		changes = idx.StatChanges(root, edrDir)
	}

	if hasIndex && useTrigram {
		// Union candidate sets across all literals (alternation: any can match).
		seen := map[string]bool{}
		var allCandidates []string
		for _, lit := range trigramLiterals {
			if len(lit) < 3 {
				continue
			}
			tris := idx.QueryTrigrams(strings.ToLower(lit))
			if len(tris) == 0 {
				continue
			}
			cands, ok := idx.Query(edrDir, tris)
			if !ok {
				continue
			}
			for _, c := range cands {
				if !seen[c] {
					seen[c] = true
					allCandidates = append(allCandidates, c)
				}
			}
		}

		if len(allCandidates) > 0 {
			// Build set of changed files to skip in trigram results
			// (they get rescanned below with current content).
			changedSet := make(map[string]bool)
			if changes != nil {
				for _, f := range changes.Modified {
					changedSet[f] = true
				}
				for _, f := range changes.Deleted {
					changedSet[f] = true
				}
			}
			for _, rel := range allCandidates {
				if changedSet[rel] {
					continue // rescanned below
				}
				data, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil {
					continue
				}
				if matchFn(data) {
					matches = append(matches, rel)
				}
			}
			source = "index"
			if re != nil {
				source = "index+regex"
			}
		}
	}

	// Scan changed + new files — their trigrams may be stale or absent.
	if changes != nil && !changes.Empty() {
		scanList := append(changes.Modified, changes.New...)
		for _, rel := range scanList {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				continue
			}
			if matchFn(data) {
				matches = append(matches, rel)
			}
		}
		if source == "index" {
			source = "index+stat"
		} else if source == "index+regex" {
			source = "index+regex+stat"
		}
	}

	// Regex mode with no usable trigram literal: walk all indexed files.
	// Trigram prefilter wasn't possible, so we must scan everything.
	if re != nil && !useTrigram && hasIndex {
		indexed := idx.IndexedPaths(edrDir)
		for rel := range indexed {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				continue
			}
			if re.Match(data) {
				matches = append(matches, rel)
			}
		}
		source = "regex_scan"
	} else if !hasIndex {
		// No index at all — must walk.
		index.WalkRepoFiles(root, func(path string) error {
			rel, _ := filepath.Rel(root, path)
			if rel == "" {
				rel = path
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			if matchFn(data) {
				matches = append(matches, rel)
			}
			return nil
		})
	}

	if !hasIndex {
		source = "scan"
	}
	sort.Strings(matches)
	// Deduplicate — changed-file rescan may overlap with trigram results.
	matches = dedupStrings(matches)

	// Apply --glob path filter (e.g. "**/*.go", "cmd/*").
	if glob := flagString(flags, "glob", ""); glob != "" {
		n := 0
		for _, rel := range matches {
			if index.MatchGlob(rel, glob) {
				matches[n] = rel
				n++
			}
		}
		matches = matches[:n]
	}

	// Auto-retry as regex when literal mode found nothing and pattern contains
	// alternation. Catches the common case of users typing foo|bar expecting
	// alternation. If the regex retry also finds nothing, we return the
	// original 0-result response (no warning needed).
	if len(matches) == 0 && !regexMode && containsAlternation(pattern) {
		if _, err := regexp.Compile(pattern); err == nil {
			retryFlags := make(map[string]any, len(flags)+1)
			for k, v := range flags {
				retryFlags[k] = v
			}
			retryFlags["regex"] = true
			retryResult, _ := runFiles(nil, db, root, args, retryFlags)
			if rm, ok := retryResult.(map[string]any); ok {
				if n := anyInt(rm["n"]); n > 0 {
					retryWarn := fmt.Sprintf("treating %q as regex (literal had 0 matches); use --regex to skip this fallback", pattern)
					existing := toStringSlice(rm["warnings"])
					rm["warnings"] = append([]string{retryWarn}, existing...)
					return rm, nil
				}
			}
		}
	}

	result := filesResult(pattern, matches, source, budget)
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	if len(matches) == 0 {
		result["root"] = output.Rel(root)
	}
	return result, nil
}

// containsAlternation returns true if the pattern has an unescaped | character.
func containsAlternation(pattern string) bool {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			i++
			continue
		}
		if pattern[i] == '|' {
			return true
		}
	}
	return false
}

// anyInt is defined in the output package; we need a local copy here.
func anyInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// toStringSlice converts an any-typed slice or string slice to []string.
func toStringSlice(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, s := range x {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// dedupStrings returns a sorted, deduplicated slice.
func dedupStrings(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// extractTrigramLiterals returns literal substrings useful for trigram
// prefiltering a regex. For alternations (foo|bar), returns one literal per
// branch — the caller unions the candidate sets. For simple patterns,
// returns a single literal. Returns nil when no usable literal exists.
func extractTrigramLiterals(pattern string) []string {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	// Top-level alternation: extract one literal from each branch.
	// If any branch yields no literal, fall back to single-literal mode
	// (the regex could match files without any of the literals).
	if re.Op == syntax.OpAlternate {
		var lits []string
		for _, sub := range re.Sub {
			lit := longestLiteral(sub)
			if len(lit) < 3 {
				// One branch has no usable literal — can't prefilter via union.
				return nil
			}
			lits = append(lits, lit)
		}
		return lits
	}
	if lit := longestLiteral(re); len(lit) >= 3 {
		return []string{lit}
	}
	return nil
}

// longestLiteral walks a regex syntax tree and returns the longest literal
// substring guaranteed to appear in any match.
func longestLiteral(re *syntax.Regexp) string {
	switch re.Op {
	case syntax.OpLiteral:
		return string(re.Rune)
	case syntax.OpConcat:
		var best, cur string
		for _, sub := range re.Sub {
			if sub.Op == syntax.OpLiteral {
				cur += string(sub.Rune)
				if len(cur) > len(best) {
					best = cur
				}
			} else {
				cur = ""
				if inner := longestLiteral(sub); len(inner) > len(best) {
					best = inner
				}
			}
		}
		return best
	case syntax.OpCapture:
		if len(re.Sub) == 1 {
			return longestLiteral(re.Sub[0])
		}
	}
	return ""
}

func fileMatches(data, searchBytes, searchLower []byte, caseSensitive bool) bool {
	if caseSensitive {
		return bytes.Contains(data, searchBytes)
	}
	return bytes.Contains(bytes.ToLower(data), searchLower)
}

func filesResult(pattern string, matches []string, source string, budget int) map[string]any {
	shown, budgetUsed, truncated := truncateFiles(matches, budget)
	result := map[string]any{
		"pattern": pattern,
		"files":   shown,
		"n":       len(matches),
		"source":  source,
	}
	if truncated {
		result["truncated"] = true
		result["budget_used"] = budgetUsed
	}
	return result
}

func truncateFiles(matches []string, budget int) ([]string, int, bool) {
	if budget <= 0 || len(matches) == 0 {
		return matches, 0, false
	}

	shown := make([]string, 0, len(matches))
	used := 0
	for _, match := range matches {
		estimate := estimateFileTokens(match)
		if len(shown) > 0 && used+estimate > budget {
			return shown, used, true
		}
		shown = append(shown, match)
		used += estimate
		if used >= budget {
			if len(shown) < len(matches) {
				return shown, used, true
			}
			break
		}
	}
	return shown, used, len(shown) < len(matches)
}

func estimateFileTokens(path string) int {
	estimate := (len(path) + 3) / 4
	if estimate < 1 {
		return 1
	}
	return estimate + 1
}
