package index

import (
	"path/filepath"
	"strings"
)

// Language configuration for the symbol index. This replaces the
// regex-based language detection — all symbol extraction now goes
// through hand-written parsers in parse_*.go.

// ContainerStyle describes how a language delimits container bodies.
type ContainerStyle int

const (
	ContainerBrace   ContainerStyle = iota // { }
	ContainerIndent                        // indentation (Python)
	ContainerKeyword                       // keyword-terminated (Ruby: end)
)

type langConfig struct {
	id             string
	container      ContainerStyle
	containerClose string
	methodsOutside bool
}

var langByExt = map[string]*langConfig{
	".go":    {id: "go", container: ContainerBrace, containerClose: "}", methodsOutside: true},
	".py":    {id: "python", container: ContainerIndent},
	".pyi":   {id: "python", container: ContainerIndent},
	".js":    {id: "javascript", container: ContainerBrace, containerClose: "}"},
	".jsx":   {id: "javascript", container: ContainerBrace, containerClose: "}"},
	".ts":    {id: "typescript", container: ContainerBrace, containerClose: "}"},
	".tsx":   {id: "typescript", container: ContainerBrace, containerClose: "}"},
	".mts":   {id: "typescript", container: ContainerBrace, containerClose: "}"},
	".cts":   {id: "typescript", container: ContainerBrace, containerClose: "}"},
	".rs":    {id: "rust", container: ContainerBrace, containerClose: "}"},
	".java":  {id: "java", container: ContainerBrace, containerClose: "}"},
	".rb":    {id: "ruby", container: ContainerKeyword, containerClose: "end"},
	".c":     {id: "c", container: ContainerBrace, containerClose: "}"},
	".h":     {id: "c", container: ContainerBrace, containerClose: "}"},
	".cpp":   {id: "cpp", container: ContainerBrace, containerClose: "}"},
	".cc":    {id: "cpp", container: ContainerBrace, containerClose: "}"},
	".hpp":   {id: "cpp", container: ContainerBrace, containerClose: "}"},
	".cxx":   {id: "cpp", container: ContainerBrace, containerClose: "}"},
	".hxx":   {id: "cpp", container: ContainerBrace, containerClose: "}"},
	".hh":    {id: "cpp", container: ContainerBrace, containerClose: "}"},
	".cs":    {id: "csharp", container: ContainerBrace, containerClose: "}"},
	".kt":    {id: "kotlin", container: ContainerBrace, containerClose: "}"},
	".kts":   {id: "kotlin", container: ContainerBrace, containerClose: "}"},
	".swift": {id: "swift", container: ContainerBrace, containerClose: "}"},
	".php":   {id: "php", container: ContainerBrace, containerClose: "}"},
	".scala": {id: "scala", container: ContainerBrace, containerClose: "}"},
	".sc":    {id: "scala", container: ContainerBrace, containerClose: "}"},
}

func langForFile(path string) *langConfig {
	ext := strings.ToLower(filepath.Ext(path))
	return langByExt[ext]
}

// Supported returns true if the file extension has a hand-written parser.
func Supported(path string) bool {
	return langForFile(path) != nil
}

// LangMethodsOutside returns true if methods live outside the type (Go).
func LangMethodsOutside(path string) bool {
	lang := langForFile(path)
	return lang != nil && lang.methodsOutside
}

// LangContainer returns the container style for a file path.
func LangContainer(path string) ContainerStyle {
	lang := langForFile(path)
	if lang == nil {
		return ContainerBrace
	}
	return lang.container
}

// LangContainerClose returns the closing delimiter for a file's container style.
func LangContainerClose(path string) string {
	lang := langForFile(path)
	if lang == nil {
		return "}"
	}
	return lang.containerClose
}

// Parse dispatches to the appropriate hand-written parser based on file
// extension and returns []SymbolInfo. This is the unified entry point
// that replaces RegexParse for all callers outside of parseFile.
func Parse(path string, src []byte) []SymbolInfo {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".rb":
		return rubyToSymbolInfo(path, src, ParseRuby(src))
	case ".js", ".jsx", ".ts", ".tsx", ".mts", ".cts":
		return tsToSymbolInfo(path, src, ParseTS(src))
	case ".go":
		return goToSymbolInfo(path, src, ParseGo(src))
	case ".py", ".pyi":
		return pythonToSymbolInfo(path, src, ParsePython(src))
	case ".java":
		return javaToSymbolInfo(path, src, ParseJava(src))
	case ".cs":
		return csharpToSymbolInfo(path, src, ParseCSharp(src))
	case ".rs":
		return rustToSymbolInfo(path, src, ParseRust(src))
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hxx", ".hh":
		return cppToSymbolInfo(path, src, ParseCpp(src))
	case ".kt", ".kts":
		return kotlinToSymbolInfo(path, src, ParseKotlin(src))
	case ".swift":
		return swiftToSymbolInfo(path, src, ParseSwift(src))
	case ".php":
		return phpToSymbolInfo(path, src, ParsePHP(src))
	case ".scala", ".sc":
		return scalaToSymbolInfo(path, src, ParseScala(src))
	default:
		return nil
	}
}

// LangID returns the language identifier for a file path, or "".
func LangID(path string) string {
	lang := langForFile(path)
	if lang == nil {
		return ""
	}
	return lang.id
}
