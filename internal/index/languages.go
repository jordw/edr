package index

import (
	"path/filepath"
	"strings"
	"unsafe"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tree_sitter_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

// LangConfig holds the tree-sitter language and the AST node types that
// represent symbols worth indexing for a given programming language.
type LangConfig struct {
	Language    *tree_sitter.Language
	SymbolNodes []string
	NameField   string
}

// GetLangConfig returns the language configuration for the given filename
// based on its extension. It returns nil if the extension is not recognized.
func GetLangConfig(filename string) *LangConfig {
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".go":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_go.Language())),
			SymbolNodes: []string{
				"function_declaration",
				"method_declaration",
				"type_declaration",
				"type_spec",
			},
			NameField: "name",
		}
	case ".py":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_python.Language())),
			SymbolNodes: []string{
				"function_definition",
				"class_definition",
			},
			NameField: "name",
		}
	case ".js", ".jsx":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_javascript.Language())),
			SymbolNodes: []string{
				"function_declaration",
				"class_declaration",
				"method_definition",
				"arrow_function",
			},
			NameField: "name",
		}
	case ".c", ".h":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_c.Language())),
			SymbolNodes: []string{
				"function_definition",
				"struct_specifier",
			},
			NameField: "name",
		}
	case ".rs":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_rust.Language())),
			SymbolNodes: []string{
				"function_item",
				"struct_item",
				"impl_item",
				"enum_item",
			},
			NameField: "name",
		}
	case ".java":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_java.Language())),
			SymbolNodes: []string{
				"method_declaration",
				"class_declaration",
				"interface_declaration",
			},
			NameField: "name",
		}
	case ".rb":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(tree_sitter_ruby.Language())),
			SymbolNodes: []string{
				"method",
				"class",
				"module",
			},
			NameField: "name",
		}
	default:
		return nil
	}
}
