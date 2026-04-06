// Text-based reference finding and dependency extraction.
// Replaces tree-sitter AST-based identifier scanning with word-boundary matching.
package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/jordw/edr/internal/idx"
)

// findReferencesTextBased scans all repo files for whole-word matches of symbolName.
func findReferencesTextBased(ctx context.Context, db SymbolStore, symbolName string) ([]SymbolInfo, error) {
	var files []string
	WalkRepoFiles(db.Root(), func(path string) error {
		files = append(files, path)
		return nil
	})

	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbolName) + `\b`)

	var refs []SymbolInfo
	for _, file := range files {
		if !RegexSupported(file) {
			continue
		}
		src, err := CachedReadFile(ctx, file)
		if err != nil {
			continue
		}
		found := findWordOccurrences(src, file, symbolName, re)
		refs = append(refs, found...)
	}

	return refs, nil
}

// findWordOccurrences finds all whole-word matches of name in src, returning
// SymbolInfo entries with type "reference" and exact byte ranges.
func findWordOccurrences(src []byte, file, name string, re *regexp.Regexp) []SymbolInfo {
	var refs []SymbolInfo
	matches := re.FindAllIndex(src, -1)
	for _, m := range matches {
		startByte := uint32(m[0])
		endByte := uint32(m[1])
		// Compute line number
		line := uint32(1)
		for i := 0; i < m[0] && i < len(src); i++ {
			if src[i] == '\n' {
				line++
			}
		}
		refs = append(refs, SymbolInfo{
			Type:      "reference",
			Name:      name,
			File:      file,
			StartLine: line,
			EndLine:   line,
			StartByte: startByte,
			EndByte:   endByte,
		})
	}
	return refs
}

// FindReferencesInFile searches for references to a symbol name.
// Uses heuristic body-text matching — not a true semantic reference index.
func FindReferencesInFile(ctx context.Context, db SymbolStore, symbolName, symbolFile string) ([]SymbolInfo, error) {
	// Try body-substring matching first (fast heuristic, not semantic).
	results, err := db.FindSemanticReferences(ctx, symbolName, symbolFile)
	if err == nil && len(results) > 0 {
		return results, nil
	}
	return findReferencesTextBased(ctx, db, symbolName)
}

// FindDeps finds symbols that the given symbol depends on.
func FindDeps(ctx context.Context, db SymbolStore, sym *SymbolInfo) ([]SymbolInfo, error) {
	return findDepsTextBased(ctx, db, sym)
}

// findDepsTextBased extracts identifiers from a symbol body and resolves them.
func findDepsTextBased(ctx context.Context, db SymbolStore, sym *SymbolInfo) ([]SymbolInfo, error) {
	if !RegexSupported(sym.File) {
		return nil, fmt.Errorf("unsupported language for %s", sym.File)
	}

	src, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, err
	}

	// Extract identifiers from the symbol body.
	body := src[sym.StartByte:sym.EndByte]
	idents := extractIdentifiers(string(body))

	// Phase 1: same-file symbols (fast, no repo-wide parse).
	sameFileSyms, _ := db.GetSymbolsByFile(ctx, sym.File)
	sameFileByName := make(map[string]SymbolInfo, len(sameFileSyms))
	for _, s := range sameFileSyms {
		sameFileByName[s.Name] = s
	}

	var sameFile, otherIdents []string
	depSeen := make(map[string]bool)
	var deps []SymbolInfo
	for _, name := range idents {
		if builtinNames[name] {
			continue
		}
		if m, ok := sameFileByName[name]; ok {
			if m.File == sym.File && m.Name == sym.Name && m.StartLine == sym.StartLine {
				continue
			}
			key := m.File + ":" + m.Name
			if !depSeen[key] {
				depSeen[key] = true
				deps = append(deps, m)
				sameFile = append(sameFile, name)
			}
		} else {
			otherIdents = append(otherIdents, name)
		}
	}

	// Phase 2: repo-wide lookup for remaining identifiers.
	// Skip if same-file already found enough deps, or if the repo is too
	// large for a full parse to be worthwhile (>1000 files).
	// Use the index header for a fast file count when available.
	fileCount := 0
	if h, err := idx.ReadHeader(db.EdrDir()); err == nil {
		fileCount = int(h.NumFiles)
	} else {
		fileCount, _, _ = db.Stats(ctx)
	}
	if len(deps) < 10 && len(otherIdents) > 0 && fileCount <= 1000 {
		allSyms, err := db.AllSymbols(ctx)
		if err == nil {
			byName := make(map[string][]SymbolInfo, len(allSyms)/2)
			for _, s := range allSyms {
				byName[s.Name] = append(byName[s.Name], s)
			}

			symDir := filepath.Dir(sym.File)
			for _, name := range otherIdents {
				matches := byName[name]
				// Skip overly ambiguous names (>5 definitions across repo)
				if len(matches) > 5 {
					continue
				}
				for _, m := range matches {
					key := m.File + ":" + m.Name
					if !depSeen[key] {
						depSeen[key] = true
						deps = append(deps, m)
					}
				}
			}

			// Sort non-same-file deps by proximity: same dir first, then
			// by path depth distance.
			sameFileCount := len(sameFile)
			if sameFileCount < len(deps) {
				cross := deps[sameFileCount:]
				sort.SliceStable(cross, func(i, j int) bool {
					di := filepath.Dir(cross[i].File)
					dj := filepath.Dir(cross[j].File)
					// Same directory as target sorts first
					iSame := di == symDir
					jSame := dj == symDir
					if iSame != jSame {
						return iSame
					}
					// Closer path (fewer separators of difference) sorts first
					return pathDistance(di, symDir) < pathDistance(dj, symDir)
				})
			}
		}
	}

	return deps, nil
}

// pathDistance returns a rough measure of how far apart two directory paths are.
func pathDistance(a, b string) int {
	if a == b {
		return 0
	}
	pa := strings.Split(a, string(filepath.Separator))
	pb := strings.Split(b, string(filepath.Separator))
	// Find common prefix length
	common := 0
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] != pb[i] {
			break
		}
		common++
	}
	return (len(pa) - common) + (len(pb) - common)
}

// extractIdentifiers splits source text into unique identifiers,
// filtering out builtins, keywords, and non-identifier tokens.
func extractIdentifiers(body string) []string {
	seen := make(map[string]bool)
	var result []string

	// Split on non-identifier characters
	start := -1
	for i, r := range body {
		isIdent := unicode.IsLetter(r) || r == '_' || (unicode.IsDigit(r) && start >= 0)
		if isIdent {
			if start < 0 {
				start = i
			}
		} else {
			if start >= 0 {
				word := body[start:i]
				if len(word) > 1 && !seen[word] && !builtinNames[word] {
					seen[word] = true
					result = append(result, word)
				}
				start = -1
			}
		}
	}
	// Handle last token
	if start >= 0 {
		word := body[start:]
		if len(word) > 1 && !seen[word] && !builtinNames[word] {
			seen[word] = true
			result = append(result, word)
		}
	}
	return result
}

// builtinNames contains Go builtins and common names that should not be treated as dependencies.
var builtinNames = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"any": true,
	"append": true, "cap": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true,
	"make": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
	"nil": true, "true": true, "false": true, "iota": true,
	"err": true, "ok": true, "ctx": true, "_": true,
}
