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

		// Tiebreakers (small signals that only matter when popularity is equal)

		// Name match quality
		if tier == tierExact && s.Name == query {
			score += 3 // case-exact match
		}

		// Definition type + shape synergy
		if isDefinitionType(s.Type) {
			score += 3
		}
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

func isDefinitionType(t string) bool {
	switch t {
	case "struct", "class", "interface", "type", "enum", "impl":
		return true
	}
	return false
}
