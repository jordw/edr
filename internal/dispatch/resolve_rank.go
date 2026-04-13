package dispatch

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/jordw/edr/internal/idx"
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
func rankCandidates(candidates []index.SymbolInfo, query, root string, _ ...string) []rankedCandidate {
	return heuristicRank(candidates, query, root)
}

// heuristicRank scores candidates using popularity scores (when available)
// or import graph + structural signals as fallback.
func heuristicRank(candidates []index.SymbolInfo, query, root string) []rankedCandidate {
	queryLower := strings.ToLower(query)

	edrDir := index.HomeEdrDir(root)
	graph := idx.ReadImportGraph(edrDir)

	// Load popularity scores (parallel to symbol table, computed at index time).
	var popScores []uint16
	if h, err := idx.ReadHeader(edrDir); err == nil && h.NumSymbols > 0 {
		popScores = idx.ReadPopularity(edrDir, int(h.NumSymbols))
	}

	// Deduplicate: when the same file has multiple symbols with the same name
	// (e.g. a forward declaration and the actual definition), keep the one
	// with the largest span — it is most likely the real definition.
	type dedupEntry struct {
		idx  int
		span uint32
	}
	bestByKey := map[string]dedupEntry{}
	for i, s := range candidates {
		rel, _ := filepath.Rel(root, s.File)
		key := rel + ":" + s.Name
		span := s.EndLine - s.StartLine
		if prev, ok := bestByKey[key]; !ok || span > prev.span {
			bestByKey[key] = dedupEntry{idx: i, span: span}
		}
	}
	dedupAllowed := map[int]bool{}
	for _, e := range bestByKey {
		dedupAllowed[e.idx] = true
	}

	var ranked []rankedCandidate

	for i, s := range candidates {
		if !dedupAllowed[i] {
			continue
		}
		rel, _ := filepath.Rel(root, s.File)

		nameLower := strings.ToLower(s.Name)

		// Assign tier
		var tier rankTier
		if strings.EqualFold(s.Name, query) {
			tier = tierExact
		} else if strings.Contains(nameLower, queryLower) {
			tier = tierPartial
		} else {
			continue
		}

		var score int

		if popScores != nil && s.IndexID > 0 && int(s.IndexID) < len(popScores) {
			// --- Primary path: popularity score ---
			score = int(popScores[s.IndexID])
		} else {
			// --- Fallback: log-scaled import count ---
			inbound := 0
			if graph != nil {
				inbound = graph.Inbound(rel)
				if inbound == 0 {
					inbound = headerImportCount(graph, rel)
				}
			}
			if inbound > 0 {
				score = int(8 * math.Log2(1+float64(inbound)))
			}
		}

		// Definition body boost: a multi-line struct/class/interface is much
		// more likely to be THE definition than a 1-line variable declaration
		// that the regex parser also tags as "struct". Scale by log of span.
		if isDefinitionType(s.Type) {
			span := int(s.EndLine - s.StartLine)
			if span >= 3 {
				// log2(827) ≈ 10 → bonus ≈ 50; log2(3) ≈ 1.6 → bonus ≈ 8
				score += int(5 * math.Log2(float64(span)))
			}
			// Header-file boost: in C/C++ projects, canonical type definitions
			// live in headers. A struct in a .h file is almost certainly THE
			// definition, not a usage.
			if isHeaderFile(rel) {
				score += 20
			}
		}

		// Function/method body boost: a multi-line definition is more useful
		// than a 1-line forward declaration or extern prototype.
		// Kept smaller than definition type boost to preserve struct > function ranking.
		if s.Type == "function" || s.Type == "method" {
			span := int(s.EndLine - s.StartLine)
			if span >= 3 {
				// Moderate boost: log2(49) ≈ 5.6 → bonus ≈ 11
				score += int(2 * math.Log2(float64(span)))
			}
		}

		// Penalty for vendor, test, and mock paths — these are rarely
		// the canonical definition an agent is looking for.
		// Use proportional penalty (halve) + fixed floor to handle both
		// high-popularity and low-popularity peripheral symbols.
		// Mock files get a stronger penalty since their popularity comes
		// from test imports, not from being canonical definitions.
		if peri := peripheralLevel(rel); peri == 2 {
			score = score/3 - 15
		} else if peri == 1 {
			score = score/2 - 10
		}

		// Name match quality
		if tier == tierExact && s.Name == query {
			score += 3 // case-exact match
		}

		// Shape synergy: PascalCase query + definition type
		if inferShape(query) == shapeType && isDefinitionType(s.Type) {
			score += 3
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
	if len(ranked) == 1 {
		return true
	}
	if ranked[1].Tier > tierExact {
		return true // only tier 1 result
	}
	// Require a meaningful score gap for auto-resolve.
	minGap := 20
	if len(query) <= 3 {
		minGap = 40
	} else if len(query) <= 6 {
		minGap = 30
	}
	gap := top.Score - ranked[1].Score
	// For high popularity scores, also accept a 2x ratio
	if top.Score > 50 && gap > 0 && top.Score >= ranked[1].Score*2 {
		return true
	}
	return gap >= minGap
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

// headerImportCount returns the import count of the corresponding header file
// for C/C++ source files.
func headerImportCount(graph *idx.ImportGraphData, rel string) int {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".c", ".cc", ".cpp", ".cxx":
	default:
		return 0
	}
	base := rel[:len(rel)-len(ext)]

	for _, hext := range []string{".h", ".hpp"} {
		if n := graph.Inbound(base + hext); n > 0 {
			return n
		}
	}

	best := 0
	for _, imported := range graph.Imports(rel) {
		if n := graph.Inbound(imported); n > best {
			best = n
		}
	}
	return best
}

func isHeaderFile(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".h", ".hpp", ".hxx", ".hh":
		return true
	}
	return false
}

func isDefinitionType(t string) bool {
	switch t {
	case "struct", "class", "interface", "type", "enum", "impl":
		return true
	}
	return false
}

// peripheralLevel returns how peripheral a path is:
//
//	0 = normal (no penalty)
//	1 = peripheral (vendor, test files, generated, examples)
//	2 = highly peripheral (mock files, fake implementations)
func peripheralLevel(rel string) int {
	lower := strings.ToLower(rel)

	// Level 2: mocks and fakes — these are stub implementations whose
	// high popularity comes from test imports, not real usage.
	if strings.Contains(lower, "/mocks/") || strings.Contains(lower, "/mock_") ||
		strings.Contains(lower, "_mock.") || strings.Contains(lower, "mock.go") ||
		strings.Contains(lower, "/fake_") || strings.Contains(lower, "fake.go") {
		return 2
	}

	// Level 1: vendor, test files, generated code, examples
	if strings.HasPrefix(lower, "vendor/") || strings.Contains(lower, "/vendor/") {
		return 1
	}
	if strings.Contains(lower, "/testdata/") || strings.Contains(lower, "/testing/") {
		return 1
	}
	if strings.HasSuffix(lower, "_test.go") || strings.HasSuffix(lower, "_test.ts") ||
		strings.HasSuffix(lower, "_test.js") || strings.HasSuffix(lower, "_test.py") ||
		strings.HasSuffix(lower, ".test.ts") || strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".spec.ts") || strings.HasSuffix(lower, ".spec.js") {
		return 1
	}
	if strings.Contains(lower, "/generated/") || strings.Contains(lower, "/zz_generated") {
		return 1
	}
	if strings.HasPrefix(lower, "examples/") || strings.HasPrefix(lower, "samples/") ||
		strings.Contains(lower, "/examples/") || strings.Contains(lower, "/samples/") {
		return 1
	}
	return 0
}
