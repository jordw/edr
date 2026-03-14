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
	"github.com/jordw/edr/internal/grammars/css"
	"github.com/jordw/edr/internal/grammars/dockerfile"
	"github.com/jordw/edr/internal/grammars/elixir"
	"github.com/jordw/edr/internal/grammars/go_lang"
	"github.com/jordw/edr/internal/grammars/hcl"
	"github.com/jordw/edr/internal/grammars/html"
	"github.com/jordw/edr/internal/grammars/java"
	"github.com/jordw/edr/internal/grammars/javascript"
	"github.com/jordw/edr/internal/grammars/json_lang"
	"github.com/jordw/edr/internal/grammars/kotlin"
	"github.com/jordw/edr/internal/grammars/lua"
	"github.com/jordw/edr/internal/grammars/markdown"
	"github.com/jordw/edr/internal/grammars/php"
	"github.com/jordw/edr/internal/grammars/proto"
	"github.com/jordw/edr/internal/grammars/python"
	"github.com/jordw/edr/internal/grammars/ruby"
	"github.com/jordw/edr/internal/grammars/rust"
	"github.com/jordw/edr/internal/grammars/scala"
	"github.com/jordw/edr/internal/grammars/sql"
	"github.com/jordw/edr/internal/grammars/toml"
	"github.com/jordw/edr/internal/grammars/typescript"
	"github.com/jordw/edr/internal/grammars/yaml"
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
	case ".scala", ".sc":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(scala.Language())),
			SymbolNodes: []string{
				"class_definition",
				"object_definition",
				"trait_definition",
				"function_definition",
				"function_declaration",
				"val_definition",
				"type_definition",
				"enum_definition",
			},
			NameField:      "name",
			LangID:         "scala",
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
	case ".ex", ".exs":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(elixir.Language())),
			SymbolNodes: []string{
				"call",
			},
			NameField:      "target",
			LangID:         "elixir",
			Container:      ContainerKeyword,
			ContainerClose: "end",
		}
	case ".html", ".htm":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(html.Language())),
			SymbolNodes: []string{
				"element",
				"script_element",
				"style_element",
			},
			NameField: "name",
			LangID:    "html",
		}
	case ".css":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(css.Language())),
			SymbolNodes: []string{
				"rule_set",
				"media_statement",
				"keyframes_statement",
			},
			NameField:      "name",
			LangID:         "css",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".json":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(json_lang.Language())),
			SymbolNodes: []string{
				"pair",
			},
			NameField: "key",
			LangID:    "json",
		}
	case ".yaml", ".yml":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(yaml.Language())),
			SymbolNodes: []string{
				"block_mapping_pair",
			},
			NameField:      "key",
			LangID:         "yaml",
			Container:      ContainerIndent,
			ContainerClose: "",
		}
	case ".toml":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(toml.Language())),
			SymbolNodes: []string{
				"table",
				"table_array_element",
			},
			NameField: "name",
			LangID:    "toml",
		}
	case ".md", ".markdown":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(markdown.Language())),
			SymbolNodes: []string{
				"atx_heading",
				"setext_heading",
			},
			NameField: "heading_content",
			LangID:    "markdown",
		}
	case ".proto":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(proto.Language())),
			SymbolNodes: []string{
				"message",
				"service",
				"rpc",
				"enum",
			},
			NameField:      "name",
			LangID:         "protobuf",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".sql":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(sql.Language())),
			SymbolNodes: []string{
				"create_table_stmt",
				"create_view_stmt",
				"create_index_stmt",
				"create_trigger_stmt",
			},
			NameField: "name",
			LangID:    "sql",
		}
	case ".hcl", ".tf", ".tfvars":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(hcl.Language())),
			SymbolNodes: []string{
				"block",
				"attribute",
			},
			NameField:      "name",
			LangID:         "hcl",
			Container:      ContainerBrace,
			ContainerClose: "}",
		}
	case ".dockerfile":
		return &LangConfig{
			Language: tree_sitter.NewLanguage(unsafe.Pointer(dockerfile.Language())),
			SymbolNodes: []string{
				"from_instruction",
				"run_instruction",
				"copy_instruction",
				"env_instruction",
				"arg_instruction",
			},
			NameField: "name",
			LangID:    "dockerfile",
		}
	default:
		// Handle Dockerfile (no extension)
		base := strings.ToLower(filepath.Base(filename))
		if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
			return &LangConfig{
				Language: tree_sitter.NewLanguage(unsafe.Pointer(dockerfile.Language())),
				SymbolNodes: []string{
					"from_instruction",
					"run_instruction",
					"copy_instruction",
					"env_instruction",
					"arg_instruction",
				},
				NameField: "name",
				LangID:    "dockerfile",
			}
		}
		return nil
	}
}
