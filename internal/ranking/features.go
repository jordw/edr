package ranking

import (
	"math"
	"path/filepath"
	"strings"
)

// Feature indices into the flat feature vector.
const (
	FCaseExact    = iota // 1 if name == query (case-sensitive)
	FCaseMatch           // 1 if case-insensitive match
	FIsPrefix            // 1 if query is prefix of name (case-insensitive)
	FIsSuffix            // 1 if query is suffix of name
	FNameLenRatio        // len(query) / len(name), clamped to [0,1]
	// Symbol type (one-hot)
	FTypeFunc
	FTypeMethod
	FTypeStruct
	FTypeIface
	FTypeOther
	// Symbol properties
	FIsDefinition // struct/class/interface/type
	FLogSpan      // log2(span+1) / 10, clamped to [0,1]
	// Path features
	FDepth       // path separator count / 8, clamped to [0,1]
	FIsInclude   // starts with include/
	FIsCore      // core infrastructure directory
	FIsPeripheral
	FIsTools
	FIsTest
	FIsVendor
	FIsDoc
	FIsSample
	FIsScripts
	// File extension (one-hot)
	FExtC     // .c or .h
	FExtGo    // .go
	FExtRust  // .rs
	FExtPyTS // .py, .ts, .js
	// Cross-candidate features (set by ExtractAll, not ExtractFeatures)
	FExtMajorityRatio // fraction of candidates sharing this extension
	FSpanRank         // this candidate's span rank among all (0=smallest, 1=largest)
	FFileSymbolCount  // log2(symbols in file + 1) / 10, proxy for file importance
	// Name character features
	FNameHasUnderscore // name contains underscore (C/Python convention)
	FNameAllLower      // name is all lowercase
	FNameStartsUpper   // name starts with uppercase (Go/Rust/Java convention)
	FNameIsShort       // len(name) <= 3
	// Global context features (set by ExtractAll)
	FCandidateCount   // log2(total candidates) / 6, clamped
	FPeripheralRatio  // fraction of candidates from peripheral paths
	FCoreRatio        // fraction of candidates from core paths
	FQueryLen         // len(query) / 20, clamped to [0,1]
	FSpanStdDev       // std dev of log-spans / 3, how varied the candidates are
	FMaxSpanRatio     // this candidate's span / max span among all candidates
	// = 40 features = NumFeatures
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

// ExtractFeatures computes the feature vector for a single candidate
// relative to a query. Returns a [NumFeatures]float32 slice.
func ExtractFeatures(query string, c CandidateFeatures) [NumFeatures]float32 {
	var f [NumFeatures]float32
	queryLower := strings.ToLower(query)
	nameLower := strings.ToLower(c.Name)

	// Name match features
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

	// Path features
	rel := c.File
	depth := strings.Count(rel, string(filepath.Separator))
	f[FDepth] = clamp(float32(depth)/8.0, 0, 1)

	if strings.HasPrefix(rel, "include/") || strings.HasPrefix(rel, "include\\") {
		f[FIsInclude] = 1
	}

	topDir := strings.SplitN(rel, string(filepath.Separator), 2)[0]

	// Core infrastructure
	switch topDir {
	case "kernel", "core", "init", "mm", "fs", "net", "block", "ipc", "security",
		"internal", "pkg", "cmd", "src", "lib":
		f[FIsCore] = 1
	}

	// Peripheral
	switch topDir {
	case "drivers", "plugins", "extensions", "addons", "contrib",
		"adapters", "connectors", "integrations":
		f[FIsPeripheral] = 1
	}

	// Tools
	switch topDir {
	case "tools", "tool", "util", "utils", "hack", "misc":
		f[FIsTools] = 1
	}

	// Test
	if isTestFile(rel) {
		f[FIsTest] = 1
	}

	// Vendor
	switch topDir {
	case "vendor", "node_modules", "third_party":
		f[FIsVendor] = 1
	}

	// Doc
	switch topDir {
	case "docs", "doc", "documentation", "Documentation":
		f[FIsDoc] = 1
	}

	// Sample
	switch topDir {
	case "examples", "example", "samples", "sample", "demo", "demos":
		f[FIsSample] = 1
	}

	// Scripts/build
	switch topDir {
	case "scripts", "script", "build", "ci", "deploy":
		f[FIsScripts] = 1
	}

	// File extension (one-hot)
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".cxx":
		f[FExtC] = 1
	case ".go":
		f[FExtGo] = 1
	case ".rs":
		f[FExtRust] = 1
	case ".py", ".ts", ".js", ".tsx", ".jsx":
		f[FExtPyTS] = 1
	}

	// File symbol count (proxy for file importance)
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
	if len(c.Name) > 0 && c.Name[0] >= 'A' && c.Name[0] <= 'Z' {
		f[FNameStartsUpper] = 1
	}
	if len(c.Name) <= 3 {
		f[FNameIsShort] = 1
	}

	return f
}

// ExtractAll builds the flat feature matrix for a batch of candidates.
// Returns [n * NumFeatures]float32. Computes cross-candidate features
// (extension majority ratio, span rank) that require seeing all candidates.
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

	// Cross-candidate: extension majority ratio
	extCount := map[string]int{}
	for _, c := range candidates {
		ext := strings.ToLower(filepath.Ext(c.File))
		extCount[ext]++
	}
	for i, c := range candidates {
		ext := strings.ToLower(filepath.Ext(c.File))
		out[i*NumFeatures+FExtMajorityRatio] = float32(extCount[ext]) / float32(n)
	}

	// Cross-candidate: span rank (0=smallest, 1=largest)
	spans := make([]int, n)
	for i, c := range candidates {
		spans[i] = int(c.EndLine) - int(c.StartLine) + 1
		if spans[i] < 1 {
			spans[i] = 1
		}
	}
	// Count how many candidates have smaller span + max span ratio
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

	// Global context: candidate count, peripheral/core ratios, query len
	candCount := clamp(float32(math.Log2(float64(n)))/6.0, 0, 1)
	peripheralCount := 0
	coreCount := 0
	for i := range candidates {
		if out[i*NumFeatures+FIsPeripheral] > 0 {
			peripheralCount++
		}
		if out[i*NumFeatures+FIsCore] > 0 {
			coreCount++
		}
	}
	periphRatio := float32(peripheralCount) / float32(n)
	coreRatio := float32(coreCount) / float32(n)
	queryLen := clamp(float32(len(query))/20.0, 0, 1)

	// Span std dev
	meanLogSpan := float32(0)
	for _, s := range spans {
		meanLogSpan += float32(math.Log2(float64(s)))
	}
	meanLogSpan /= float32(n)
	variance := float32(0)
	for _, s := range spans {
		d := float32(math.Log2(float64(s))) - meanLogSpan
		variance += d * d
	}
	spanStdDev := clamp(float32(math.Sqrt(float64(variance/float32(n))))/3.0, 0, 1)

	for i := range candidates {
		out[i*NumFeatures+FCandidateCount] = candCount
		out[i*NumFeatures+FPeripheralRatio] = periphRatio
		out[i*NumFeatures+FCoreRatio] = coreRatio
		out[i*NumFeatures+FQueryLen] = queryLen
		out[i*NumFeatures+FSpanStdDev] = spanStdDev
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
