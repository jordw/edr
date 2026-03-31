// Regex-based symbol extraction. Pure Go, no CGO, no external dependencies.
// Replaces tree-sitter for symbol detection and boundary finding.
package index

import (
	"path/filepath"
	"regexp"
	"strings"
)

// ContainerStyle describes how a language delimits container bodies.
type ContainerStyle int

const (
	ContainerBrace   ContainerStyle = iota // { } (Go, JS/TS, C, Rust, Java)
	ContainerIndent                        // indentation (Python)
	ContainerKeyword                       // closing keyword like "end" (Ruby, Lua)
)

// regexLang defines patterns and end-detection style for a language.
type regexLang struct {
	patterns       []regexPattern
	endStyle       regexEndStyle
	container      ContainerStyle
	containerClose string // "}", "end", ""
	methodsOutside bool   // true if methods live outside the struct (Go)
	langID         string
}

type regexEndStyle int

const (
	regexBraceEnd  regexEndStyle = iota
	regexIndentEnd
)

type regexPattern struct {
	re      *regexp.Regexp
	typ     string
	nameIdx int
	prefix  string // optional: if non-empty, line must contain this before trying regex
}

// ID is the unicode-aware identifier pattern: letters, digits, underscore.
// Used in regex patterns via re() which replaces {ID} before compiling.
const identifierPattern = `[\p{L}\p{N}_]`

// re compiles a regex pattern, replacing {ID} with the unicode identifier class.
// This keeps patterns readable: `^func\s+({ID}+)` instead of `^func\s+([\p{L}\p{N}_]+)`.
func re(pattern string) *regexp.Regexp {
	expanded := strings.ReplaceAll(pattern, "{ID}", identifierPattern)
	return regexp.MustCompile(expanded)
}

// p creates a regexPattern with an optional prefix hint for fast-path filtering.
func p(pattern, typ string, nameIdx int, prefix ...string) regexPattern {
	rp := regexPattern{re: re(pattern), typ: typ, nameIdx: nameIdx}
	if len(prefix) > 0 {
		rp.prefix = prefix[0]
	}
	return rp
}

// --- Language definitions ---
//
// Each language defines: patterns to match symbol declarations, end-detection
// style (brace counting vs indentation), and container metadata.
//
// Pattern syntax: {ID} expands to a unicode identifier character class.
// Patterns are tried in order; first match wins per line.

var regexGo = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", methodsOutside: true, langID: "go",
	patterns: []regexPattern{
		p(`^func\s+\(({ID}+)\s+\*?({ID}+)\)\s+({ID}+)\s*\(`, "method", 3, "func"),
		p(`^func\s+({ID}+)\s*[\(\[]`, "function", 1, "func"),
		p(`^type\s+({ID}+)\s+struct\s*\{`, "struct", 1, "type"),
		p(`^type\s+({ID}+)\s+interface\s*\{`, "interface", 1, "type"),
		p(`^type\s+({ID}+)\s+`, "type", 1, "type"),
		p(`^var\s+({ID}+)\s+`, "variable", 1, "var"),
	},
}

var regexPython = &regexLang{
	endStyle: regexIndentEnd, container: ContainerIndent, langID: "python",
	patterns: []regexPattern{
		p(`^(\s*)class\s+({ID}+)`, "class", 2, "class"),
		p(`^(\s*)(?:async\s+)?def\s+({ID}+)\s*\(`, "function", 2, "def"),
	},
}

var jsKeywords = map[string]bool{
	"if": true, "else": true, "for": true, "while": true, "do": true,
	"switch": true, "case": true, "break": true, "continue": true,
	"return": true, "throw": true, "try": true, "catch": true, "finally": true,
	"typeof": true, "void": true, "in": true,
	"instanceof": true, "with": true,
	"import": true, "export": true, "from": true,
}

var regexTypeScript = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "typescript",
	patterns: []regexPattern{
		p(`^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+({ID}+)`, "class", 1, "class"),
		p(`^(?:export\s+)?interface\s+({ID}+)`, "interface", 1, "interface"),
		p(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s+({ID}+)`, "function", 1, "function"),
		p(`^(?:export\s+)?type\s+({ID}+)\s*(?:<[^>]*>)?\s*=`, "type", 1, "type"),
		p(`^(?:export\s+)?(?:const\s+)?enum\s+({ID}+)`, "type", 1, "enum"),
		p(`^(?:export\s+)?(?:declare\s+)?namespace\s+({ID}+)`, "class", 1, "namespace"),
		// Methods with modifier keywords
		p(`^\s+(?:private|protected|public|static|abstract|override|readonly|async|get|set)\s+(?:(?:private|protected|public|static|abstract|override|readonly|async|get|set)\s+)*#?({ID}+)\s*(?:<[^>]*>)?\s*[\(:]`, "method", 1),
		p(`^\s+(constructor)\s*\(`, "method", 1),
		// Unmodified methods — require { at end of line
		p(`^\s+#?({ID}+)\s*(?:<[^>]*>)?\s*\(.*\)\s*(?::\s*[^{]*)?\{`, "method", 1),
		// Arrow functions
		p(`^(?:export\s+)?(?:const|let|var)\s+({ID}+)\s*(?::\s*[^=]+)?\s*=\s*(?:async\s+)?(?:function|<[^>]*>\s*\(|\(|[a-zA-Z_]{ID}*\s*=>)`, "function", 1),
		// Static arrow methods
		p(`^\s+(?:static\s+)?({ID}+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_]{ID}*)\s*(?::\s*[^=]+)?\s*=>`, "method", 1),
	},
}

var regexRust = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "rust",
	patterns: []regexPattern{
		p(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+({ID}+)`, "function", 1),
		p(`^(?:pub(?:\([^)]*\))?\s+)?struct\s+({ID}+)`, "struct", 1),
		p(`^(?:pub(?:\([^)]*\))?\s+)?enum\s+({ID}+)`, "type", 1),
		p(`^(?:pub(?:\([^)]*\))?\s+)?trait\s+({ID}+)`, "interface", 1),
		p(`^impl(?:<[^>]*>)?\s+(?:[\p{L}\p{N}_:]+\s+for\s+)?({ID}+)`, "impl", 1),
	},
}

var regexJava = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "java",
	patterns: []regexPattern{
		p(`^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:abstract\s+)?(?:class|interface|enum)\s+({ID}+)`, "class", 1),
		// Constructors: visibility + UpperCaseName(  — no return type
		p(`^\s*(?:public|private|protected)\s+([A-Z]{ID}*)\s*\(`, "method", 1),
		// Methods: have a return type before the name
		p(`^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:abstract\s+)?(?:synchronized\s+)?(?:{ID}+(?:<.*>)?(?:\[\])*)\s+({ID}+)\s*\(`, "method", 1),
	},
}

var regexRuby = &regexLang{
	endStyle: regexIndentEnd, container: ContainerKeyword, containerClose: "end", langID: "ruby",
	patterns: []regexPattern{
		p(`^(\s*)class\s+({ID}+)`, "class", 2),
		p(`^(\s*)module\s+({ID}+)`, "class", 2),
		p(`^(\s*)def\s+({ID}+[?!=]?)`, "function", 2),
	},
}

var regexC = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "c",
	patterns: []regexPattern{
		p(`^(?:static\s+)?(?:inline\s+)?(?:const\s+)?(?:unsigned\s+)?(?:struct\s+)?(?:{ID}+(?:\s*\*)*)\s+({ID}+)\s*\([^;]*$`, "function", 1),
		p(`^(?:typedef\s+)?struct\s+({ID}+)\s*\{`, "struct", 1),
		p(`^(?:typedef\s+)?enum\s+({ID}+)\s*\{`, "type", 1),
	},
}

var regexCSharp = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "csharp",
	patterns: []regexPattern{
		// class, struct, interface, enum, record
		p(`^\s*(?:public|private|protected|internal)?\s*(?:static\s+)?(?:abstract\s+)?(?:sealed\s+)?(?:partial\s+)?(?:class|struct|interface|enum|record)\s+({ID}+)`, "class", 1),
		// namespace
		p(`^\s*namespace\s+([\p{L}\p{N}_.]+)`, "class", 1),
		// Constructors: visibility + UpperCaseName(
		p(`^\s*(?:public|private|protected|internal)\s+([A-Z]{ID}*)\s*\(`, "method", 1),
		// Methods: have a return type before the name
		p(`^\s*(?:public|private|protected|internal)?\s*(?:static\s+)?(?:abstract\s+)?(?:virtual\s+)?(?:override\s+)?(?:async\s+)?(?:{ID}+(?:<.*>)?(?:\[\]|\?)*)\s+({ID}+)\s*\(`, "method", 1),
	},
}

var regexKotlin = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "kotlin",
	patterns: []regexPattern{
		// class, interface, object, enum class, data class, sealed class
		p(`^\s*(?:public|private|protected|internal)?\s*(?:open\s+)?(?:abstract\s+)?(?:sealed\s+)?(?:data\s+)?(?:class|interface|object|enum)\s+({ID}+)`, "class", 1),
		// fun keyword — Kotlin's function declaration
		p(`^\s*(?:public|private|protected|internal)?\s*(?:open\s+)?(?:override\s+)?(?:suspend\s+)?fun\s+(?:<[^>]*>\s*)?({ID}+)\s*\(`, "function", 1),
	},
}

var regexSwift = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "swift",
	patterns: []regexPattern{
		// class, struct, enum, protocol, extension
		p(`^\s*(?:public|private|fileprivate|internal|open)?\s*(?:final\s+)?(?:class|struct|enum|protocol)\s+({ID}+)`, "class", 1),
		p(`^\s*extension\s+({ID}+)`, "impl", 1),
		// func — Swift's function/method declaration
		p(`^\s*(?:public|private|fileprivate|internal|open)?\s*(?:static\s+|class\s+)?(?:override\s+)?(?:mutating\s+)?func\s+({ID}+)`, "function", 1),
		// init
		p(`^\s*(?:public|private|fileprivate|internal|open)?\s*(?:convenience\s+)?(?:required\s+)?(init)\s*[\(]`, "method", 1),
	},
}

var regexPHP = &regexLang{
	endStyle: regexBraceEnd, container: ContainerBrace, containerClose: "}", langID: "php",
	patterns: []regexPattern{
		// class, interface, trait, enum
		p(`^\s*(?:abstract\s+)?(?:final\s+)?(?:class|interface|trait|enum)\s+({ID}+)`, "class", 1),
		// function keyword — PHP's function declaration
		p(`^\s*(?:public|private|protected)?\s*(?:static\s+)?function\s+({ID}+)\s*\(`, "function", 1),
	},
}

var regexByExt = map[string]*regexLang{
	".go":   regexGo,
	".py":   regexPython,
	".js":   regexTypeScript, ".jsx": regexTypeScript,
	".ts":   regexTypeScript, ".tsx": regexTypeScript,
	".rs":   regexRust,
	".java": regexJava,
	".rb":   regexRuby,
	".c":    regexC, ".h": regexC, ".cpp": regexC, ".cc": regexC, ".hpp": regexC, ".cxx": regexC, ".hxx": regexC, ".hh": regexC,
	".cs":    regexCSharp,
	".kt":    regexKotlin, ".kts": regexKotlin,
	".swift": regexSwift,
	".php":   regexPHP,
	// TODO: add patterns for .scala, .lua, .zig once validated.
}

// regexLangForFile returns the regex language for a file path, or nil.
func regexLangForFile(path string) *regexLang {
	ext := filepath.Ext(path)
	return regexByExt[ext]
}

// RegexSupported returns true if the file extension has regex patterns.
func RegexSupported(path string) bool {
	return regexLangForFile(path) != nil
}

// LangContainer returns the container style for a file path.
func LangContainer(path string) ContainerStyle {
	lang := regexLangForFile(path)
	if lang == nil {
		return ContainerBrace
	}
	return lang.container
}

// LangContainerClose returns the closing token for a file path ("}", "end", "").
func LangContainerClose(path string) string {
	lang := regexLangForFile(path)
	if lang == nil {
		return "}"
	}
	return lang.containerClose
}

// LangMethodsOutside returns true if methods live outside the type (Go).
func LangMethodsOutside(path string) bool {
	lang := regexLangForFile(path)
	return lang != nil && lang.methodsOutside
}

// LangID returns the language identifier for a file path, or "".
func LangID(path string) string {
	lang := regexLangForFile(path)
	if lang == nil {
		return ""
	}
	return lang.langID
}

// RegexParse extracts symbols from source code using regex patterns.
// Returns SymbolInfo structs compatible with the rest of the index package.
func RegexParse(path string, src []byte) []SymbolInfo {
	lang := regexLangForFile(path)
	if lang == nil {
		return nil
	}

	source := string(src)
	lines := strings.Split(source, "\n")

	// Precompute cumulative byte offsets per line (avoids O(n²) in symbol loop).
	lineOffsets := make([]uint32, len(lines)+1)
	for i, line := range lines {
		lineOffsets[i+1] = lineOffsets[i] + uint32(len(line)) + 1 // +1 for \n
	}

	var symbols []SymbolInfo
	isTS := lang == regexTypeScript || lang == regexJava

	for i, line := range lines {
		for _, pat := range lang.patterns {
			// Fast prefix check: skip regex if the line can't possibly match.
			if pat.prefix != "" && !strings.Contains(line, pat.prefix) {
				continue
			}
			m := pat.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[pat.nameIdx]
			if name == "" || name == "_" {
				continue
			}
			if isTS && pat.typ == "method" && jsKeywords[name] {
				continue
			}

			startLine := i + 1 // 1-based

			var endLine int
			switch lang.endStyle {
			case regexBraceEnd:
				if regexHasBrace(lines, i) {
					endLine = regexFindBraceEnd(lines, i)
				} else {
					endLine = regexFindNoBraceEnd(lines, i)
				}
			case regexIndentEnd:
				endLine = regexFindIndentEnd(lines, i)
			}
			if endLine == 0 {
				endLine = startLine
			}

			// Byte offsets from precomputed table
			startByte := lineOffsets[i]
			endByte := lineOffsets[i]
			if endLine <= len(lines) {
				endByte = lineOffsets[endLine] - 1 // -1 to exclude trailing \n
			}

			// Determine parent index for Go methods
			parentIdx := -1
			if pat.typ == "method" && lang == regexGo {
				// Go method receiver type is in capture group 2
				receiverType := m[2]
				// Find matching struct
				for j := len(symbols) - 1; j >= 0; j-- {
					if symbols[j].Name == receiverType && (symbols[j].Type == "struct" || symbols[j].Type == "type") {
						parentIdx = j
						break
					}
				}
			}

			symbols = append(symbols, SymbolInfo{
				Type:        pat.typ,
				Name:        name,
				File:        path,
				StartLine:   uint32(startLine),
				EndLine:     uint32(endLine),
				StartByte:   startByte,
				EndByte:     endByte,
				ParentIndex: parentIdx,
			})
			break // first matching pattern wins
		}
	}

	// Assign parents for indent-based languages
	if lang.endStyle == regexIndentEnd {
		regexAssignIndentParents(lines, symbols)
	}

	return symbols
}

// --- Brace/indent end-detection ---

func regexHasBrace(lines []string, lineIdx int) bool {
	// No hard cap — stop conditions (blank line, new declaration) prevent runaway.
	// Long multiline signatures (Rust where-clauses, Java generics) can push {
	// well beyond 20 lines.
	for i := lineIdx; i < len(lines); i++ {
		line := lines[i]
		if i > lineIdx {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				if !regexHasWhereClause(lines, lineIdx, i) {
					return false
				}
				continue
			}
			if regexIsNewDeclaration(trimmed) {
				return false
			}
			if len(line) > 0 && line[0] != ' ' && line[0] != '\t' &&
				line[0] != ')' && line[0] != '>' && line[0] != '{' &&
				!strings.HasPrefix(trimmed, "where") {
				return false
			}
		}
		if strings.Contains(line, "{") {
			return true
		}
	}
	return false
}

var newDeclPrefixes = []string{
	"var ", "type ", "func ", "const ", "//",
	"pub fn ", "pub struct ", "pub enum ", "pub trait ",
	"fn ", "struct ", "enum ", "trait ", "impl ", "impl<",
	"pub(", "mod ", "use ", "#[",
	"class ", "interface ", "def ", "async def ",
	"export ", "import ",
}

func regexIsNewDeclaration(trimmed string) bool {
	for _, p := range newDeclPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

func regexHasWhereClause(lines []string, start, current int) bool {
	for i := start; i < current; i++ {
		if strings.Contains(strings.TrimSpace(lines[i]), "where") {
			return true
		}
	}
	return false
}

func regexFindNoBraceEnd(lines []string, lineIdx int) int {
	line := lines[lineIdx]
	trimmed := strings.TrimSpace(line)
	if strings.HasSuffix(trimmed, "(") {
		depth := 1
		for i := lineIdx + 1; i < len(lines); i++ {
			for _, ch := range lines[i] {
				if ch == '(' {
					depth++
				} else if ch == ')' {
					depth--
					if depth == 0 {
						return i + 1
					}
				}
			}
		}
	}
	return lineIdx + 1
}

func regexFindBraceEnd(lines []string, lineIdx int) int {
	depth := 0
	inString := byte(0) // 0, '"', '\'', '`'
	inRegex := false     // JS/TS regex literal /…/
	inBlockComment := false

	for i := lineIdx; i < len(lines); i++ {
		line := lines[i]
		for j := 0; j < len(line); j++ {
			ch := line[j]

			// Block comment
			if inBlockComment {
				if ch == '*' && j+1 < len(line) && line[j+1] == '/' {
					inBlockComment = false
					j++
				}
				continue
			}

			// String literal
			if inString != 0 {
				if ch == '\\' {
					j++ // skip escaped char
					continue
				}
				if ch == inString {
					inString = 0
				}
				continue
			}

			// Regex literal /…/
			if inRegex {
				if ch == '\\' {
					j++ // skip escaped char
					continue
				}
				if ch == '/' {
					inRegex = false
				}
				continue
			}

			// Slash: line comment, block comment, or regex literal
			if ch == '/' && j+1 < len(line) {
				if line[j+1] == '/' {
					break // line comment — skip rest of line
				}
				if line[j+1] == '*' {
					inBlockComment = true
					j++
					continue
				}
				// Regex literal heuristic: / after operator or at start of expression.
				// If preceded by a brace/paren/operator/comma/semicolon/keyword-end, it's a regex.
				if isRegexSlash(line, j) {
					inRegex = true
					continue
				}
			}

			// String/template openers
			if ch == '"' || ch == '`' {
				inString = ch
				continue
			}
			// Single quote: string in most languages, but lifetime in Rust ('static, 'a).
			// Treat as string only if it looks like a char literal ('x') not a lifetime.
			if ch == '\'' {
				if j+2 < len(line) && line[j+2] == '\'' {
					// Character literal like 'x' — skip the 3 bytes
					j += 2
				}
				// Otherwise ignore (lifetime or unclosed quote at end of line)
				continue
			}

			// Braces
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
	}
	return 0
}

// isRegexSlash determines if a '/' at position j in line is likely a JS/TS
// regex literal opener rather than a division operator.
// Heuristic: regex follows operators, punctuation, or keywords — not identifiers/numbers.
func isRegexSlash(line string, j int) bool {
	// Find the last non-space character before position j
	for k := j - 1; k >= 0; k-- {
		ch := line[k]
		if ch == ' ' || ch == '\t' {
			continue
		}
		// After these characters, / starts a regex
		switch ch {
		case '=', '(', '[', '{', ',', ';', '!', '&', '|', '?', ':', '~', '^', '+', '-', '*', '%', '<', '>', '\n':
			return true
		}
		// After identifier char or closing paren/bracket, it's division
		return false
	}
	// / at start of line (after whitespace) is a regex
	return true
}

func regexFindIndentEnd(lines []string, lineIdx int) int {
	if lineIdx >= len(lines) {
		return lineIdx + 1
	}
	baseIndent := regexIndentLevel(lines[lineIdx])
	lastContentLine := lineIdx + 1
	for i := lineIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if regexIndentLevel(lines[i]) <= baseIndent {
			return lastContentLine
		}
		lastContentLine = i + 1
	}
	return lastContentLine
}

func regexIndentLevel(line string) int {
	n := 0
	for _, ch := range line {
		if ch == ' ' {
			n++
		} else if ch == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

func regexAssignIndentParents(lines []string, symbols []SymbolInfo) {
	for i := range symbols {
		sym := &symbols[i]
		indent := regexIndentLevel(lines[sym.StartLine-1])
		if indent == 0 {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			candidate := &symbols[j]
			candidateIndent := regexIndentLevel(lines[candidate.StartLine-1])
			if candidateIndent < indent && candidate.EndLine >= sym.StartLine {
				sym.ParentIndex = j
				if sym.Type == "function" {
					sym.Type = "method"
				}
				break
			}
		}
	}
}
