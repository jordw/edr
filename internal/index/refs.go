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
		if !Supported(file) {
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
	if !Supported(sym.File) {
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

	var otherIdents []string
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
			}
		} else {
			otherIdents = append(otherIdents, name)
		}
	}

	// Phase 2: cross-file lookup for remaining identifiers.
	// Use the symbol index for O(k) lookups when available;
	// fall back to AllSymbols for small repos without an index.
	if len(otherIdents) > 0 {
		crossStart := len(deps)
		symDir := filepath.Dir(sym.File)
		edrDir := db.EdrDir()

		if idx.HasSymbolIndex(edrDir) {
			// Fast path: symbol index available — O(k) lookups via cached lightweight index
			_, files := idx.LoadAllSymbols(edrDir)
			for _, name := range otherIdents {
				entries := idx.LookupSymbols(edrDir, name)
				if len(entries) > 5 {
					continue
				}
				for _, e := range entries {
					rel := ""
					if int(e.FileID) < len(files) {
						rel = files[e.FileID].Path
					}
					abs := filepath.Join(db.Root(), rel)
					key := abs + ":" + e.Name
					if depSeen[key] {
						continue
					}
					if idx.IsDirtyFile(edrDir, rel) {
						continue
					}
					depSeen[key] = true
					deps = append(deps, SymbolInfo{
						Name: e.Name, Type: e.Kind.String(),
						File:      abs,
						StartLine: e.StartLine, EndLine: e.EndLine,
						StartByte: e.StartByte, EndByte: e.EndByte,
					})
				}
			}
		} else {
			// Slow path: no index — parse all symbols (small repos only)
			fileCount, _, _ := db.Stats(ctx)
			if fileCount <= 1000 {
				allSyms, err := db.AllSymbols(ctx)
				if err == nil {
					byName := make(map[string][]SymbolInfo, len(allSyms)/2)
					for _, s := range allSyms {
						byName[s.Name] = append(byName[s.Name], s)
					}
					for _, name := range otherIdents {
						matches := byName[name]
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
				}
			}
		}

		// Sort cross-file deps by proximity: same dir first, then by path distance.
		if crossStart < len(deps) {
			cross := deps[crossStart:]
			sort.SliceStable(cross, func(i, j int) bool {
				di := filepath.Dir(cross[i].File)
				dj := filepath.Dir(cross[j].File)
				iSame := di == symDir
				jSame := dj == symDir
				if iSame != jSame {
					return iSame
				}
				return pathDistance(di, symDir) < pathDistance(dj, symDir)
			})
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

// builtinNames contains language builtins, keywords, and common short identifiers
// that should not be treated as cross-file dependencies. Covers Go, C/C++, Rust,
// Python, JavaScript/TypeScript, and Java.
var builtinNames = map[string]bool{
	// Go builtins and keywords
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

	// C/C++ keywords and common types
	"void": true, "char": true, "short": true, "long": true,
	"signed": true, "unsigned": true, "float": true, "double": true,
	"struct": true, "union": true, "enum": true, "typedef": true,
	"const": true, "static": true, "extern": true, "volatile": true,
	"inline": true, "register": true, "auto": true, "sizeof": true,
	"if": true, "else": true, "for": true, "while": true, "do": true,
	"switch": true, "case": true, "default": true, "break": true,
	"continue": true, "return": true, "goto": true,
	"NULL": true, "size_t": true, "ssize_t": true, "ptrdiff_t": true,
	"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
	"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
	"u8": true, "u16": true, "u32": true, "u64": true,
	"s8": true, "s16": true, "s32": true, "s64": true,
	"__u8": true, "__u16": true, "__u32": true, "__u64": true,
	"__s8": true, "__s16": true, "__s32": true, "__s64": true,
	"ifdef": true, "ifndef": true, "endif": true, "define": true,
	"include": true, "pragma": true, "undef": true,
	"class": true, "namespace": true, "template": true, "typename": true,
	"virtual": true, "override": true, "final": true, "explicit": true,
	"public": true, "private": true, "protected": true, "friend": true,
	"this": true, "nullptr": true, "throw": true, "catch": true, "try": true,
	"noexcept": true, "constexpr": true, "decltype": true,
	"static_cast": true, "dynamic_cast": true, "reinterpret_cast": true,

	// Rust keywords
	"fn": true, "let": true, "mut": true, "ref": true, "self": true,
	"Self": true, "impl": true, "trait": true, "pub": true, "mod": true,
	"crate": true, "super": true, "where": true, "async": true,
	"await": true, "move": true, "dyn": true, "unsafe": true,
	"match": true, "loop": true, "in": true, "as": true, "use": true,
	"type": true, "Some": true, "None": true, "Ok": true, "Err": true,
	"Box": true, "Vec": true, "String": true, "str": true,
	"i8": true, "i16": true, "i32": true, "i64": true, "i128": true,
	"f32": true, "f64": true, "usize": true, "isize": true,

	// Python keywords and builtins
	"def": true, "import": true, "from": true,
	"and": true, "or": true, "not": true, "is": true,
	"with": true, "yield": true, "raise": true, "except": true,
	"finally": true, "pass": true, "lambda": true, "global": true,
	"nonlocal": true, "assert": true, "elif": true,
	"True": true, "False": true, "cls": true,
	"range": true, "list": true, "dict": true, "set": true, "tuple": true,
	"isinstance": true, "getattr": true, "setattr": true, "hasattr": true,
	"property": true, "staticmethod": true, "classmethod": true,

	// JavaScript/TypeScript keywords
	"var": true, "function": true, "typeof": true,
	"instanceof": true, "undefined": true, "arguments": true,
	"export": true, "require": true, "module": true, "extends": true,
	"implements": true, "interface": true, "abstract": true,
	"constructor": true, "prototype": true, "toString": true,
	"valueOf": true, "apply": true, "call": true, "bind": true,
	"Promise": true, "Array": true, "Object": true, "Map": true,
	"Set": true, "number": true, "boolean": true,
	"never": true, "unknown": true, "readonly": true, "declare": true,
	"keyof": true, "infer": true,

	// Common short identifiers (too generic to be useful deps)
	"of": true, "to": true, "on": true, "at": true, "by": true,
	"up": true, "no": true, "id": true, "fd": true, "rc": true,
	"op": true, "nr": true, "sz": true, "ch": true, "tx": true,
	"rx": true, "wp": true, "rw": true, "tp": true, "bp": true,
	"sb": true, "db": true, "cb": true, "io": true, "fs": true,
	"os": true, "ip": true, "hw": true, "sw": true, "mm": true,
	"np": true, "sp": true, "pp": true, "dp": true, "tt": true,
	"xx": true, "cc": true, "ll": true, "ss": true,
	"val": true, "tmp": true, "buf": true, "ret": true, "res": true,
	"src": true, "dst": true, "msg": true, "cmd": true, "cfg": true,
	"max": true, "min": true, "pos": true, "off": true, "end": true,
	"key": true, "idx": true, "cnt": true, "ptr": true, "cur": true,
	"old": true, "out": true, "got": true, "arg": true, "abs": true,
	"log": true, "dev": true, "cpu": true, "irq": true, "skb": true,
	"get": true, "put": true, "add": true, "sub": true, "run": true,
}
