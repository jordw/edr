package dispatch

import (
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
// Uses import graph for file importance + span size + name match + test penalty.
func rankCandidates(candidates []index.SymbolInfo, query, root string, _ ...string) []rankedCandidate {
	return heuristicRank(candidates, query, root)
}

// heuristicRank scores candidates using import graph + structural signals.
func heuristicRank(candidates []index.SymbolInfo, query, root string) []rankedCandidate {
	queryLower := strings.ToLower(query)

	// Load import graph (cached) for file importance signal
	edrDir := index.HomeEdrDir(root)
	graph := idx.ReadImportGraph(edrDir)

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

		// 1. Import count — the primary signal.
		// Files imported by many others are canonical/authoritative.
		// For C/C++ source files, inherit the count of their corresponding
		// header (the .c implements the .h, so they share importance).
		inbound := 0
		if graph != nil {
			inbound = graph.Inbound(rel)
			if inbound == 0 {
				inbound = headerImportCount(graph, rel)
			}
		}
		switch {
		case inbound >= 100:
			score += 30 // heavily imported (core header/module)
		case inbound >= 20:
			score += 20
		case inbound >= 5:
			score += 12
		case inbound >= 1:
			score += 5
		}

		// 2. Span size — larger implementations over stubs.
		span := int(s.EndLine - s.StartLine)
		switch {
		case span >= 100:
			score += 12
		case span >= 30:
			score += 8
		case span >= 10:
			score += 4
		case span <= 1:
			score -= 8 // forward declaration or stub
		}

		// 3. Name match quality.
		if tier == tierExact && s.Name == query {
			score += 8 // case-exact
		}
		if tier == tierPartial {
			if strings.HasPrefix(nameLower, queryLower) {
				score += 10
			} else if strings.HasSuffix(nameLower, queryLower) {
				score += 3
			}
		}

		// 4. Test/vendor penalty — these are never canonical.
		if isTestPath(rel) {
			score -= 20
		}
		if isVendorPath(rel) {
			score -= 20
		}

		// 5. Definition type boost.
		if isDefinitionType(s.Type) {
			score += 3
		}

		// 6. Shallow depth tiebreaker — when import counts don't differentiate.
		depth := strings.Count(rel, string(filepath.Separator))
		if depth <= 1 {
			score += 4
		} else if depth <= 2 {
			score += 2
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

// headerImportCount returns the import count of the corresponding header file
// for C/C++ source files. E.g., for "kernel/sched/core.c", checks
// "kernel/sched/core.h", "include/linux/core.h", etc.
// Returns 0 for non-C files or if no matching header is found.
func headerImportCount(graph *idx.ImportGraphData, rel string) int {
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".c", ".cc", ".cpp", ".cxx":
	default:
		return 0
	}
	base := rel[:len(rel)-len(ext)]

	// Try direct header: foo.c → foo.h
	for _, hext := range []string{".h", ".hpp"} {
		if n := graph.Inbound(base + hext); n > 0 {
			return n
		}
	}

	// Try include/ variants: kernel/sched/core.c → include/linux/core.h
	// Just check what the .c file itself includes and pick the most-imported one.
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

func isTestPath(rel string) bool {
	lower := strings.ToLower(rel)
	// Directory-based patterns
	for _, seg := range []string{"test/", "tests/", "testing/", "spec/", "__tests__/"} {
		if strings.Contains(lower, seg) || strings.HasPrefix(lower, seg) {
			return true
		}
	}
	// File-based patterns
	base := filepath.Base(lower)
	return strings.Contains(base, "_test.") ||
		strings.Contains(base, ".test.") ||
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

// isCoreInfraPath returns true for directories that typically hold primary
// definitions and core infrastructure rather than consumers or bindings.
func isCoreInfraPath(rel string) bool {
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	// C/C++ kernel/system patterns
	case "kernel", "core", "init", "mm", "fs", "net", "block", "ipc", "security":
		return true
	// Go/general patterns
	case "internal", "pkg", "cmd":
		return true
	// General source patterns (only at top level)
	case "src", "lib":
		return true
	}
	return false
}

// isToolsPath returns true for tooling/utility directories that are
// not primary source code.
func isToolsPath(rel string) bool {
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "tools", "tool", "util", "utils", "hack", "misc":
		return true
	}
	return false
}

// isPeripheralPath returns true for directories that contain many definitions
// of common names (open, init, probe, config, etc.) but are leaf code rather
// than core infrastructure. Cross-language: applies to any project layout.
func isPeripheralPath(rel string) bool {
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "drivers", "plugins", "extensions", "addons", "contrib":
		return true
	case "adapters", "connectors", "integrations":
		return true
	}
	return false
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

// isScriptsPath returns true for build/dev utility directories that contain
// re-declarations of core types but are not primary source code.
func isScriptsPath(rel string) bool {
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "scripts", "script", "build", "ci", "hack", "deploy":
		return true
	}
	return false
}
