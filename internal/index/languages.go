package index

import (
	"path/filepath"
	"strings"
	"unsafe"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	bash_lang "github.com/jordw/edr/internal/grammars/bash"
	"github.com/jordw/edr/internal/grammars/c_lang"
	"github.com/jordw/edr/internal/grammars/cpp"
	"github.com/jordw/edr/internal/grammars/csharp"
	"github.com/jordw/edr/internal/grammars/go_lang"
	"github.com/jordw/edr/internal/grammars/java"
	"github.com/jordw/edr/internal/grammars/javascript"
	"github.com/jordw/edr/internal/grammars/kotlin"
	"github.com/jordw/edr/internal/grammars/lua"
	"github.com/jordw/edr/internal/grammars/php"
	"github.com/jordw/edr/internal/grammars/python"
	"github.com/jordw/edr/internal/grammars/ruby"
	"github.com/jordw/edr/internal/grammars/rust"
	"github.com/jordw/edr/internal/grammars/typescript"
	"github.com/jordw/edr/internal/grammars/zig"
)

// ImportNodeConfig describes how to extract imports from tree-sitter AST.
type ImportNodeConfig struct {
	// TopLevel is the import declaration node type (e.g., "import_declaration").
	TopLevel []string
	// SpecNode is the individual import spec within a group (e.g., "import_spec").
	SpecNode string
	// PathField is the field name for the import path (e.g., "path").
	PathField string
	// AliasField is the field name for the import alias (e.g., "name").
	AliasField string
}

// LangConfig holds the tree-sitter language and the AST node types that
// represent symbols worth indexing for a given programming language.
// ContainerStyle describes how a language delimits container bodies (classes, structs, etc.).
type ContainerStyle int

const (
	// ContainerBrace uses { } delimiters (Go, JS/TS, C, C++, Rust, Java, PHP, Zig).
	ContainerBrace ContainerStyle = iota
	// ContainerIndent uses indentation to define scope (Python).
	ContainerIndent
	// ContainerKeyword uses a closing keyword like "end" (Ruby, Lua).
	ContainerKeyword
)

type LangConfig struct {
	Language       *tree_sitter.Language
	SymbolNodes    []string
	NameField      string
	Imports        *ImportNodeConfig
	LangID         string         // "go", "python", "javascript", "typescript", etc.
	Container      ContainerStyle // how containers are delimited
	ContainerClose string         // closing token: "}", "end", "" (indent-based)
	MethodsOutside bool           // true if methods live outside the struct (Go)
}

// GetLangConfig returns the language configuration for the given filename
// based on its extension. It returns nil if the extension is not recognized.
func GetLangConfig(filename string) *LangConfig {
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".go":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(go_lang.Language())),
			SymbolNodes: []string{
				"function_declaration",
				"method_declaration",
				"type_declaration",
				"type_spec",
				"var_spec",
			},
			NameField:      "name",
			LangID:         "go",
			Container:      ContainerBrace,
			ContainerClose: "}",
			MethodsOutside: true,
			Imports: &ImportNodeConfig{
				TopLevel:   []string{"import_declaration"},
				SpecNode:   "import_spec",
				PathField:  "path",
				AliasField: "name",
			},
		}
	case ".py":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(python.Language())),
			SymbolNodes: []string{
				"function_definition",
				"class_definition",
			},
			NameField:      "name",
			LangID:         "python",
			Container:      ContainerIndent,
			ContainerClose: "",
			Imports: &ImportNodeConfig{
				TopLevel: []string{"import_statement", "import_from_statement"},
			},
		}
	case ".js", ".jsx":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(javascript.Language())),
			SymbolNodes: []string{
				"function_declaration",
				"class_declaration",
				"method_definition",
				"arrow_function",
			},
			NameField:      "name",
			LangID:         "javascript",
			Container:      ContainerBrace,
			ContainerClose: "}",
			Imports: &ImportNodeConfig{
				TopLevel:  []string{"import_statement"},
				PathField: "source",
			},
		}
	case ".c", ".h":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(c_lang.Language())),
			SymbolNodes: []string{
				"function_definition",
				"struct_specifier",
				"declaration",
				"type_definition",
			},
			NameField:      "name",
			LangID:         "c",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".rs":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(rust.Language())),
			SymbolNodes: []string{
				"function_item",
				"struct_item",
				"impl_item",
				"enum_item",
			},
			NameField:      "name",
			LangID:         "rust",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".java":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(java.Language())),
			SymbolNodes: []string{
				"method_declaration",
				"class_declaration",
				"interface_declaration",
			},
			NameField:      "name",
			LangID:         "java",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".ts":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(typescript.LanguageTypescript())),
			SymbolNodes: []string{
				"function_declaration",
				"class_declaration",
				"method_definition",
				"arrow_function",
				"interface_declaration",
				"enum_declaration",
				"type_alias_declaration",
			},
			NameField:      "name",
			LangID:         "typescript",
			Container:      ContainerBrace,
			ContainerClose: "}",
			Imports: &ImportNodeConfig{
				TopLevel:  []string{"import_statement"},
				PathField: "source",
			},
		}
	case ".tsx":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(typescript.LanguageTSX())),
			SymbolNodes: []string{
				"function_declaration",
				"class_declaration",
				"method_definition",
				"arrow_function",
				"interface_declaration",
				"enum_declaration",
				"type_alias_declaration",
			},
			NameField:      "name",
			LangID:         "typescript",
			Container:      ContainerBrace,
			ContainerClose: "}",
			Imports: &ImportNodeConfig{
				TopLevel:  []string{"import_statement"},
				PathField: "source",
			},
		}
	case ".rb":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(ruby.Language())),
			SymbolNodes: []string{
				"method",
				"class",
				"module",
			},
			NameField:      "name",
			LangID:         "ruby",
			Container:      ContainerKeyword,
			ContainerClose: "end",
		}
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(cpp.Language())),
			SymbolNodes: []string{
				"function_definition",
				"class_specifier",
				"struct_specifier",
				"enum_specifier",
				"namespace_definition",
				"template_declaration",
				"declaration",
				"type_definition",
			},
			NameField:      "name",
			LangID:         "cpp",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".php":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(php.Language())),
			SymbolNodes: []string{
				"function_definition",
				"method_declaration",
				"class_declaration",
				"interface_declaration",
				"trait_declaration",
				"enum_declaration",
			},
			NameField:      "name",
			LangID:         "php",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".zig":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(zig.Language())),
			SymbolNodes: []string{
				"function_declaration",
				"test_declaration",
				"variable_declaration",
				"struct_declaration",
				"enum_declaration",
				"union_declaration",
			},
			NameField:      "name",
			LangID:         "zig",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".lua":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(lua.Language())),
			SymbolNodes: []string{
				"function_declaration",
				"function_definition",
				"local_function",
			},
			NameField:      "name",
			LangID:         "lua",
			Container:      ContainerKeyword,
			ContainerClose: "end",
		}
	case ".sh", ".bash":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(bash_lang.Language())),
			SymbolNodes: []string{
				"function_definition",
			},
			NameField:      "name",
			LangID:         "bash",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".cs":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(csharp.Language())),
			SymbolNodes: []string{
				"class_declaration",
				"struct_declaration",
				"interface_declaration",
				"enum_declaration",
				"record_declaration",
				"method_declaration",
				"constructor_declaration",
				"namespace_declaration",
			},
			NameField:      "name",
			LangID:         "csharp",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".kt", ".kts":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(kotlin.Language())),
			SymbolNodes: []string{
				"class_declaration",
				"function_declaration",
				"object_declaration",
				"property_declaration",
			},
			NameField:      "name",
			LangID:         "kotlin",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	default:
		return nil
	}
}
