package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseCSharp is a hand-written C# symbol + import extractor.
//
// Handles:
//   - class / struct / interface / enum / record / record struct
//   - methods, constructors, properties (public int Foo { get; set; })
//   - using statements (imports)
//   - namespace declarations (block-scoped and file-scoped: namespace Foo.Bar;)
//   - modifiers: public, private, protected, internal, static, abstract,
//     sealed, partial, virtual, override, async, readonly, new, extern
//   - generics in declarations
//   - nested types
//   - line + block comments, verbatim strings (@"..."), interpolated strings
//     ($"..."), raw strings (""" ... """), combined forms ($@"...", @$"...")
//   - attributes ([Attribute])
//
// Known gaps:
//   - Expression-bodied property getters (public int X => ...) not recorded
//   - Event declarations (public event EventHandler X) not recorded
//   - Anonymous types and lambdas not tracked
//   - Field declarations not recorded as symbols
//   - Local functions inside methods not tracked

type CSharpResult struct {
	Symbols []CSharpSymbol
	Imports []CSharpImport
}

type CSharpSymbol struct {
	Type      string // "class" | "interface" | "enum" | "method"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type CSharpImport struct {
	Path string
	Line int
}

type csharpScopeKind int

const (
	csBlock    csharpScopeKind = iota
	csClass                    // class / struct / interface / enum / record body
	csFunction                 // method / property / constructor body
	csNamespace
)

func ParseCSharp(src []byte) CSharpResult {
	p := &csharpParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type csharpParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[csharpScopeKind]
	result       CSharpResult
	pendingScope *lexkit.Scope[csharpScopeKind]
	memberStart  bool
}

func csStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		// raw string: """ ... """
		if s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			scanCSharpRawString(s)
			return true
		}
		s.ScanSimpleString('"')
		return true
	case '\'':
		s.ScanSimpleString('\'')
		return true
	case '@':
		// verbatim string @"..." or combined @$"..." / $@"..."
		if s.PeekAt(1) == '"' {
			s.Advance(2)
			scanCSharpVerbatimBody(s)
			return true
		}
		if s.PeekAt(1) == '$' && s.PeekAt(2) == '"' {
			s.Advance(3)
			scanCSharpVerbatimBody(s)
			return true
		}
	case '$':
		// interpolated string $"..." — treat as simple (skip body without tracking nested {})
		if s.PeekAt(1) == '"' {
			s.Advance(2)
			s.ScanSimpleString('"') // simplified: ignores nested braces
			return true
		}
		if s.PeekAt(1) == '@' && s.PeekAt(2) == '"' {
			s.Advance(3)
			scanCSharpVerbatimBody(s)
			return true
		}
		// raw interpolated: $"""..."""
		if s.PeekAt(1) == '"' && s.PeekAt(2) == '"' && s.PeekAt(3) == '"' {
			s.Advance(4)
			scanCSharpRawStringBody(s)
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

func scanCSharpVerbatimBody(s *lexkit.Scanner) {
	// Verbatim strings end at unescaped quote; "" is an escaped quote inside.
	for !s.EOF() {
		if s.Peek() == '"' {
			s.Pos++
			if !s.EOF() && s.Peek() == '"' {
				s.Pos++ // escaped ""
				continue
			}
			return
		}
		s.Next()
	}
}

func scanCSharpRawString(s *lexkit.Scanner) {
	s.Advance(3) // opening """
	scanCSharpRawStringBody(s)
}

func scanCSharpRawStringBody(s *lexkit.Scanner) {
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

func (p *csharpParser) run() {
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
				scanCSharpRawString(&p.s)
			} else {
				p.s.ScanSimpleString('"')
			}
			p.memberStart = false
		case c == '\'':
			p.s.ScanSimpleString('\'')
			p.memberStart = false
		case c == '@' && p.s.PeekAt(1) == '"':
			p.s.Advance(2)
			scanCSharpVerbatimBody(&p.s)
			p.memberStart = false
		case c == '$' && p.s.PeekAt(1) == '"':
			p.s.Advance(2)
			p.s.ScanSimpleString('"')
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
			p.memberStart = false
		case c == '[':
			// attribute or array — skip the bracketed content
			p.s.SkipBalanced('[', ']', csStringScanner)
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *csharpParser) currentKind() csharpScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return csBlock // file-level
}

func (p *csharpParser) inFunction() bool {
	return p.stack.Any(func(k csharpScopeKind) bool { return k == csFunction })
}

func (p *csharpParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[csharpScopeKind]{Data: csBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == csFunction {
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if csStringScanner(&p.s) {
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

func (p *csharpParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
}

func (p *csharpParser) handleIdent(word []byte) {
	switch string(word) {
	case "namespace":
		p.parseNamespace()
		return
	case "using":
		p.parseUsing()
		return
	case "class", "struct", "interface", "enum", "record":
		if !p.inFunction() {
			p.parseTypeDecl(string(word))
			return
		}
	case "public", "private", "protected", "internal",
		"static", "abstract", "sealed", "partial",
		"virtual", "override", "async", "readonly",
		"new", "extern", "unsafe", "volatile", "fixed":
		// modifier — keep memberStart, let next token handle
		return
	case "void", "var", "dynamic":
		// return type keyword — next ident is likely a method name
		return
	}

	// At class scope and memberStart: try to detect method or property
	if p.memberStart && p.currentKind() == csClass && !p.inFunction() {
		p.tryParseMember(word)
		return
	}
	p.memberStart = false
}

func (p *csharpParser) parseNamespace() {
	startLine := p.s.Line
	p.skipWSAndComments()
	// Read dotted namespace name
	start := p.s.Pos
	for !p.s.EOF() {
		if lexkit.DefaultIdentStart[p.s.Peek()] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		} else if p.s.Peek() == '.' {
			p.s.Pos++
		} else {
			break
		}
	}
	name := string(p.s.Src[start:p.s.Pos])
	p.skipWSAndComments()

	// File-scoped namespace: namespace Foo.Bar;
	if !p.s.EOF() && p.s.Peek() == ';' {
		p.s.Pos++
		if name != "" {
			sym := len(p.result.Symbols)
			p.result.Symbols = append(p.result.Symbols, CSharpSymbol{
				Type: "class", Name: name, StartLine: startLine, Parent: -1,
			})
			// File-scoped namespace — push a namespace scope that covers rest of file
			p.stack.Push(lexkit.Scope[csharpScopeKind]{Data: csNamespace, SymIdx: sym, OpenLine: startLine})
		}
		p.memberStart = true
		return
	}

	// Block-scoped namespace: namespace Foo.Bar { ... }
	if name != "" {
		sym := len(p.result.Symbols)
		p.result.Symbols = append(p.result.Symbols, CSharpSymbol{
			Type: "class", Name: name, StartLine: startLine, Parent: -1,
		})
		p.pendingScope = &lexkit.Scope[csharpScopeKind]{Data: csNamespace, SymIdx: sym, OpenLine: startLine}
	}
}

func (p *csharpParser) parseUsing() {
	startLine := p.s.Line
	p.skipWSAndComments()
	// Skip optional "static" or "global::"
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		ws := string(w)
		if ws == "static" || ws == "global" {
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == ':' {
				// "global::" — skip ::
				p.s.Pos++
				if !p.s.EOF() && p.s.Peek() == ':' {
					p.s.Pos++
				}
				p.skipWSAndComments()
			}
		} else {
			p.s.Pos = save
		}
	}
	// Read dotted path (may end with alias = ...)
	start := p.s.Pos
	for !p.s.EOF() {
		if lexkit.DefaultIdentStart[p.s.Peek()] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		} else if p.s.Peek() == '.' {
			p.s.Pos++
		} else if p.s.Peek() == '*' {
			p.s.Pos++
		} else if p.s.Peek() == '=' {
			// alias: using Alias = Some.Type; — skip the rest
			for !p.s.EOF() && p.s.Peek() != ';' && p.s.Peek() != '\n' {
				p.s.Pos++
			}
			break
		} else {
			break
		}
	}
	path := string(p.s.Src[start:p.s.Pos])
	if path != "" {
		p.result.Imports = append(p.result.Imports, CSharpImport{Path: path, Line: startLine})
	}
	for !p.s.EOF() && p.s.Peek() != ';' && p.s.Peek() != '\n' {
		p.s.Pos++
	}
	if !p.s.EOF() && p.s.Peek() == ';' {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *csharpParser) parseTypeDecl(kind string) {
	startLine := p.s.Line
	p.skipWSAndComments()

	// "record struct" — consume "struct" keyword if present
	if kind == "record" && !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(w) != "struct" && string(w) != "class" {
			p.s.Pos = save // not a keyword — it's the name
		} else {
			p.skipWSAndComments()
		}
	}

	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))

	// Skip generics, base list, constraints until {
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
			// Bodyless declaration (e.g., record Point(double X, double Y);)
			p.s.Pos++
			p.result.Symbols = append(p.result.Symbols, CSharpSymbol{
				Type: "class", Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: p.stack.NearestSym(),
			})
			return
		}
		p.s.Pos++
	}

	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, CSharpSymbol{
		Type: "class", Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[csharpScopeKind]{Data: csClass, SymIdx: sym, OpenLine: startLine}
}

// tryParseMember handles tokens at class scope that could be the start
// of a method, property, or constructor. C# members follow the pattern:
// [modifiers] [returnType] Name(params) { body }   — method/constructor
// [modifiers] [returnType] Name { get; set; }       — property
func (p *csharpParser) tryParseMember(firstWord []byte) {
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
			if lastName != "" {
				sym := p.recordMethod(lastName, startLine)
				p.pendingScope = &lexkit.Scope[csharpScopeKind]{Data: csFunction, SymIdx: sym, OpenLine: startLine}
				p.handleOpenBrace()
			} else {
				p.handleOpenBrace()
			}
			return
		case c == '(':
			sawParen = true
			p.s.SkipBalanced('(', ')', csStringScanner)
		case c == '<':
			p.s.SkipAngles()
		case c == '=':
			if sawParen && lastName != "" && p.s.PeekAt(1) == '>' {
				// Expression-bodied method: name(...) => expr;
				p.s.Advance(2)
				sym := p.recordMethod(lastName, startLine)
				p.skipToMemberEnd()
				p.result.Symbols[sym].EndLine = p.s.Line
				return
			}
			p.skipToMemberEnd()
			return
		case c == '[' || c == ']':
			p.s.Pos++ // array brackets in type
		case c == '.':
			p.s.Pos++
		case c == ',':
			p.skipToMemberEnd()
			return
		case c == '[':
			// attribute
			p.s.SkipBalanced('[', ']', csStringScanner)
		case lexkit.DefaultIdentStart[c]:
			w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			ws := string(w)
			switch ws {
			case "class", "struct", "interface", "enum", "record":
				p.parseTypeDecl(ws)
				return
			case "public", "private", "protected", "internal",
				"static", "abstract", "sealed", "partial",
				"virtual", "override", "async", "readonly",
				"new", "extern", "unsafe", "volatile":
				continue
			case "void", "var", "dynamic":
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

func (p *csharpParser) recordMethod(name string, startLine int) int {
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, CSharpSymbol{
		Type: "method", Name: name, StartLine: startLine, Parent: parent,
	})
	return sym
}

func (p *csharpParser) skipToMemberEnd() {
	depth := 0
	for !p.s.EOF() {
		if csStringScanner(&p.s) {
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

func (p *csharpParser) skipWSAndComments() {
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
