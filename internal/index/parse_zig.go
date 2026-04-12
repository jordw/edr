package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseZig is a hand-written Zig symbol + import extractor.
//
// Handles:
//   - fn name(...)                      → "function"
//   - pub fn name(...)                  → "function"
//   - const Name = struct { ... }       → "struct"
//   - const Name = enum { ... }         → "enum"
//   - const Name = union { ... }        → "type"
//   - const Name = error { ... }        → "type"
//   - const name = value;               → "constant" (non-struct/enum/union/error RHS)
//   - pub const / pub fn                → same as above (pub is a modifier)
//   - test "name" { ... }               → "function" (test block)
//   - @import("std")                    → import
//   - line comments: //
//   - strings: "..." with escapes
//   - brace scope: { }
//
// Known gaps:
//   - comptime blocks not tracked
//   - var declarations not recorded
//   - struct field functions not tracked as top-level

type ZigResult struct {
	Symbols []ZigSymbol
	Imports []ZigImport
}

type ZigSymbol struct {
	Type      string // "function" | "struct" | "enum" | "type" | "constant"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type ZigImport struct {
	Path string
	Line int
}

type zigScopeKind int

const (
	zigBlock    zigScopeKind = iota
	zigStruct                // struct/enum/union/error body (track as container)
	zigFunction              // fn body (skip)
)

func ParseZig(src []byte) ZigResult {
	p := &zigParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type zigParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[zigScopeKind]
	result       ZigResult
	pendingScope *lexkit.Scope[zigScopeKind]
	memberStart  bool
	isPub        bool // last token was "pub"
}

func zigStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		s.ScanSimpleString('"')
		return true
	case '/':
		if s.PeekAt(1) == '/' {
			s.SkipLineComment()
			return true
		}
	}
	return false
}

func (p *zigParser) run() {
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
		case c == '"':
			p.s.ScanSimpleString('"')
			p.memberStart = false
		case c == '\\' && p.s.PeekAt(1) == '\\':
			// Multiline string — skip to end of line
			p.s.SkipLineComment()
		case c == '@':
			p.handleBuiltin()
			p.memberStart = false
		case c == '{':
			p.handleOpenBrace()
		case c == '}':
			p.handleCloseBrace()
		case c == ';':
			p.s.Pos++
			p.memberStart = true
			p.isPub = false
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *zigParser) currentKind() zigScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return zigBlock
}

func (p *zigParser) inFunction() bool {
	return p.stack.Any(func(k zigScopeKind) bool { return k == zigFunction })
}

// inContainer returns true if we are currently inside any non-file scope
// (struct, enum, union, etc.) or function. Used to suppress recording of
// nested fn declarations that belong to a struct body.
func (p *zigParser) inContainer() bool {
	return p.stack.Any(func(k zigScopeKind) bool { return k == zigFunction || k == zigStruct })
}

func (p *zigParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[zigScopeKind]{Data: zigBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == zigFunction {
		// Skip function body without descending
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if zigStringScanner(&p.s) {
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
		p.memberStart = true
		return
	}
	p.stack.Push(entry)
	p.memberStart = true
}

func (p *zigParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
	p.isPub = false
}

// handleBuiltin handles @-prefixed builtins, specifically @import.
func (p *zigParser) handleBuiltin() {
	p.s.Pos++ // consume '@'
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	if string(name) == "import" {
		p.parseAtImport()
		return
	}
	// Other builtins: skip argument list if present
	p.skipWSInline()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', zigStringScanner)
	}
}

// parseAtImport extracts the path from @import("path").
func (p *zigParser) parseAtImport() {
	line := p.s.Line
	p.skipWSInline()
	if p.s.EOF() || p.s.Peek() != '(' {
		return
	}
	p.s.Pos++ // consume '('
	p.skipWSInline()
	if p.s.EOF() || p.s.Peek() != '"' {
		// skip to close paren
		for !p.s.EOF() && p.s.Peek() != ')' && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		if !p.s.EOF() && p.s.Peek() == ')' {
			p.s.Pos++
		}
		return
	}
	p.s.Pos++ // consume opening '"'
	start := p.s.Pos
	for !p.s.EOF() {
		ch := p.s.Peek()
		if ch == '"' {
			path := string(p.s.Src[start:p.s.Pos])
			p.s.Pos++
			p.result.Imports = append(p.result.Imports, ZigImport{Path: path, Line: line})
			break
		}
		if ch == '\\' {
			p.s.Pos += 2
			continue
		}
		if ch == '\n' {
			break
		}
		p.s.Pos++
	}
	// skip to close paren
	for !p.s.EOF() && p.s.Peek() != ')' && p.s.Peek() != '\n' {
		p.s.Pos++
	}
	if !p.s.EOF() && p.s.Peek() == ')' {
		p.s.Pos++
	}
}

func (p *zigParser) handleIdent(word []byte) {
	ws := string(word)
	switch ws {
	case "pub":
		p.isPub = true
		return
	case "fn":
		if !p.inContainer() {
			p.parseFn()
		}
		p.isPub = false
		return
	case "const":
		if !p.inContainer() {
			p.parseConst()
		}
		p.isPub = false
		return
	case "test":
		if !p.inFunction() {
			p.parseTest()
		}
		p.isPub = false
		return
	case "var":
		// var declarations: not tracked as symbols, skip
		p.isPub = false
		return
	}
	p.memberStart = false
	p.isPub = false
}

// parseFn parses a function declaration after the "fn" keyword.
func (p *zigParser) parseFn() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Skip parameter list
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', zigStringScanner)
	}
	// Skip return type and any attributes until '{' or ';'
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			parent := p.stack.NearestSym()
			sym := len(p.result.Symbols)
			p.result.Symbols = append(p.result.Symbols, ZigSymbol{
				Type: "function", Name: name, StartLine: startLine, Parent: parent,
			})
			p.pendingScope = &lexkit.Scope[zigScopeKind]{Data: zigFunction, SymIdx: sym, OpenLine: startLine}
			return
		}
		if c == ';' {
			// extern/forward declaration
			parent := p.stack.NearestSym()
			p.result.Symbols = append(p.result.Symbols, ZigSymbol{
				Type: "function", Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: parent,
			})
			return
		}
		if c == '\n' {
			p.s.Next()
			continue
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', zigStringScanner)
			continue
		}
		if c == '[' {
			p.s.SkipBalanced('[', ']', zigStringScanner)
			continue
		}
		if c == '@' {
			p.handleBuiltin()
			continue
		}
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			continue
		}
		p.s.Pos++
	}
}

// parseConst parses a const declaration: const Name = <type-expr>;
func (p *zigParser) parseConst() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Skip optional type annotation: `: Type`
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == ':' {
		p.s.Pos++
		// skip type until '='
		for !p.s.EOF() {
			p.skipWSAndComments()
			c := p.s.Peek()
			if c == '=' || c == ';' || c == '\n' {
				break
			}
			if c == '(' {
				p.s.SkipBalanced('(', ')', zigStringScanner)
				continue
			}
			if c == '[' {
				p.s.SkipBalanced('[', ']', zigStringScanner)
				continue
			}
			p.s.Pos++
		}
	}
	p.skipWSAndComments()
	if p.s.EOF() || p.s.Peek() != '=' {
		// No '=' — incomplete or unexpected syntax
		return
	}
	p.s.Pos++ // consume '='
	p.skipWSAndComments()

	// Determine what follows the '='
	if p.s.EOF() {
		return
	}

	// Check for keyword-defined type: struct, enum, union, error
	if lexkit.DefaultIdentStart[p.s.Peek()] {
		kwSave := p.s.Pos
		kw := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		kwStr := string(kw)
		switch kwStr {
		case "struct":
			p.skipWSAndComments()
			// Skip optional base types or packed/extern qualifiers
			if !p.s.EOF() && p.s.Peek() == '(' {
				p.s.SkipBalanced('(', ')', zigStringScanner)
				p.skipWSAndComments()
			}
			if !p.s.EOF() && p.s.Peek() == '{' {
				parent := p.stack.NearestSym()
				sym := len(p.result.Symbols)
				p.result.Symbols = append(p.result.Symbols, ZigSymbol{
					Type: "struct", Name: name, StartLine: startLine, Parent: parent,
				})
				p.pendingScope = &lexkit.Scope[zigScopeKind]{Data: zigStruct, SymIdx: sym, OpenLine: startLine}
			}
			return
		case "enum":
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == '(' {
				p.s.SkipBalanced('(', ')', zigStringScanner)
				p.skipWSAndComments()
			}
			if !p.s.EOF() && p.s.Peek() == '{' {
				parent := p.stack.NearestSym()
				sym := len(p.result.Symbols)
				p.result.Symbols = append(p.result.Symbols, ZigSymbol{
					Type: "enum", Name: name, StartLine: startLine, Parent: parent,
				})
				p.pendingScope = &lexkit.Scope[zigScopeKind]{Data: zigStruct, SymIdx: sym, OpenLine: startLine}
			}
			return
		case "union":
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == '(' {
				p.s.SkipBalanced('(', ')', zigStringScanner)
				p.skipWSAndComments()
			}
			if !p.s.EOF() && p.s.Peek() == '{' {
				parent := p.stack.NearestSym()
				sym := len(p.result.Symbols)
				p.result.Symbols = append(p.result.Symbols, ZigSymbol{
					Type: "type", Name: name, StartLine: startLine, Parent: parent,
				})
				p.pendingScope = &lexkit.Scope[zigScopeKind]{Data: zigStruct, SymIdx: sym, OpenLine: startLine}
			}
			return
		case "error":
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == '{' {
				parent := p.stack.NearestSym()
				sym := len(p.result.Symbols)
				p.result.Symbols = append(p.result.Symbols, ZigSymbol{
					Type: "type", Name: name, StartLine: startLine, Parent: parent,
				})
				p.pendingScope = &lexkit.Scope[zigScopeKind]{Data: zigStruct, SymIdx: sym, OpenLine: startLine}
			}
			return
		default:
			// Not a container keyword — restore pos, record as constant
			p.s.Pos = kwSave
		}
	}

	// Check for @import on the RHS — record import before the constant.
	if !p.s.EOF() && p.s.Peek() == '@' {
		p.handleBuiltin()
	}

	// Record as a constant and skip to end of statement.
	parent := p.stack.NearestSym()
	p.result.Symbols = append(p.result.Symbols, ZigSymbol{
		Type: "constant", Name: name, StartLine: startLine, Parent: parent,
	})
	// Skip to semicolon
	p.skipToSemicolon()
	if !p.s.EOF() && p.result.Symbols[len(p.result.Symbols)-1].EndLine == 0 {
		p.result.Symbols[len(p.result.Symbols)-1].EndLine = p.s.Line
	}
}

// parseTest parses a test block: test "name" { ... }
func (p *zigParser) parseTest() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() {
		return
	}
	// Expect a string literal for the test name
	if p.s.Peek() != '"' {
		// unnamed test block — skip
		for !p.s.EOF() && p.s.Peek() != '{' && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		if !p.s.EOF() && p.s.Peek() == '{' {
			p.skipBraceBlock()
		}
		return
	}
	p.s.Pos++ // consume '"'
	start := p.s.Pos
	for !p.s.EOF() {
		ch := p.s.Peek()
		if ch == '"' {
			name := string(p.s.Src[start:p.s.Pos])
			p.s.Pos++
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == '{' {
				parent := p.stack.NearestSym()
				sym := len(p.result.Symbols)
				p.result.Symbols = append(p.result.Symbols, ZigSymbol{
					Type: "function", Name: name, StartLine: startLine, Parent: parent,
				})
				p.pendingScope = &lexkit.Scope[zigScopeKind]{Data: zigFunction, SymIdx: sym, OpenLine: startLine}
			}
			return
		}
		if ch == '\\' {
			p.s.Pos += 2
			continue
		}
		if ch == '\n' {
			break
		}
		p.s.Pos++
	}
}

// skipBraceBlock skips a { ... } block with balanced braces.
func (p *zigParser) skipBraceBlock() {
	if p.s.EOF() || p.s.Peek() != '{' {
		return
	}
	p.s.SkipBalanced('{', '}', zigStringScanner)
}

// skipToSemicolon skips tokens until ';', balanced across brackets.
func (p *zigParser) skipToSemicolon() {
	depth := 0
	for !p.s.EOF() {
		if zigStringScanner(&p.s) {
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
			return
		case c == '\n':
			p.s.Next()
		default:
			p.s.Pos++
		}
	}
}

func (p *zigParser) skipWSInline() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' {
			p.s.Pos++
			continue
		}
		break
	}
}

func (p *zigParser) skipWSAndComments() {
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
		break
	}
}
