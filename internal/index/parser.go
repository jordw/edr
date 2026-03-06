package index

import (
	"context"
	"fmt"
	"os"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// SymbolInfo represents an extracted symbol from source code.
type SymbolInfo struct {
	Type      string // "function", "class", "struct", "method", etc.
	Name      string
	File      string
	StartLine uint32
	EndLine   uint32
	StartByte uint32
	EndByte   uint32
	Body      string // raw source text of the symbol
}

// ParseFile extracts symbols from a single file using tree-sitter.
func ParseFile(path string) ([]SymbolInfo, error) {
	lang := GetLangConfig(path)
	if lang == nil {
		return nil, fmt.Errorf("unsupported language for %s", path)
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang.Language); err != nil {
		return nil, fmt.Errorf("set language: %w", err)
	}

	tree := parser.Parse(src, nil)
	defer tree.Close()

	root := tree.RootNode()
	var symbols []SymbolInfo
	extractSymbols(root, src, path, lang, &symbols)
	return symbols, nil
}

// ParseSource parses source code from bytes (used during indexing when source is already loaded).
func ParseSource(path string, src []byte, lang *LangConfig) ([]SymbolInfo, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang.Language); err != nil {
		return nil, fmt.Errorf("set language: %w", err)
	}

	tree := parser.Parse(src, nil)
	defer tree.Close()

	root := tree.RootNode()
	var symbols []SymbolInfo
	extractSymbols(root, src, path, lang, &symbols)
	return symbols, nil
}

// ParseResult holds the complete parse output for indexing.
type ParseResult struct {
	Symbols []SymbolInfo
	Imports []ImportInfo
	// ExtractRefs is called after symbol IDs are known. It returns ref edges.
	ExtractRefs func(symbolIDs map[int]int64) []RefInfo
}

// ParseFileComplete parses a file and returns symbols, imports, and a deferred ref extractor.
func ParseFileComplete(path string, src []byte, lang *LangConfig) (*ParseResult, error) {
	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang.Language); err != nil {
		return nil, fmt.Errorf("set language: %w", err)
	}

	tree := parser.Parse(src, nil)
	defer tree.Close()

	root := tree.RootNode()

	// Extract symbols
	var symbols []SymbolInfo
	extractSymbols(root, src, path, lang, &symbols)

	// Extract imports
	var imports []ImportInfo
	if lang.Imports != nil {
		imports = extractImports(root, src, path, lang)
	}

	// Extract raw ref data while tree is still alive.
	// Symbol IDs come later, so we store indices and resolve in ExtractRefs.
	var rawRefs []rawRef
	for i, sym := range symbols {
		extractRefsFromSymbol(root, src, &sym, i, &rawRefs)
	}

	result := &ParseResult{
		Symbols: symbols,
		Imports: imports,
		ExtractRefs: func(symbolIDs map[int]int64) []RefInfo {
			var refs []RefInfo
			for _, rr := range rawRefs {
				id, ok := symbolIDs[rr.symIdx]
				if !ok {
					continue
				}
				refs = append(refs, RefInfo{
					FromSymbolID: id,
					ToName:       rr.toName,
					Line:         rr.line,
					Kind:         rr.kind,
				})
			}
			return refs
		},
	}

	return result, nil
}

func extractSymbols(node *tree_sitter.Node, src []byte, path string, lang *LangConfig, out *[]SymbolInfo) {
	nodeType := node.Kind()

	for _, symType := range lang.SymbolNodes {
		if nodeType == symType {
			name := extractName(node, src, lang)
			if name != "" {
				startLine := uint32(node.StartPosition().Row + 1)
				endLine := uint32(node.EndPosition().Row + 1)
				*out = append(*out, SymbolInfo{
					Type:      normalizeType(nodeType),
					Name:      name,
					File:      path,
					StartLine: startLine,
					EndLine:   endLine,
					StartByte: uint32(node.StartByte()),
					EndByte:   uint32(node.EndByte()),
					Body:      string(src[node.StartByte():node.EndByte()]),
				})
			}
			break
		}
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			extractSymbols(child, src, path, lang, out)
		}
	}
}

func extractName(node *tree_sitter.Node, src []byte, lang *LangConfig) string {
	// Try the configured name field first
	nameNode := node.ChildByFieldName(lang.NameField)
	if nameNode != nil {
		return string(src[nameNode.StartByte():nameNode.EndByte()])
	}

	// For variable_declarator patterns (e.g., const foo = () => {})
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil && child.Kind() == "identifier" {
			return string(src[child.StartByte():child.EndByte()])
		}
	}

	return ""
}

func normalizeType(nodeType string) string {
	switch {
	case strings.Contains(nodeType, "function") || strings.Contains(nodeType, "method"):
		return "function"
	case strings.Contains(nodeType, "class"):
		return "class"
	case strings.Contains(nodeType, "struct"):
		return "struct"
	case strings.Contains(nodeType, "enum"):
		return "enum"
	case strings.Contains(nodeType, "impl"):
		return "impl"
	case strings.Contains(nodeType, "interface"):
		return "interface"
	case strings.Contains(nodeType, "type"):
		return "type"
	case strings.Contains(nodeType, "module"):
		return "module"
	default:
		return nodeType
	}
}

// FindReferences searches for references to a symbol name across files.
// Uses semantic refs (import-filtered) when available, falls back to text-based.
func FindReferences(ctx context.Context, db *DB, symbolName string) ([]SymbolInfo, error) {
	// Try semantic path first: find the symbol's definition file
	if db.HasRefs(ctx) {
		sym, err := db.ResolveSymbol(ctx, symbolName)
		if err == nil {
			return db.FindSemanticReferences(ctx, symbolName, sym.File)
		}
		// If symbol not uniquely resolved, try by exact name match in refs table
		// Still use semantic refs but without import filtering (better than nothing)
	}

	return findReferencesTextBased(ctx, db, symbolName)
}

// findReferencesTextBased is the legacy text-based reference search.
func findReferencesTextBased(ctx context.Context, db *DB, symbolName string) ([]SymbolInfo, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT DISTINCT file FROM symbols`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		files = append(files, f)
	}

	var refs []SymbolInfo
	for _, file := range files {
		lang := GetLangConfig(file)
		if lang == nil {
			continue
		}

		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		parser := tree_sitter.NewParser()
		if err := parser.SetLanguage(lang.Language); err != nil {
			parser.Close()
			continue
		}

		tree := parser.Parse(src, nil)
		root := tree.RootNode()
		findIdentifierRefs(root, src, file, symbolName, &refs)
		tree.Close()
		parser.Close()
	}

	return refs, nil
}

// FindReferencesInFile searches for references to a symbol name, where the symbol
// is defined in the given file. Uses semantic filtering.
func FindReferencesInFile(ctx context.Context, db *DB, symbolName, symbolFile string) ([]SymbolInfo, error) {
	if db.HasRefs(ctx) {
		return db.FindSemanticReferences(ctx, symbolName, symbolFile)
	}
	return findReferencesTextBased(ctx, db, symbolName)
}

// FindDeps finds symbols that the given symbol depends on.
// Uses semantic deps (import-filtered) when available, falls back to text-based.
func FindDeps(ctx context.Context, db *DB, sym *SymbolInfo) ([]SymbolInfo, error) {
	// Try semantic path
	if db.HasRefs(ctx) {
		symID, err := db.GetSymbolID(ctx, sym.File, sym.Name)
		if err == nil {
			return db.FindSemanticDeps(ctx, symID, sym.File)
		}
	}

	return findDepsTextBased(ctx, db, sym)
}

// findDepsTextBased is the legacy text-based dependency search.
func findDepsTextBased(ctx context.Context, db *DB, sym *SymbolInfo) ([]SymbolInfo, error) {
	lang := GetLangConfig(sym.File)
	if lang == nil {
		return nil, fmt.Errorf("unsupported language for %s", sym.File)
	}

	src, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, err
	}

	parser := tree_sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang.Language); err != nil {
		return nil, err
	}

	tree := parser.Parse(src, nil)
	defer tree.Close()

	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(tree.RootNode(), src, sym.StartByte, sym.EndByte, seen, &idents)

	var deps []SymbolInfo
	depSeen := make(map[string]bool)
	for _, name := range idents {
		if name == sym.Name {
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

func collectIdentifiers(node *tree_sitter.Node, src []byte, startByte, endByte uint32, seen map[string]bool, out *[]string) {
	nb := uint32(node.StartByte())
	ne := uint32(node.EndByte())
	// Skip nodes entirely outside the range
	if ne <= startByte || nb >= endByte {
		return
	}
	if node.Kind() == "identifier" || node.Kind() == "type_identifier" {
		if nb >= startByte && ne <= endByte {
			text := string(src[nb:ne])
			if !seen[text] {
				seen[text] = true
				*out = append(*out, text)
			}
		}
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			collectIdentifiers(child, src, startByte, endByte, seen, out)
		}
	}
}

// extractImports walks the AST and extracts import statements.
func extractImports(root *tree_sitter.Node, src []byte, path string, lang *LangConfig) []ImportInfo {
	if lang.Imports == nil {
		return nil
	}

	var imports []ImportInfo

	switch lang.LangID {
	case "go":
		extractGoImports(root, src, path, &imports)
	case "python":
		extractPythonImports(root, src, path, &imports)
	case "javascript", "typescript":
		extractJSImports(root, src, path, &imports)
	}

	return imports
}

func extractGoImports(node *tree_sitter.Node, src []byte, path string, out *[]ImportInfo) {
	kind := node.Kind()

	if kind == "import_spec" {
		pathNode := node.ChildByFieldName("path")
		if pathNode != nil {
			importPath := strings.Trim(string(src[pathNode.StartByte():pathNode.EndByte()]), `"`)
			alias := ""
			nameNode := node.ChildByFieldName("name")
			if nameNode != nil {
				alias = string(src[nameNode.StartByte():nameNode.EndByte()])
			}
			*out = append(*out, ImportInfo{
				File:       path,
				ImportPath: importPath,
				Alias:      alias,
			})
		}
		return
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			extractGoImports(child, src, path, out)
		}
	}
}

func extractPythonImports(node *tree_sitter.Node, src []byte, path string, out *[]ImportInfo) {
	kind := node.Kind()

	if kind == "import_statement" {
		// import foo, bar
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(uint(i))
			if child == nil {
				continue
			}
			if child.Kind() == "dotted_name" {
				importPath := string(src[child.StartByte():child.EndByte()])
				*out = append(*out, ImportInfo{
					File:       path,
					ImportPath: importPath,
				})
			} else if child.Kind() == "aliased_import" {
				nameNode := child.ChildByFieldName("name")
				aliasNode := child.ChildByFieldName("alias")
				if nameNode != nil {
					importPath := string(src[nameNode.StartByte():nameNode.EndByte()])
					alias := ""
					if aliasNode != nil {
						alias = string(src[aliasNode.StartByte():aliasNode.EndByte()])
					}
					*out = append(*out, ImportInfo{
						File:       path,
						ImportPath: importPath,
						Alias:      alias,
					})
				}
			}
		}
		return
	}

	if kind == "import_from_statement" {
		// from foo import bar, baz
		// Find the module name
		moduleName := ""
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(uint(i))
			if child == nil {
				continue
			}
			if child.Kind() == "dotted_name" || child.Kind() == "relative_import" {
				moduleName = string(src[child.StartByte():child.EndByte()])
				break
			}
		}
		if moduleName != "" {
			// Check for wildcard import
			text := string(src[node.StartByte():node.EndByte()])
			if strings.Contains(text, "import *") {
				*out = append(*out, ImportInfo{
					File:       path,
					ImportPath: moduleName,
					Alias:      "*",
				})
			} else {
				*out = append(*out, ImportInfo{
					File:       path,
					ImportPath: moduleName,
				})
			}
		}
		return
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			extractPythonImports(child, src, path, out)
		}
	}
}

func extractJSImports(node *tree_sitter.Node, src []byte, path string, out *[]ImportInfo) {
	kind := node.Kind()

	if kind == "import_statement" {
		sourceNode := node.ChildByFieldName("source")
		if sourceNode != nil {
			importPath := strings.Trim(string(src[sourceNode.StartByte():sourceNode.EndByte()]), `"'`)
			*out = append(*out, ImportInfo{
				File:       path,
				ImportPath: importPath,
			})
		}
		return
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			extractJSImports(child, src, path, out)
		}
	}
}

// extractRefsFromSymbol collects identifier references within a symbol's byte range.
type rawRef struct {
	symIdx int
	toName string
	line   uint32
	kind   string
}

func extractRefsFromSymbol(root *tree_sitter.Node, src []byte, sym *SymbolInfo, symIdx int, out *[]rawRef) {
	// Get the symbol's declaration name to skip it
	declName := sym.Name

	seen := make(map[string]bool)
	var walk func(node *tree_sitter.Node)
	walk = func(node *tree_sitter.Node) {
		nb := uint32(node.StartByte())
		ne := uint32(node.EndByte())

		// Skip nodes entirely outside the symbol's range
		if ne <= sym.StartByte || nb >= sym.EndByte {
			return
		}

		kind := node.Kind()

		if kind == "identifier" || kind == "type_identifier" || kind == "field_identifier" {
			if nb >= sym.StartByte && ne <= sym.EndByte {
				text := string(src[nb:ne])
				// Skip the symbol's own name and very short/common identifiers
				if text == declName || len(text) <= 1 {
					// Still skip — but allow if it's not the declaration position
					// We skip all occurrences of the symbol's own name to avoid self-refs
					if text == declName {
						goto recurse
					}
					goto recurse
				}

				refKind := "identifier"
				switch kind {
				case "type_identifier":
					refKind = "type"
				case "field_identifier":
					refKind = "field"
				}

				// Check if this is a call
				parent := node.Parent()
				if parent != nil && (parent.Kind() == "call_expression" || parent.Kind() == "call") {
					fnNode := parent.ChildByFieldName("function")
					if fnNode != nil && fnNode.StartByte() == node.StartByte() {
						refKind = "call"
					}
				}

				// Deduplicate by name within this symbol
				dedup := text + ":" + refKind
				if !seen[dedup] {
					seen[dedup] = true
					*out = append(*out, rawRef{
						symIdx: symIdx,
						toName: text,
						line:   uint32(node.StartPosition().Row + 1),
						kind:   refKind,
					})
				}
			}
		}

	recurse:
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(uint(i))
			if child != nil {
				walk(child)
			}
		}
	}

	walk(root)
}

func findIdentifierRefs(node *tree_sitter.Node, src []byte, file, name string, out *[]SymbolInfo) {
	kind := node.Kind()
	if kind == "identifier" || kind == "type_identifier" || kind == "field_identifier" {
		text := string(src[node.StartByte():node.EndByte()])
		if text == name {
			*out = append(*out, SymbolInfo{
				Type:      "reference",
				Name:      name,
				File:      file,
				StartLine: uint32(node.StartPosition().Row + 1),
				EndLine:   uint32(node.EndPosition().Row + 1),
				StartByte: uint32(node.StartByte()),
				EndByte:   uint32(node.EndByte()),
			})
		}
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			findIdentifierRefs(child, src, file, name, out)
		}
	}
}
