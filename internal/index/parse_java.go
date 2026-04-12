package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseJava is a hand-written Java symbol + import extractor.
//
// Handles:
//   - class / interface / enum / record / @interface (annotation type)
//   - methods + constructors (with visibility, static, abstract, etc.)
//   - import statements (single and static)
//   - package declaration
//   - generics in declarations
//   - nested classes
//   - line + block comments, strings, text blocks (triple-quote)
//   - annotations (@Name)
//
// Known gaps:
//   - Anonymous inner classes not tracked
//   - Lambda expressions not tracked
//   - Field declarations not recorded as symbols

type JavaResult struct {
	Symbols []JavaSymbol
	Imports []JavaImport
}

type JavaSymbol struct {
	Type      string // "class" | "interface" | "enum" | "method"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type JavaImport struct {
	Path string
	Line int
}

type javaScopeKind int

const (
	javaBlock javaScopeKind = iota
	javaClass
	javaFunction
)

func ParseJava(src []byte) JavaResult {
	p := &javaParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type javaParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[javaScopeKind]
	result       JavaResult
	pendingScope *lexkit.Scope[javaScopeKind]
	memberStart  bool
}

func javaStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		if s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			scanJavaTextBlock(s)
		} else {
			s.ScanSimpleString('"')
		}
		return true
	case '\'':
		s.ScanSimpleString('\'')
		return true
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

func scanJavaTextBlock(s *lexkit.Scanner) {
	s.Advance(3) // opening """
	for !s.EOF() {
		if s.Peek() == '"' && s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			s.Advance(3)
			return
		}
		if s.Peek() == '\\' && s.Pos+1 < len(s.Src) {
			s.Advance(2)
			continue
		}
		s.Next()
	}
}

func (p *javaParser) run() {
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.s.Pos++
		case c == '\n':
			p.s.Next()
			p.memberStart = true
		case c == '/' && p.s.PeekAt(1) == '/':
			p.s.SkipLineComment()
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		case c == '"':
			if p.s.PeekAt(1) == '"' && p.s.PeekAt(2) == '"' {
				scanJavaTextBlock(&p.s)
			} else {
				p.s.ScanSimpleString('"')
			}
			p.memberStart = false
		case c == '\'':
			p.s.ScanSimpleString('\'')
			p.memberStart = false
		case c == '{':
			p.handleOpenBrace()
		case c == '}':
			p.handleCloseBrace()
		case c == ';':
			p.s.Pos++
			p.memberStart = true
		case c == '<':
			p.s.SkipAngles()
		case c == '@':
			p.handleAnnotation()
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *javaParser) currentKind() javaScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return javaBlock // file-level
}

func (p *javaParser) inFunction() bool {
	return p.stack.Any(func(k javaScopeKind) bool { return k == javaFunction })
}

func (p *javaParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[javaScopeKind]{Data: javaBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == javaFunction {
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if javaStringScanner(&p.s) {
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
	p.memberStart = true
}

func (p *javaParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
}

func (p *javaParser) handleAnnotation() {
	p.s.Pos++ // @
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	// @interface is a Java annotation type declaration, not an annotation usage.
	if string(word) == "interface" {
		p.parseTypeDecl("interface")
		return
	}
	// Skip optional annotation arguments
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', javaStringScanner)
	}
}

func (p *javaParser) handleIdent(word []byte) {
	switch string(word) {
	case "package":
		p.parsePackage()
		return
	case "import":
		p.parseImport()
		return
	case "class", "interface", "enum", "record":
		if !p.inFunction() {
			p.parseTypeDecl(string(word))
			return
		}
	case "public", "private", "protected", "static", "final",
		"abstract", "synchronized", "native", "strictfp",
		"transient", "volatile", "default", "sealed", "non":
		// modifier — keep memberStart, let next token handle
		return
	case "void":
		// return type — next ident is likely a method name
		return
	}

	// At class scope and memberStart: try to detect method/constructor
	if p.memberStart && p.currentKind() == javaClass && !p.inFunction() {
		p.tryParseMethod(word)
		return
	}
	p.memberStart = false
}

func (p *javaParser) parsePackage() {
	// Skip to semicolon
	for !p.s.EOF() && p.s.Peek() != ';' {
		p.s.Pos++
	}
	if !p.s.EOF() {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *javaParser) parseImport() {
	startLine := p.s.Line
	p.skipWSAndComments()
	// Optional "static"
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(w) != "static" {
			p.s.Pos = save
		} else {
			p.skipWSAndComments()
		}
	}
	// Read dotted path
	start := p.s.Pos
	for !p.s.EOF() {
		if lexkit.DefaultIdentStart[p.s.Peek()] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		} else if p.s.Peek() == '.' {
			p.s.Pos++
		} else if p.s.Peek() == '*' {
			p.s.Pos++
		} else {
			break
		}
	}
	path := string(p.s.Src[start:p.s.Pos])
	if path != "" {
		p.result.Imports = append(p.result.Imports, JavaImport{Path: path, Line: startLine})
	}
	for !p.s.EOF() && p.s.Peek() != ';' && p.s.Peek() != '\n' {
		p.s.Pos++
	}
	if !p.s.EOF() && p.s.Peek() == ';' {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *javaParser) parseTypeDecl(kind string) {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Skip generics, extends, implements until {
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			break
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		if c == ';' {
			p.s.Pos++
			return
		}
		p.s.Pos++
	}
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	typ := "class"
	if kind == "interface" {
		typ = "interface"
	} else if kind == "enum" {
		typ = "enum"
	}
	p.result.Symbols = append(p.result.Symbols, JavaSymbol{
		Type: typ, Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[javaScopeKind]{Data: javaClass, SymIdx: sym, OpenLine: startLine}
}

// tryParseMethod handles tokens at class scope that could be the start
// of a method or constructor declaration. Java methods follow the
// pattern: [modifiers] [returnType] name(params) [throws...] { body }
// Constructors: [modifiers] ClassName(params) [throws...] { body }
func (p *javaParser) tryParseMethod(firstWord []byte) {
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
				p.recordMethod(lastName, startLine)
			}
			p.memberStart = true
			return
		case c == '{':
			if sawParen && lastName != "" {
				sym := p.recordMethod(lastName, startLine)
				p.pendingScope = &lexkit.Scope[javaScopeKind]{Data: javaFunction, SymIdx: sym, OpenLine: startLine}
				p.handleOpenBrace()
			} else {
				p.handleOpenBrace()
			}
			return
		case c == '(':
			sawParen = true
			p.s.SkipBalanced('(', ')', javaStringScanner)
		case c == '<':
			p.s.SkipAngles()
		case c == '=':
			// Field initialization
			p.skipToFieldEnd()
			return
		case c == '[' || c == ']':
			p.s.Pos++ // array brackets in type
		case c == '.':
			p.s.Pos++
		case c == ',':
			// Multiple field declarations — skip
			p.skipToFieldEnd()
			return
		case c == '@':
			p.handleAnnotation()
		case lexkit.DefaultIdentStart[c]:
			w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			ws := string(w)
			switch ws {
			case "class", "interface", "enum", "record":
				p.parseTypeDecl(ws)
				return
			case "throws":
				// Skip exception list: throws X, Y, Z
				for !p.s.EOF() {
					p.skipWSAndComments()
					cc := p.s.Peek()
					if cc == '{' || cc == ';' {
						break
					}
					if cc == ',' || cc == '.' {
						p.s.Pos++
						continue
					}
					if lexkit.DefaultIdentStart[cc] {
						p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
						continue
					}
					break
				}
				continue
			case "public", "private", "protected", "static", "final",
				"abstract", "synchronized", "native", "default",
				"volatile", "transient", "strictfp":
				continue
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

func (p *javaParser) recordMethod(name string, startLine int) int {
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, JavaSymbol{
		Type: "method", Name: name, StartLine: startLine, Parent: parent,
	})
	return sym
}

func (p *javaParser) skipToFieldEnd() {
	depth := 0
	for !p.s.EOF() {
		if javaStringScanner(&p.s) {
			continue
		}
		c := p.s.Peek()
		switch {
		case c == '(' || c == '[' || c == '{':
			depth++
			p.s.Pos++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return
			}
			depth--
			p.s.Pos++
		case c == ';' && depth == 0:
			p.s.Pos++
			p.memberStart = true
			return
		case c == '\n':
			p.s.Next()
		default:
			p.s.Pos++
		}
	}
}

func (p *javaParser) skipWSAndComments() {
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