package ranking

import (
	"math"
	"path/filepath"
	"strings"
	"unicode"
)

// Feature indices into the flat feature vector.
// Design principle: no hard-coded directory names. All features are
// relative (how this candidate compares to others) or structural
// (properties of the candidate itself). This ensures generalization
// across repos with different layouts.
const (
	// --- Name match features (query-relative) ---
	FCaseExact    = iota // 1 if name == query (case-sensitive)
	FCaseMatch           // 1 if case-insensitive match
	FIsPrefix            // 1 if query is prefix of name
	FIsSuffix            // 1 if query is suffix of name
	FNameLenRatio        // len(query) / len(name), clamped to [0,1]

	// --- Symbol type (one-hot) ---
	FTypeFunc
	FTypeMethod
	FTypeStruct
	FTypeIface
	FTypeOther

	// --- Symbol properties ---
	FIsDefinition // struct/class/interface/type/enum
	FLogSpan      // log2(span+1) / 10

	// --- Path features (relative, not category-based) ---
	FDepth          // absolute depth / 8
	FDepthRank      // depth rank among candidates (0=shallowest, 1=deepest) [cross-cand]
	FDirPopularity  // fraction of candidates in the same top-level dir [cross-cand]
	FIsTestPath     // contains test/tests/testing/spec/_test (structural, not dir name)
	FNameInPath     // 1 if query appears in the file path (e.g. "open" in open.c)

	// --- Extension features (relative) ---
	FExtMajorityRatio // fraction of candidates sharing this extension [cross-cand]

	// --- Span features (relative) ---
	FSpanRank     // span rank among candidates (0=smallest, 1=largest) [cross-cand]
	FMaxSpanRatio // this span / max span [cross-cand]

	// --- File importance ---
	FFileSymbolCount // log2(symbols in file + 1) / 10

	// --- Name character features ---
	FNameHasUnderscore // C/Python convention
	FNameAllLower      // all lowercase
	FNameStartsUpper   // Go/Rust/Java convention
	FNameIsShort       // len(name) <= 3

	// --- Global context (same for all candidates) ---
	FCandidateCount // log2(total candidates) / 6
	FQueryLen       // len(query) / 20
	FSpanStdDev     // std dev of log-spans / 3, how varied candidates are
	FDepthStdDev    // std dev of depths / 3, how spread out in the tree
	FExtDiversity   // number of distinct extensions / 5 (language mix)

	// = 30 features = NumFeatures
)

// CandidateFeatures holds the raw info needed to extract features.
type CandidateFeatures struct {
	Name            string
	Type            string // "function", "method", "struct", etc.
	File            string // relative path from repo root
	StartLine       uint32
	EndLine         uint32
	FileSymbolCount int // total symbols in the same file (0 if unknown)
}

// ExtractFeatures computes per-candidate features (not cross-candidate).
// Cross-candidate features are filled in by ExtractAll.
func ExtractFeatures(query string, c CandidateFeatures) [NumFeatures]float32 {
	var f [NumFeatures]float32
	queryLower := strings.ToLower(query)
	nameLower := strings.ToLower(c.Name)

	// Name match
	if c.Name == query {
		f[FCaseExact] = 1
	}
	if nameLower == queryLower {
		f[FCaseMatch] = 1
	}
	if strings.HasPrefix(nameLower, queryLower) {
		f[FIsPrefix] = 1
	}
	if strings.HasSuffix(nameLower, queryLower) {
		f[FIsSuffix] = 1
	}
	if len(c.Name) > 0 {
		ratio := float32(len(query)) / float32(len(c.Name))
		if ratio > 1 {
			ratio = 1
		}
		f[FNameLenRatio] = ratio
	}

	// Symbol type (one-hot)
	switch c.Type {
	case "function":
		f[FTypeFunc] = 1
	case "method":
		f[FTypeMethod] = 1
	case "struct", "class":
		f[FTypeStruct] = 1
	case "interface", "trait":
		f[FTypeIface] = 1
	default:
		f[FTypeOther] = 1
	}

	// Definition type
	switch c.Type {
	case "struct", "class", "interface", "type", "trait", "enum":
		f[FIsDefinition] = 1
	}

	// Span (log-normalized)
	span := int(c.EndLine) - int(c.StartLine) + 1
	if span < 1 {
		span = 1
	}
	f[FLogSpan] = clamp(float32(math.Log2(float64(span)))/10.0, 0, 1)

	// Depth (absolute)
	rel := c.File
	depth := strings.Count(rel, string(filepath.Separator))
	f[FDepth] = clamp(float32(depth)/8.0, 0, 1)

	// Test path detection (structural: look for patterns, not dir names)
	if isTestFile(rel) {
		f[FIsTestPath] = 1
	}

	// Name in path: query appears in the file path
	if queryLower != "" && strings.Contains(strings.ToLower(rel), queryLower) {
		f[FNameInPath] = 1
	}

	// File symbol count
	if c.FileSymbolCount > 0 {
		f[FFileSymbolCount] = clamp(float32(math.Log2(float64(c.FileSymbolCount+1)))/10.0, 0, 1)
	}

	// Name character features
	if strings.Contains(c.Name, "_") {
		f[FNameHasUnderscore] = 1
	}
	allLower := true
	for _, r := range c.Name {
		if r >= 'A' && r <= 'Z' {
			allLower = false
			break
		}
	}
	if allLower {
		f[FNameAllLower] = 1
	}
	if len(c.Name) > 0 && unicode.IsUpper(rune(c.Name[0])) {
		f[FNameStartsUpper] = 1
	}
	if len(c.Name) <= 3 {
		f[FNameIsShort] = 1
	}

	return f
}

// ExtractAll builds the flat feature matrix for a batch of candidates.
// Computes cross-candidate features that require seeing all candidates.
func ExtractAll(query string, candidates []CandidateFeatures) []float32 {
	n := len(candidates)
	out := make([]float32, n*NumFeatures)

	// Per-candidate features first
	for i, c := range candidates {
		f := ExtractFeatures(query, c)
		copy(out[i*NumFeatures:], f[:])
	}

	if n < 2 {
		return out
	}

	// --- Cross-candidate: extension majority ratio ---
	extCount := map[string]int{}
	for _, c := range candidates {
		ext := strings.ToLower(filepath.Ext(c.File))
		extCount[ext]++
	}
	for i, c := range candidates {
		ext := strings.ToLower(filepath.Ext(c.File))
		out[i*NumFeatures+FExtMajorityRatio] = float32(extCount[ext]) / float32(n)
	}

	// --- Cross-candidate: span rank + max span ratio ---
	spans := make([]int, n)
	for i, c := range candidates {
		spans[i] = int(c.EndLine) - int(c.StartLine) + 1
		if spans[i] < 1 {
			spans[i] = 1
		}
	}
	maxSpan := 0
	for _, s := range spans {
		if s > maxSpan {
			maxSpan = s
		}
	}
	for i := range candidates {
		smaller := 0
		for j := range candidates {
			if spans[j] < spans[i] {
				smaller++
			}
		}
		out[i*NumFeatures+FSpanRank] = float32(smaller) / float32(n-1)
		if maxSpan > 0 {
			out[i*NumFeatures+FMaxSpanRatio] = float32(spans[i]) / float32(maxSpan)
		}
	}

	// --- Cross-candidate: depth rank ---
	depths := make([]int, n)
	for i, c := range candidates {
		depths[i] = strings.Count(c.File, string(filepath.Separator))
	}
	for i := range candidates {
		shallower := 0
		for j := range candidates {
			if depths[j] < depths[i] {
				shallower++
			}
		}
		out[i*NumFeatures+FDepthRank] = float32(shallower) / float32(n-1)
	}

	// --- Cross-candidate: dir popularity ---
	dirCount := map[string]int{}
	for _, c := range candidates {
		dir := strings.SplitN(c.File, string(filepath.Separator), 2)[0]
		dirCount[dir]++
	}
	for i, c := range candidates {
		dir := strings.SplitN(c.File, string(filepath.Separator), 2)[0]
		out[i*NumFeatures+FDirPopularity] = float32(dirCount[dir]) / float32(n)
	}

	// --- Global context ---
	candCount := clamp(float32(math.Log2(float64(n)))/6.0, 0, 1)
	queryLen := clamp(float32(len(query))/20.0, 0, 1)

	// Span std dev
	meanLogSpan := float32(0)
	for _, s := range spans {
		meanLogSpan += float32(math.Log2(float64(s)))
	}
	meanLogSpan /= float32(n)
	spanVar := float32(0)
	for _, s := range spans {
		d := float32(math.Log2(float64(s))) - meanLogSpan
		spanVar += d * d
	}
	spanStdDev := clamp(float32(math.Sqrt(float64(spanVar/float32(n))))/3.0, 0, 1)

	// Depth std dev
	meanDepth := float32(0)
	for _, d := range depths {
		meanDepth += float32(d)
	}
	meanDepth /= float32(n)
	depthVar := float32(0)
	for _, d := range depths {
		diff := float32(d) - meanDepth
		depthVar += diff * diff
	}
	depthStdDev := clamp(float32(math.Sqrt(float64(depthVar/float32(n))))/3.0, 0, 1)

	// Extension diversity
	extDiversity := clamp(float32(len(extCount))/5.0, 0, 1)

	for i := range candidates {
		out[i*NumFeatures+FCandidateCount] = candCount
		out[i*NumFeatures+FQueryLen] = queryLen
		out[i*NumFeatures+FSpanStdDev] = spanStdDev
		out[i*NumFeatures+FDepthStdDev] = depthStdDev
		out[i*NumFeatures+FExtDiversity] = extDiversity
	}

	return out
}

func isTestFile(rel string) bool {
	lower := strings.ToLower(rel)
	for _, seg := range []string{"test/", "tests/", "testing/", "spec/", "__tests__/", "_test.", "test_"} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
