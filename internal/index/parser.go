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
func FindReferences(ctx context.Context, db *DB, symbolName string) ([]SymbolInfo, error) {
	// Get all files from index
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

	// Search for references in each file using tree-sitter
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

// FindDeps finds symbols that the given symbol depends on (identifiers within
// the symbol's body that match known symbols in the index).
func FindDeps(ctx context.Context, db *DB, sym *SymbolInfo) ([]SymbolInfo, error) {
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

	// Collect all identifiers within the symbol's byte range
	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(tree.RootNode(), src, sym.StartByte, sym.EndByte, seen, &idents)

	// Look up each identifier in the index
	var deps []SymbolInfo
	depSeen := make(map[string]bool)
	for _, name := range idents {
		if name == sym.Name {
			continue // skip self-references
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
