// Regex-based symbol extraction. Pure Go, no CGO, no external dependencies.
// Replaces tree-sitter for symbol detection and boundary finding.
package index

import (
	"path/filepath"
	"regexp"
	"strings"
)

// regexLang defines patterns and end-detection style for a language.
type regexLang struct {
	patterns []regexPattern
	endStyle regexEndStyle
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
}

// --- Language definitions ---

var regexGo = &regexLang{
	endStyle: regexBraceEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^func\s+\((\w+)\s+\*?(\w+)\)\s+(\w+)\s*\(`), "method", 3},
		{regexp.MustCompile(`^func\s+(\w+)\s*[\(\[]`), "function", 1},
		{regexp.MustCompile(`^type\s+(\w+)\s+struct\s*\{`), "struct", 1},
		{regexp.MustCompile(`^type\s+(\w+)\s+interface\s*\{`), "interface", 1},
		{regexp.MustCompile(`^type\s+(\w+)\s+`), "type", 1},
		{regexp.MustCompile(`^var\s+(\w+)\s+`), "variable", 1},
	},
}

var regexPython = &regexLang{
	endStyle: regexIndentEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^(\s*)class\s+(\w+)`), "class", 2},
		{regexp.MustCompile(`^(\s*)(?:async\s+)?def\s+(\w+)\s*\(`), "function", 2},
	},
}

var jsKeywords = map[string]bool{
	"if": true, "else": true, "for": true, "while": true, "do": true,
	"switch": true, "case": true, "break": true, "continue": true,
	"return": true, "throw": true, "try": true, "catch": true, "finally": true,
	"new": true, "delete": true, "typeof": true, "void": true, "in": true,
	"instanceof": true, "with": true, "yield": true, "await": true,
	"import": true, "export": true, "default": true, "from": true,
}

var regexTypeScript = &regexLang{
	endStyle: regexBraceEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:abstract\s+)?class\s+(\w+)`), "class", 1},
		{regexp.MustCompile(`^(?:export\s+)?interface\s+(\w+)`), "interface", 1},
		{regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s+(\w+)`), "function", 1},
		{regexp.MustCompile(`^(?:export\s+)?type\s+(\w+)\s*(?:<[^>]*>)?\s*=`), "type", 1},
		{regexp.MustCompile(`^(?:export\s+)?(?:const\s+)?enum\s+(\w+)`), "type", 1},
		{regexp.MustCompile(`^(?:export\s+)?(?:declare\s+)?namespace\s+(\w+)`), "class", 1},
		// Methods with modifier keywords
		{regexp.MustCompile(`^\s+(?:private|protected|public|static|abstract|override|readonly|async|get|set)\s+(?:(?:private|protected|public|static|abstract|override|readonly|async|get|set)\s+)*#?(\w+)\s*(?:<[^>]*>)?\s*[\(:]`), "method", 1},
		{regexp.MustCompile(`^\s+(constructor)\s*\(`), "method", 1},
		// Unmodified methods — require { at end of line
		{regexp.MustCompile(`^\s+#?(\w+)\s*\([^)]*\)\s*(?::\s*[^{]*)?\{\s*$`), "method", 1},
		// Arrow functions
		{regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+(\w+)\s*(?::\s*[^=]+)?\s*=\s*(?:async\s+)?(?:function|\(|[a-zA-Z_]\w*\s*=>)`), "function", 1},
		// Static arrow methods
		{regexp.MustCompile(`^\s+(?:static\s+)?(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[a-zA-Z_]\w*)\s*(?::\s*[^=]+)?\s*=>`), "method", 1},
	},
}

var regexRust = &regexLang{
	endStyle: regexBraceEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+(\w+)`), "function", 1},
		{regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?struct\s+(\w+)`), "struct", 1},
		{regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?enum\s+(\w+)`), "type", 1},
		{regexp.MustCompile(`^(?:pub(?:\([^)]*\))?\s+)?trait\s+(\w+)`), "interface", 1},
		{regexp.MustCompile(`^impl(?:<[^>]*>)?\s+(?:[\w:]+\s+for\s+)?(\w+)`), "impl", 1},
	},
}

var regexJava = &regexLang{
	endStyle: regexBraceEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:abstract\s+)?(?:class|interface|enum)\s+(\w+)`), "class", 1},
		// Constructors: visibility + UpperCaseName(  — no return type
		{regexp.MustCompile(`^\s*(?:public|private|protected)\s+([A-Z]\w*)\s*\(`), "method", 1},
		// Methods: have a return type before the name
		{regexp.MustCompile(`^\s*(?:public|private|protected)?\s*(?:static\s+)?(?:abstract\s+)?(?:synchronized\s+)?(?:\w+(?:<[^>]*>)?(?:\[\])*)\s+(\w+)\s*\(`), "method", 1},
	},
}

var regexRuby = &regexLang{
	endStyle: regexIndentEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^(\s*)class\s+(\w+)`), "class", 2},
		{regexp.MustCompile(`^(\s*)module\s+(\w+)`), "class", 2},
		{regexp.MustCompile(`^(\s*)def\s+(\w+[?!=]?)`), "function", 2},
	},
}

var regexC = &regexLang{
	endStyle: regexBraceEnd,
	patterns: []regexPattern{
		{regexp.MustCompile(`^(?:static\s+)?(?:inline\s+)?(?:const\s+)?(?:unsigned\s+)?(?:struct\s+)?(?:\w+(?:\s*\*)*)\s+(\w+)\s*\([^;]*$`), "function", 1},
		{regexp.MustCompile(`^(?:typedef\s+)?struct\s+(\w+)\s*\{`), "struct", 1},
		{regexp.MustCompile(`^(?:typedef\s+)?enum\s+(\w+)\s*\{`), "type", 1},
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
	// TODO: add patterns for .cs, .kt, .php, .swift, .scala, .lua, .zig once validated.
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
			if ch == '"' || ch == '\'' || ch == '`' {
				inString = ch
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
