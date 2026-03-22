package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

// parserPools holds a sync.Pool per language ID for reusing tree-sitter parsers.
var parserPools sync.Map // langID -> *sync.Pool

// getParser retrieves a reusable parser for the given language from the pool,
// or creates a new one if the pool is empty.
func getParser(lang *LangConfig) *tree_sitter.Parser {
	pool, _ := parserPools.LoadOrStore(lang.LangID, &sync.Pool{
		New: func() any {
			p := tree_sitter.NewParser()
			p.SetLanguage(lang.Language)
			return p
		},
	})
	return pool.(*sync.Pool).Get().(*tree_sitter.Parser)
}

// putParser returns a parser to the pool for reuse.
func putParser(lang *LangConfig, p *tree_sitter.Parser) {
	pool, ok := parserPools.Load(lang.LangID)
	if ok {
		pool.(*sync.Pool).Put(p)
	}
}

// parseWith gets a pooled parser, parses src, calls fn with the tree root,
// then cleans up and returns the parser to the pool. Safe against panics.
func parseWith(lang *LangConfig, src []byte, fn func(root *tree_sitter.Node)) {
	parser := getParser(lang)
	tree := parser.Parse(src, nil)
	defer func() {
		if tree != nil {
			tree.Close()
		}
		putParser(lang, parser)
	}()
	if tree == nil {
		return
	}
	fn(tree.RootNode())
}

// SymbolInfo represents an extracted symbol from source code.
type SymbolInfo struct {
	Type      string // "function", "class", "struct", "method", etc.
	Name      string
	File      string
	StartLine uint32
	EndLine   uint32
	StartByte uint32
	EndByte     uint32
	Body        string // raw source text of the symbol
	ParentIndex int    // index into symbols slice at parse time; -1 = no parent
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
	var symbols []SymbolInfo
	var imports []ImportInfo
	var rawRefs []rawRef

	parseWith(lang, src, func(root *tree_sitter.Node) {
		extractSymbols(root, src, path, lang, &symbols, -1)

		if lang.Imports != nil {
			imports = extractImports(root, src, path, lang)
		}

		// Extract raw ref data while tree is still alive.
		// Symbol IDs come later, so we store indices and resolve in ExtractRefs.
		for i, sym := range symbols {
			extractRefsFromSymbol(root, src, &sym, i, &rawRefs)
		}
	})

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

func extractSymbols(node *tree_sitter.Node, src []byte, path string, lang *LangConfig, out *[]SymbolInfo, parentIdx int) {
	nodeType := node.Kind()

	thisIdx := -1
	for _, symType := range lang.SymbolNodes {
		if nodeType == symType {
			name := extractName(node, src, lang)
			if name != "" {
				startLine := uint32(node.StartPosition().Row + 1)
				endLine := uint32(node.EndPosition().Row + 1)
				*out = append(*out, SymbolInfo{
					Type:        normalizeType(nodeType),
					Name:        name,
					File:        path,
					StartLine:   startLine,
					EndLine:     endLine,
					StartByte:   uint32(node.StartByte()),
					EndByte:     uint32(node.EndByte()),
					Body:        string(src[node.StartByte():node.EndByte()]),
					ParentIndex: parentIdx,
				})
				thisIdx = len(*out) - 1
			}
			break
		}
	}

	// For children, use thisIdx as parent if this node was a symbol, otherwise pass through parentIdx
	childParent := parentIdx
	if thisIdx >= 0 {
		childParent = thisIdx
	}

	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil {
			extractSymbols(child, src, path, lang, out, childParent)
		}
	}
}

func extractName(node *tree_sitter.Node, src []byte, lang *LangConfig) string {
	// Try the configured name field first
	nameNode := node.ChildByFieldName(lang.NameField)
	if nameNode != nil {
		return string(src[nameNode.StartByte():nameNode.EndByte()])
	}

	// C/C++: function_definition uses declarator field → function_declarator → identifier
	if decl := node.ChildByFieldName("declarator"); decl != nil {
		// Direct identifier or type_identifier (e.g., variable declarator, C typedef)
		if decl.Kind() == "identifier" || decl.Kind() == "type_identifier" {
			return string(src[decl.StartByte():decl.EndByte()])
		}
		// function_declarator or pointer_declarator wrapping an identifier
		if id := decl.ChildByFieldName("declarator"); id != nil {
			if id.Kind() == "identifier" || id.Kind() == "field_identifier" {
				return string(src[id.StartByte():id.EndByte()])
			}
			// One more level for pointer_declarator → function_declarator → identifier
			if id2 := id.ChildByFieldName("declarator"); id2 != nil && (id2.Kind() == "identifier" || id2.Kind() == "field_identifier") {
				return string(src[id2.StartByte():id2.EndByte()])
			}
		}
		// Qualified identifier (C++ namespace::func)
		if n := decl.ChildByFieldName("name"); n != nil {
			return string(src[n.StartByte():n.EndByte()])
		}
	}

	// Template declarations (C++): name is on the inner definition
	if node.Kind() == "template_declaration" {
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(uint(i))
			if child == nil {
				continue
			}
			switch child.Kind() {
			case "function_definition", "class_specifier", "struct_specifier":
				if name := extractName(child, src, lang); name != "" {
					return name
				}
			}
		}
	}

	// For variable_declarator patterns (e.g., const foo = () => {})
	// Also matches type_identifier for C type_definition (typedef struct { ... } Name;)
	for i := 0; i < int(node.ChildCount()); i++ {
		child := node.Child(uint(i))
		if child != nil && (child.Kind() == "identifier" || child.Kind() == "type_identifier") {
			return string(src[child.StartByte():child.EndByte()])
		}
	}

	return ""
}

func normalizeType(nodeType string) string {
	// Exact matches first to avoid substring false positives
	switch nodeType {
	case "namespace_definition":
		return "namespace"
	case "template_declaration":
		return "template"
	case "trait_declaration":
		return "trait"
	case "test_declaration":
		return "test"
	case "property_declaration":
		return "property"
	}
	// Substring-based fallbacks for the common patterns
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
	case strings.Contains(nodeType, "var"):
		return "variable"
	case strings.Contains(nodeType, "module"):
		return "module"
	default:
		return nodeType
	}
}

// FindIdentifierOccurrences finds all identifier nodes matching symbolName across
// indexed files using tree-sitter. Returns exact identifier byte ranges suitable
// for rename operations. When semantic refs are available, only scans files that
// import or define the symbol (filtering out unrelated identifiers with the same name).
func FindIdentifierOccurrences(ctx context.Context, db *DB, symbolName string) ([]SymbolInfo, error) {
	// Always check for ambiguity first, regardless of refs table state.
	sym, err := db.ResolveSymbol(ctx, symbolName)
	if err != nil {
		var ambErr *AmbiguousSymbolError
		if errors.As(err, &ambErr) {
			// For languages without import extraction (C/C++), "ambiguity"
			// usually means declaration + definition of the same symbol
			// across .h/.c files. Use text-based search to rename all.
			if allCandidatesLackImports(ambErr.Candidates) {
				return findReferencesTextBased(ctx, db, symbolName)
			}
			return nil, err
		}
		// Not found → fall back to text-based scan.
		return findReferencesTextBased(ctx, db, symbolName)
	}

	// Use text-based fallback when:
	// 1. No semantic refs exist in the DB at all, OR
	// 2. The symbol is defined in a language without import extraction (e.g. C/C++),
	//    since semantic callers will miss all call sites in those languages.
	lang := GetLangConfig(sym.File)
	if !db.HasRefs(ctx) || (lang != nil && lang.Imports == nil) {
		return findReferencesTextBased(ctx, db, symbolName)
	}

	// Build allowed byte ranges per file using semantic callers.
	// This prevents renaming shadowed locals or unrelated same-name identifiers.
	allowedRanges := make(map[string][][2]uint32)

	// The definition symbol itself — always include its full byte range.
	allowedRanges[sym.File] = append(allowedRanges[sym.File],
		[2]uint32{sym.StartByte, sym.EndByte})

	// Get all symbols that semantically reference the target (import-filtered).
	callers, err := db.FindSemanticCallers(ctx, symbolName, sym.File)
	if err == nil {
		for _, c := range callers {
			allowedRanges[c.File] = append(allowedRanges[c.File],
				[2]uint32{c.StartByte, c.EndByte})
		}
	}

	// Also include same-file references from the refs table.
	// FindSemanticCallers skips the symbol itself; we need other symbols in
	// the definition file that reference this name (e.g., init(), tests).
	sameFileRefs, _ := db.FindSameFileCallers(ctx, symbolName, sym.File)
	for _, r := range sameFileRefs {
		allowedRanges[r.File] = append(allowedRanges[r.File],
			[2]uint32{r.StartByte, r.EndByte})
	}

	// Scan each file but only keep refs within allowed symbol byte ranges.
	var refs []SymbolInfo
	for file, ranges := range allowedRanges {
		lang := GetLangConfig(file)
		if lang == nil {
			continue
		}
		src, err := CachedReadFile(ctx, file)
		if err != nil {
			continue
		}
		var allRefs []SymbolInfo
		cachedParseWith(lang, src, func(root *tree_sitter.Node) {
			findIdentifierRefs(root, src, file, symbolName, &allRefs)
		})
		// Filter to only refs within allowed symbol byte ranges.
		for _, ref := range allRefs {
			for _, r := range ranges {
				if ref.StartByte >= r[0] && ref.EndByte <= r[1] {
					refs = append(refs, ref)
					break
				}
			}
		}
	}

	return refs, nil
}

// findReferencesTextBased is the legacy text-based reference search.
// allCandidatesLackImports returns true if every candidate symbol is in a
// language without import extraction (e.g. C, C++, Rust, Java).
func allCandidatesLackImports(candidates []SymbolInfo) bool {
	for _, c := range candidates {
		lang := GetLangConfig(c.File)
		if lang != nil && lang.Imports != nil {
			return false
		}
	}
	return len(candidates) > 0
}

func findReferencesTextBased(ctx context.Context, db *DB, symbolName string) ([]SymbolInfo, error) {
	// Query the files table (all indexed files) rather than DISTINCT file FROM symbols,
	// so we also scan files that have no extracted symbols (e.g., headers with only prototypes).
	rows, err := db.db.QueryContext(ctx, `SELECT path FROM files`)
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

		src, err := CachedReadFile(ctx, file)
		if err != nil {
			continue
		}

		cachedParseWith(lang, src, func(root *tree_sitter.Node) {
			findIdentifierRefs(root, src, file, symbolName, &refs)
		})
	}

	return refs, nil
}

// FindReferencesInFile searches for references to a symbol name, where the symbol
// is defined in the given file. Uses semantic filtering.
func FindReferencesInFile(ctx context.Context, db *DB, symbolName, symbolFile string) ([]SymbolInfo, error) {
	if db.HasRefs(ctx) {
		results, err := db.FindSemanticReferences(ctx, symbolName, symbolFile)
		if err == nil && len(results) > 0 {
			return results, nil
		}
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
			results, err := db.FindSemanticDeps(ctx, symID, sym.File)
			if err == nil && len(results) > 0 {
				return results, nil
			}
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

	var idents []string
	seen := make(map[string]bool)
	cachedParseWith(lang, src, func(root *tree_sitter.Node) {
		collectIdentifiers(root, src, sym.StartByte, sym.EndByte, seen, &idents)
	})

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

// builtinNames contains Go builtins and common names that should not be treated as dependencies.
var builtinNames = map[string]bool{
	// Go builtin types
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true, "uint": true, "uint8": true,
	"uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"any": true,
	// Go builtin functions
	"append": true, "cap": true, "close": true, "complex": true,
	"copy": true, "delete": true, "imag": true, "len": true,
	"make": true, "new": true, "panic": true, "print": true,
	"println": true, "real": true, "recover": true,
	// Common identifiers that are never dependencies
	"nil": true, "true": true, "false": true, "iota": true,
	"err": true, "ok": true, "ctx": true, "_": true,
}

// isDeclarationName returns true if the given identifier node is the name being
// declared (left side of :=, var/const spec name, parameter name, etc.).
func isDeclarationName(node *tree_sitter.Node) bool {
	parent := node.Parent()
	if parent == nil {
		return false
	}
	kind := parent.Kind()

	// For short_var_declaration, assignment_statement, range_clause:
	// the identifier is typically inside an expression_list child.
	// Check both direct parent and grandparent.
	ancestor := parent
	ancestorKind := kind
	if kind == "expression_list" {
		gp := parent.Parent()
		if gp != nil {
			ancestor = gp
			ancestorKind = gp.Kind()
		}
	}

	switch ancestorKind {
	case "short_var_declaration", "assignment_statement", "range_clause":
		// Left side of := or = or range — check if this node is within the left subtree
		left := ancestor.ChildByFieldName("left")
		if left != nil {
			nb := uint32(node.StartByte())
			ne := uint32(node.EndByte())
			lb := uint32(left.StartByte())
			le := uint32(left.EndByte())
			if nb >= lb && ne <= le {
				return true
			}
		}
	case "parameter_declaration", "variadic_parameter_declaration":
		// Function parameters — the name is typically the first child
		if ancestor.ChildCount() > 1 {
			first := ancestor.Child(0)
			if first != nil && uint32(first.StartByte()) == uint32(node.StartByte()) {
				return true
			}
		}
	case "var_spec", "const_spec":
		// var x int = ... — name is the "name" field
		nameNode := ancestor.ChildByFieldName("name")
		if nameNode != nil {
			nb := uint32(node.StartByte())
			ne := uint32(node.EndByte())
			nnb := uint32(nameNode.StartByte())
			nne := uint32(nameNode.EndByte())
			if nb >= nnb && ne <= nne {
				return true
			}
		}
	}
	return false
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
			if !seen[text] && !builtinNames[text] && !isDeclarationName(node) {
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

	// Pass 1: Collect all locally declared names (parameters, locals, etc.)
	localNames := make(map[string]bool)
	var collectLocals func(node *tree_sitter.Node)
	collectLocals = func(node *tree_sitter.Node) {
		nb := uint32(node.StartByte())
		ne := uint32(node.EndByte())
		if ne <= sym.StartByte || nb >= sym.EndByte {
			return
		}
		kind := node.Kind()
		if kind == "identifier" && nb >= sym.StartByte && ne <= sym.EndByte {
			if isDeclarationName(node) {
				text := string(src[nb:ne])
				localNames[text] = true
			}
		}
		for i := 0; i < int(node.ChildCount()); i++ {
			child := node.Child(uint(i))
			if child != nil {
				collectLocals(child)
			}
		}
	}
	collectLocals(root)

	// Pass 2: Collect refs, skipping locals, builtins, and the symbol's own name
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
				// Skip own name, short identifiers, builtins, and local variable names
				if text == declName || len(text) <= 1 || builtinNames[text] || localNames[text] {
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
