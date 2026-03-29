// Text-based reference finding and dependency extraction.
// Replaces tree-sitter AST-based identifier scanning with word-boundary matching.
package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"unicode"
)

// FindIdentifierOccurrences finds all occurrences of symbolName across
// indexed files using text-based word matching. Returns exact byte ranges
// suitable for rename operations. When semantic refs are available, scopes
// the search to files that reference the symbol.
func FindIdentifierOccurrences(ctx context.Context, db SymbolStore, symbolName string) ([]SymbolInfo, error) {
	sym, err := db.ResolveSymbol(ctx, symbolName)
	if err != nil {
		var ambErr *AmbiguousSymbolError
		if errors.As(err, &ambErr) {
			if allCandidatesLackImports(ambErr.Candidates) {
				return findReferencesTextBased(ctx, db, symbolName)
			}
			return nil, err
		}
		return findReferencesTextBased(ctx, db, symbolName)
	}

	// Always use text-based for now (imports not extracted by regex parser).
	_ = sym
	return findReferencesTextBased(ctx, db, symbolName)
}

// allCandidatesLackImports returns true if none of the candidates are in
// languages with strong import resolution.
func allCandidatesLackImports(candidates []SymbolInfo) bool {
	strongLangs := map[string]bool{"go": true, "python": true, "javascript": true, "typescript": true}
	for _, c := range candidates {
		if strongLangs[LangID(c.File)] {
			return false
		}
	}
	return len(candidates) > 0
}

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
func FindReferencesInFile(ctx context.Context, db SymbolStore, symbolName, symbolFile string) ([]SymbolInfo, error) {
	if db.HasRefs(ctx) {
		results, err := db.FindSemanticReferences(ctx, symbolName, symbolFile)
		if err == nil && len(results) > 0 {
			return results, nil
		}
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

	// Extract identifiers from the symbol body using text tokenization.
	body := src[sym.StartByte:sym.EndByte]
	idents := extractIdentifiers(string(body))

	var deps []SymbolInfo
	depSeen := make(map[string]bool)
	for _, name := range idents {
		if name == sym.Name || builtinNames[name] {
			continue
		}
		matches, err := db.SearchSymbols(ctx, name)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if m.Name == name {
				key := m.File + ":" + m.Name
				if !depSeen[key] {
					depSeen[key] = true
					deps = append(deps, m)
				}
			}
		}
	}
	return deps, nil
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
