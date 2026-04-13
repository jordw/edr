package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseCpp is a hand-written C/C++ symbol + import extractor.
//
// Handles:
//   - #include (both <> and "")
//   - namespace (including C++17 nested ns::ns)
//   - class / struct / union declarations with inheritance
//   - enum / enum class
//   - function + method declarations (heuristic: name(...) followed by {)
//   - constructors, destructors (~Name)
//   - template<...> prefix
//   - typedef, using aliases
//   - Raw string literals R"delim(...)delim", wide/utf string prefixes
//   - Line + block comments, preprocessor directive skipping
//
// Known gaps:
//   - Macros can define/hide/alias declarations
//   - operator overloads (operator<<, etc.) skipped, not recorded
//   - Forward declarations (class Foo;) recorded but with no body
//   - Nested anonymous structs/unions not tracked
//   - Attribute syntax [[...]] not handled
//   - Concepts/requires clauses skipped

type CppResult struct {
	Symbols []CppSymbol
	Imports []CppImport
}

type CppSymbol struct {
	Type      string // "namespace" | "class" | "struct" | "enum" | "function" | "method" | "type"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type CppImport struct {
	Path string
	Line int
}

type cppScopeKind int

const (
	cppBlock     cppScopeKind = iota
	cppNamespace
	cppClass
	cppFunction
)

func ParseCpp(src []byte) CppResult {
	p := &cppParser{s: lexkit.New(src)}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type cppParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[cppScopeKind]
	result       CppResult
	pendingScope *lexkit.Scope[cppScopeKind]
}

func cppStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		s.ScanSimpleString('"')
		return true
	case '\'':
		s.ScanSimpleString('\'')
		return true
	case 'R':
		if s.PeekAt(1) == '"' {
			scanCppRawString(s)
			return true
		}
	case '/':
		if s.PeekAt(1) == '/' {
			s.SkipLineComment()
			return true
		}
		if s.PeekAt(1) == '*' {
			s.Advance(2)
			s.SkipBlockComment("*/")
			return true
		}
	}
	return false
}

func scanCppRawString(s *lexkit.Scanner) {
	s.Advance(2) // skip R"
	start := s.Pos
	for !s.EOF() && s.Peek() != '(' {
		s.Pos++
	}
	delim := string(s.Src[start:s.Pos])
	closing := ")" + delim + "\""
	if !s.EOF() {
		s.Pos++
	}
	for !s.EOF() {
		if s.StartsWith(closing) {
			s.Advance(len(closing))
			return
		}
		s.Next()
	}
}

func (p *cppParser) run() {
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.s.Next()
		case c == '#':
			p.handlePreprocessor()
		case c == '/' && p.s.PeekAt(1) == '/':
			p.s.SkipLineComment()
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		case c == '"':
			p.s.ScanSimpleString('"')
		case c == '\'':
			p.s.ScanSimpleString('\'')
		case c == '{':
			p.handleOpenBrace()
		case c == '}':
			p.handleCloseBrace()
		case c == ';':
			p.s.Pos++
		case c == '~' && p.currentKind() == cppClass:
			p.s.Pos++
			p.skipWSAndComments()
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				name := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				p.tryParseDeclaration([]byte("~" + string(name)))
			}
		case lexkit.DefaultIdentStart[c]:
			if c == 'R' && p.s.PeekAt(1) == '"' {
				scanCppRawString(&p.s)
				continue
			}
			if (c == 'L' || c == 'u' || c == 'U') && p.s.PeekAt(1) == '"' {
				p.s.Pos++
				p.s.ScanSimpleString('"')
				continue
			}
			if c == 'u' && p.s.PeekAt(1) == '8' && p.s.PeekAt(2) == '"' {
				p.s.Advance(2)
				p.s.ScanSimpleString('"')
				continue
			}
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
		}
	}
}

func (p *cppParser) currentKind() cppScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return cppNamespace
}

func (p *cppParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[cppScopeKind]{Data: cppBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == cppFunction {
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if cppStringScanner(&p.s) {
				continue
			}
			switch p.s.Peek() {
			case '{':
				depth++
				p.s.Pos++
			case '}':
				depth--
				p.s.Pos++
			case '\n':
				p.s.Next()
			default:
				p.s.Pos++
			}
		}
		if entry.SymIdx >= 0 {
			p.result.Symbols[entry.SymIdx].EndLine = p.s.Line
		}
		return
	}
	p.stack.Push(entry)
}

func (p *cppParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
		if e.Data == cppClass {
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == ';' {
				p.s.Pos++
			}
		}
	}
}

func (p *cppParser) handlePreprocessor() {
	line := p.s.Line
	p.s.Pos++ // #
	p.s.SkipSpaces()
	word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	if string(word) == "include" {
		p.s.SkipSpaces()
		if p.s.Peek() == '"' {
			p.s.Pos++
			start := p.s.Pos
			for !p.s.EOF() && p.s.Peek() != '"' && p.s.Peek() != '\n' {
				p.s.Pos++
			}
			if path := string(p.s.Src[start:p.s.Pos]); path != "" {
				p.result.Imports = append(p.result.Imports, CppImport{Path: path, Line: line})
			}
			if !p.s.EOF() && p.s.Peek() == '"' {
				p.s.Pos++
			}
		} else if p.s.Peek() == '<' {
			p.s.Pos++
			start := p.s.Pos
			for !p.s.EOF() && p.s.Peek() != '>' && p.s.Peek() != '\n' {
				p.s.Pos++
			}
			if path := string(p.s.Src[start:p.s.Pos]); path != "" {
				p.result.Imports = append(p.result.Imports, CppImport{Path: path, Line: line})
			}
			if !p.s.EOF() && p.s.Peek() == '>' {
				p.s.Pos++
			}
		}
	}
	for !p.s.EOF() {
		if p.s.Peek() == '\\' && p.s.PeekAt(1) == '\n' {
			p.s.Advance(2)
			continue
		}
		if p.s.Peek() == '\n' {
			return
		}
		p.s.Pos++
	}
}

func (p *cppParser) handleIdent(word []byte) {
	kind := p.currentKind()
	switch string(word) {
	case "namespace":
		p.parseNamespace()
		return
	case "class", "struct", "union":
		if kind == cppFunction {
			return
		}
		p.parseClassOrStruct(string(word))
		return
	case "enum":
		if kind == cppFunction {
			return
		}
		p.parseEnum()
		return
	case "template":
		p.skipWSAndComments()
		if !p.s.EOF() && p.s.Peek() == '<' {
			p.s.SkipAngles()
		}
		return
	case "typedef":
		if kind == cppFunction {
			return
		}
		p.parseTypedef()
		return
	case "using":
		if kind == cppFunction {
			return
		}
		p.parseUsing()
		return
	case "public", "protected", "private":
		p.skipWSAndComments()
		if !p.s.EOF() && p.s.Peek() == ':' {
			p.s.Pos++
		}
		return
	case "virtual", "static", "inline", "explicit", "constexpr",
		"consteval", "extern", "friend", "const", "volatile",
		"mutable", "noexcept", "override", "final", "auto",
		"register", "thread_local", "constinit", "signed",
		"unsigned", "long", "short", "void", "bool", "char",
		"int", "float", "double", "wchar_t", "char8_t",
		"char16_t", "char32_t", "decltype", "typeof",
		"if", "else", "for", "while", "do", "switch", "case",
		"break", "continue", "return", "goto", "try", "catch",
		"throw", "new", "delete", "sizeof", "alignof",
		"static_assert", "static_cast", "dynamic_cast",
		"const_cast", "reinterpret_cast", "co_await",
		"co_return", "co_yield", "concept", "requires":
		return
	case "operator":
		p.skipOperatorName()
		return
	}
	if kind != cppFunction {
		p.tryParseDeclaration(word)
	}
}

func (p *cppParser) parseNamespace() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() {
		return
	}
	if p.s.Peek() == '{' {
		p.pendingScope = &lexkit.Scope[cppScopeKind]{Data: cppNamespace, SymIdx: -1, OpenLine: startLine}
		return
	}
	var name string
	for !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		part := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
		if name != "" {
			name += "::" + part
		} else {
			name = part
		}
		p.skipWSAndComments()
		if !p.s.EOF() && p.s.Peek() == ':' && p.s.PeekAt(1) == ':' {
			p.s.Advance(2)
			p.skipWSAndComments()
			continue
		}
		break
	}
	if name == "" {
		return
	}
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, CppSymbol{
		Type: "namespace", Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[cppScopeKind]{Data: cppNamespace, SymIdx: sym, OpenLine: startLine}
}

func (p *cppParser) parseClassOrStruct(kind string) {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	if name == "final" {
		p.skipWSAndComments()
		if p.s.EOF() || (!lexkit.DefaultIdentStart[p.s.Peek()] && p.s.Peek() != ':' && p.s.Peek() != '{') {
			return
		}
	}
	// Scan the preamble (base-class list, template args, attributes) up to { or ;.
	// If we hit * or ( the "struct name" is actually a type in a variable/parameter
	// declaration (e.g. "struct task_struct *fork_idle(...)"), not a definition.
	isDefinition := true
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' || c == ';' {
			break
		}
		if c == '*' || c == '(' || c == ')' || c == '=' {
			isDefinition = false
			break
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		p.s.Pos++
	}
	if !isDefinition {
		return
	}
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	if kind == "union" {
		kind = "struct"
	}
	p.result.Symbols = append(p.result.Symbols, CppSymbol{
		Type: kind, Name: name, StartLine: startLine, Parent: parent,
	})
	if !p.s.EOF() && p.s.Peek() == '{' {
		p.pendingScope = &lexkit.Scope[cppScopeKind]{Data: cppClass, SymIdx: sym, OpenLine: startLine}
	} else {
		p.result.Symbols[sym].EndLine = p.s.Line
		if !p.s.EOF() && p.s.Peek() == ';' {
			p.s.Pos++
		}
	}
}

func (p *cppParser) parseEnum() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() {
		return
	}
	if lexkit.DefaultIdentStart[p.s.Peek()] {
		kw := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(kw) == "class" || string(kw) == "struct" {
			p.skipWSAndComments()
			if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
				p.skipToBraceOrSemicolon()
				return
			}
			kw = p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		}
		name := string(kw)
		p.skipToBraceOrSemicolon()
		if name != "" {
			p.result.Symbols = append(p.result.Symbols, CppSymbol{
				Type: "enum", Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: p.stack.NearestSym(),
			})
		}
		return
	}
	p.skipToBraceOrSemicolon()
}

func (p *cppParser) parseTypedef() {
	startLine := p.s.Line
	var lastName string
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == ';' {
			p.s.Pos++
			if lastName != "" {
				p.result.Symbols = append(p.result.Symbols, CppSymbol{
					Type: "type", Name: lastName, StartLine: startLine, EndLine: p.s.Line, Parent: p.stack.NearestSym(),
				})
			}
			return
		}
		if c == '{' {
			p.s.SkipBalanced('{', '}', cppStringScanner)
			continue
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', cppStringScanner)
			continue
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		if lexkit.DefaultIdentStart[c] {
			lastName = string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
			continue
		}
		p.s.Pos++
	}
}

func (p *cppParser) parseUsing() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		p.skipToSemicolon()
		return
	}
	word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	if string(word) == "namespace" {
		p.skipToSemicolon()
		return
	}
	name := string(word)
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == '=' {
		p.skipToSemicolon()
		p.result.Symbols = append(p.result.Symbols, CppSymbol{
			Type: "type", Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: p.stack.NearestSym(),
		})
		return
	}
	p.skipToSemicolon()
}

func (p *cppParser) tryParseDeclaration(firstWord []byte) {
	startLine := p.s.Line
	lastName := string(firstWord)
	sawParen := false

	for !p.s.EOF() {
		p.skipWSAndComments()
		if p.s.EOF() {
			return
		}
		c := p.s.Peek()
		switch {
		case c == ';':
			p.s.Pos++
			if sawParen && lastName != "" {
				kind := "function"
				parent := p.stack.NearestSym()
				if p.currentKind() == cppClass {
					kind = "method"
				}
				p.result.Symbols = append(p.result.Symbols, CppSymbol{
					Type: kind, Name: lastName, StartLine: startLine,
					EndLine: p.s.Line, Parent: parent,
				})
			}
			return
		case c == '{':
			if sawParen && lastName != "" {
				sym := len(p.result.Symbols)
				kind := "function"
				parent := p.stack.NearestSym()
				if p.currentKind() == cppClass {
					kind = "method"
				}
				p.result.Symbols = append(p.result.Symbols, CppSymbol{
					Type: kind, Name: lastName, StartLine: startLine, Parent: parent,
				})
				p.pendingScope = &lexkit.Scope[cppScopeKind]{Data: cppFunction, SymIdx: sym, OpenLine: startLine}
				p.handleOpenBrace()
			} else {
				p.handleOpenBrace()
			}
			return
		case c == '(':
			sawParen = true
			p.s.SkipBalanced('(', ')', cppStringScanner)
		case c == '<':
			p.s.SkipAngles()
		case c == '=':
			if sawParen {
				// = 0 (pure virtual), = default, = delete — still a declaration
				p.skipToSemicolon()
				if lastName != "" {
					kind := "function"
					parent := p.stack.NearestSym()
					if p.currentKind() == cppClass {
						kind = "method"
					}
					p.result.Symbols = append(p.result.Symbols, CppSymbol{
						Type: kind, Name: lastName, StartLine: startLine,
						EndLine: p.s.Line, Parent: parent,
					})
				}
				return
			}
			p.skipToSemicolon()
			return
		case c == ':' && p.s.PeekAt(1) == ':':
			p.s.Advance(2)
		case c == ':' && p.s.PeekAt(1) != ':':
			if sawParen {
				// Member initializer list: skip to {
				p.s.Pos++
				depth := 0
				for !p.s.EOF() {
					cc := p.s.Peek()
					if cc == '{' && depth == 0 {
						break
					}
					if cc == '(' {
						depth++
					} else if cc == ')' {
						depth--
					}
					if cc == '\n' {
						p.s.Next()
					} else {
						p.s.Pos++
					}
				}
			} else {
				p.s.Pos++
			}
		case c == '*' || c == '&':
			p.s.Pos++
		case c == '~':
			p.s.Pos++
			p.skipWSAndComments()
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				name := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				lastName = "~" + string(name)
			}
		case c == '}':
			// Don't consume closing braces — return so the main loop
			// can pop the scope stack correctly.
			return
		case c == '[':
			p.s.SkipBalanced('[', ']', cppStringScanner)
		case lexkit.DefaultIdentStart[c]:
			w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			ws := string(w)
			switch ws {
			case "class", "struct", "enum", "namespace", "typedef", "using":
				p.handleIdent(w)
				return
			case "const", "volatile", "noexcept", "override", "final",
				"virtual", "static", "inline", "explicit", "constexpr",
				"throw", "mutable", "register":
				continue
			case "operator":
				p.skipOperatorName()
				return
			default:
				lastName = ws
			}
		case c == '\n':
			p.s.Next()
		default:
			p.s.Pos++
		}
	}
}

func (p *cppParser) skipOperatorName() {
	p.skipWSAndComments()
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '(' {
			break
		}
		if c == '<' || c == '>' || c == '+' || c == '-' || c == '*' ||
			c == '/' || c == '%' || c == '=' || c == '!' || c == '&' ||
			c == '|' || c == '^' || c == '~' || c == '[' || c == ']' || c == ',' {
			p.s.Pos++
			continue
		}
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			continue
		}
		break
	}
}

func (p *cppParser) skipWSAndComments() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			p.s.Next()
			continue
		}
		if c == '/' && p.s.PeekAt(1) == '/' {
			p.s.SkipLineComment()
			continue
		}
		if c == '/' && p.s.PeekAt(1) == '*' {
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
			continue
		}
		break
	}
}

func (p *cppParser) skipToSemicolon() {
	depth := 0
	for !p.s.EOF() {
		if cppStringScanner(&p.s) {
			continue
		}
		c := p.s.Peek()
		switch {
		case c == '(' || c == '[' || c == '{':
			depth++
			p.s.Pos++
		case c == ')' || c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
			p.s.Pos++
		case c == ';' && depth == 0:
			p.s.Pos++
			return
		case c == '\n':
			p.s.Next()
		default:
			p.s.Pos++
		}
	}
}

func (p *cppParser) skipToBraceOrSemicolon() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '{' {
			p.s.SkipBalanced('{', '}', cppStringScanner)
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == ';' {
				p.s.Pos++
			}
			return
		}
		if c == ';' {
			p.s.Pos++
			return
		}
		if c == '\n' {
			p.s.Next()
			continue
		}
		p.s.Pos++
	}
}