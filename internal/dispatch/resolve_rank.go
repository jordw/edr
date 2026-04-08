package dispatch

import (
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// rankTier determines hard ranking boundaries — tier 1 always beats tier 2.
type rankTier int

const (
	tierExact   rankTier = 1 // symbol name matches query exactly (case-insensitive)
	tierPartial rankTier = 2 // symbol name contains query as substring
)

type rankedCandidate struct {
	Symbol index.SymbolInfo
	Tier   rankTier
	Score  int
	Rel    string // relative path
}

// rankCandidates scores and sorts symbol candidates for smart focus resolution.
// Returns candidates sorted by tier (hard boundary), then score descending.
func rankCandidates(candidates []index.SymbolInfo, query, root string) []rankedCandidate {
	queryLower := strings.ToLower(query)
	queryShape := inferShape(query)

	var ranked []rankedCandidate
	seen := map[string]bool{}

	for _, s := range candidates {
		rel, _ := filepath.Rel(root, s.File)
		key := rel + ":" + s.Name
		if seen[key] {
			continue
		}
		seen[key] = true

		nameLower := strings.ToLower(s.Name)

		// Assign tier
		var tier rankTier
		if strings.EqualFold(s.Name, query) {
			tier = tierExact
		} else if strings.Contains(nameLower, queryLower) {
			tier = tierPartial
		} else {
			continue // shouldn't happen but skip
		}

		var score int

		// Exactness within tier
		if tier == tierExact && s.Name == query {
			score += 10 // case-exact
		}
		if tier == tierPartial {
			if strings.HasPrefix(nameLower, queryLower) {
				score += 15
			} else if strings.HasSuffix(nameLower, queryLower) {
				score += 5
			}
		}

		// Type hint from query shape (small tiebreaker)
		score += shapeBoost(s.Type, queryShape)

		// Definition types get small boost
		if isDefinitionType(s.Type) {
			score += 5
		}

		// Span size: full definitions (many lines) beat forward declarations (1-2 lines)
		span := int(s.EndLine - s.StartLine)
		if span > 10 {
			score += 10 // substantial body — likely the real definition
		} else if span <= 1 {
			score -= 10 // forward declaration or single-line stub
		}

		// Canonical path boost: include/ dirs and shallow paths are more likely
		// to hold the "real" definition for widely-used types
		if strings.HasPrefix(rel, "include/") {
			score += 8
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if depth <= 2 {
			score += 3 // shallow paths are more canonical
		}

		// Penalties
		if isTestPath(rel) {
			score -= 20
		}
		if isDocPath(rel) {
			score -= 25
		}
		if isVendorPath(rel) {
			score -= 20
		}
		if isSamplePath(rel) {
			score -= 15
		}
		if strings.HasPrefix(rel, "arch/") && !strings.HasPrefix(rel, "arch/x86/") {
			score -= 5 // non-x86 arch-specific code is rarely the target
		}

		ranked = append(ranked, rankedCandidate{
			Symbol: s,
			Tier:   tier,
			Score:  score,
			Rel:    rel,
		})
	}

	// Sort: tier first (hard boundary), then score descending, then path for stability
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Tier != ranked[j].Tier {
			return ranked[i].Tier < ranked[j].Tier
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Rel < ranked[j].Rel
	})

	return ranked
}

// shouldAutoResolve returns true if the top candidate is confident enough
// to resolve automatically instead of showing a shortlist.
func shouldAutoResolve(ranked []rankedCandidate, query string) bool {
	if len(ranked) == 0 {
		return false
	}
	top := ranked[0]
	if top.Tier != tierExact {
		return false
	}
	// Short/common name rule: require higher confidence
	minGap := 20
	if len(query) <= 3 {
		minGap = 40
	}
	if len(ranked) == 1 {
		return true
	}
	if ranked[1].Tier > tierExact {
		return true // only tier 1 result
	}
	return top.Score-ranked[1].Score >= minGap
}

// buildShortlist constructs a structured result for ambiguous resolution.
func buildShortlist(ranked []rankedCandidate, query, root string) map[string]any {
	limit := 10
	if len(ranked) < limit {
		limit = len(ranked)
	}
	var items []any
	for _, c := range ranked[:limit] {
		items = append(items, map[string]any{
			"name":  c.Symbol.Name,
			"type":  c.Symbol.Type,
			"file":  c.Rel,
			"line":  int(c.Symbol.StartLine),
			"tier":  int(c.Tier),
			"score": c.Score,
		})
	}
	return map[string]any{
		"resolve":    "ambiguous",
		"query":      query,
		"candidates": items,
		"method":     "heuristic_ranking",
		"hint":       "use file:symbol syntax to pick one, e.g. edr focus " + ranked[0].Rel + ":" + ranked[0].Symbol.Name,
		"root":       output.Rel(root),
	}
}

// --- Helpers ---

type nameShape int

const (
	shapeUnknown  nameShape = iota
	shapeConst              // ALL_CAPS
	shapeType               // CamelCase
	shapeFunction           // snake_case or verb-like
)

func inferShape(query string) nameShape {
	if len(query) == 0 {
		return shapeUnknown
	}
	allUpper := true
	hasUnderscore := false
	startsUpper := unicode.IsUpper(rune(query[0]))
	hasLower := false
	for _, r := range query {
		if unicode.IsLower(r) {
			allUpper = false
			hasLower = true
		}
		if r == '_' {
			hasUnderscore = true
		}
	}
	if allUpper && len(query) > 1 {
		return shapeConst
	}
	if startsUpper && hasLower && !hasUnderscore {
		return shapeType
	}
	if hasUnderscore || (!startsUpper && hasLower) {
		return shapeFunction
	}
	return shapeUnknown
}

func shapeBoost(symbolType string, shape nameShape) int {
	switch shape {
	case shapeConst:
		if symbolType == "constant" || symbolType == "variable" || symbolType == "type" {
			return 5
		}
	case shapeType:
		if symbolType == "struct" || symbolType == "class" || symbolType == "interface" || symbolType == "type" {
			return 5
		}
	case shapeFunction:
		if symbolType == "function" || symbolType == "method" {
			return 5
		}
	}
	return 0
}

func isDefinitionType(t string) bool {
	switch t {
	case "struct", "class", "interface", "type", "enum", "impl":
		return true
	}
	return false
}

func isTestPath(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, "_test.c") ||
		strings.HasSuffix(base, "_test.rs") ||
		strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, "_test.py") ||
		strings.Contains(rel, "/test/") ||
		strings.Contains(rel, "/tests/") ||
		strings.HasPrefix(base, "test_")
}

func isDocPath(rel string) bool {
	ext := filepath.Ext(rel)
	if ext == ".md" || ext == ".rst" || ext == ".txt" {
		return true
	}
	lower := strings.ToLower(rel)
	return strings.HasPrefix(lower, "doc") || strings.Contains(lower, "/doc/")
}

func isVendorPath(rel string) bool {
	return strings.HasPrefix(rel, "vendor/") ||
		strings.HasPrefix(rel, "node_modules/") ||
		strings.HasPrefix(rel, "third_party/")
}

func isSamplePath(rel string) bool {
	return strings.HasPrefix(rel, "samples/") ||
		strings.HasPrefix(rel, "examples/") ||
		strings.HasPrefix(rel, "tools/testing/") ||
		strings.HasPrefix(rel, "tools/selftests/") ||
		strings.Contains(rel, "/testdata/") ||
		strings.Contains(rel, "/example/") ||
		strings.Contains(rel, "/bench/")
}
