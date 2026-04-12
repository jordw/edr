package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseLua is a hand-written Lua symbol + import extractor.
//
// Handles:
//   - function name(...)                 → "function" (file scope)
//   - local function name(...)           → "function" (file scope)
//   - function module.name(...)          → "function" (name = last segment)
//   - function module:name(...)          → "function" (method syntax)
//   - require("modname") / require "modname" → import
//   - line comments: --
//   - block comments: --[[...]] (with optional =: --[==[...]==])
//   - strings: "...", '...', [[...]] (with optional =)
//   - block scope: function...end, if...end, for...end, while...end, do...end, repeat...until
//
// Known gaps:
//   - Local variable assignments (local x = function() end) not tracked
//   - Table-field functions (t.fn = function() end) not tracked

type LuaResult struct {
	Symbols []LuaSymbol
	Imports []LuaImport
}

type LuaSymbol struct {
	Type      string // "function"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type LuaImport struct {
	Path string
	Line int
}

type luaScopeKind int

const (
	luaBlock    luaScopeKind = iota
	luaFunction
)

func ParseLua(src []byte) LuaResult {
	p := &luaParser{s: lexkit.New(src)}
	p.run()
	// Close any unclosed scopes
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type luaParser struct {
	s      lexkit.Scanner
	stack  lexkit.ScopeStack[luaScopeKind]
	result LuaResult
	// depth counts keyword-terminated scopes not tracked by stack.
	// function...end uses the stack; if/for/while/do/repeat use depth.
	blockDepth int
}

// luaStringScanner handles Lua strings and comments for SkipBalanced.
func luaStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		s.ScanSimpleString('"')
		return true
	case '\'':
		s.ScanSimpleString('\'')
		return true
	case '[':
		if s.PeekAt(1) == '[' || s.PeekAt(1) == '=' {
			if scanLuaLongString(s) {
				return true
			}
		}
	case '-':
		if s.PeekAt(1) == '-' {
			s.Advance(2)
			if !s.EOF() && s.Peek() == '[' {
				// Try long comment
				save := s.Pos
				if scanLuaLongString(s) {
					return true
				}
				s.Pos = save
			}
			s.SkipLineComment()
			return true
		}
	}
	return false
}

// scanLuaLongString advances past a Lua long string/comment starting at [.
// Returns true if it looked like a long bracket and was consumed.
func scanLuaLongString(s *lexkit.Scanner) bool {
	if s.EOF() || s.Peek() != '[' {
		return false
	}
	s.Pos++ // consume '['
	// count '='
	level := 0
	for !s.EOF() && s.Peek() == '=' {
		level++
		s.Pos++
	}
	if s.EOF() || s.Peek() != '[' {
		// Not a valid long bracket — rewind
		s.Pos -= level + 1
		return false
	}
	s.Pos++ // consume second '['
	// scan until ]=*] with same level
	for !s.EOF() {
		if s.Peek() == '\n' {
			s.Next()
			continue
		}
		if s.Peek() == ']' {
			s.Pos++
			cnt := 0
			for !s.EOF() && s.Peek() == '=' {
				cnt++
				s.Pos++
			}
			if !s.EOF() && s.Peek() == ']' && cnt == level {
				s.Pos++
				return true
			}
			// Not a match — keep scanning
			continue
		}
		s.Pos++
	}
	return true
}

func (p *luaParser) run() {
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.s.Pos++
		case c == '\n':
			p.s.Next()
		case c == '-' && p.s.PeekAt(1) == '-':
			// Comment
			p.s.Advance(2)
			if !p.s.EOF() && p.s.Peek() == '[' {
				save := p.s.Pos
				if scanLuaLongString(&p.s) {
					continue
				}
				p.s.Pos = save
			}
			p.s.SkipLineComment()
		case c == '"':
			p.s.ScanSimpleString('"')
		case c == '\'':
			p.s.ScanSimpleString('\'')
		case c == '[' && (p.s.PeekAt(1) == '[' || p.s.PeekAt(1) == '='):
			save := p.s.Pos
			if !scanLuaLongString(&p.s) {
				p.s.Pos = save
				p.s.Pos++
			}
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
		}
	}
}

func (p *luaParser) inFunction() bool {
	return p.stack.Any(func(k luaScopeKind) bool { return k == luaFunction })
}

func (p *luaParser) handleIdent(word []byte) {
	switch string(word) {
	case "function":
		p.parseFunction()
	case "local":
		p.handleLocal()
	case "require":
		p.parseRequire()
	case "if", "do":
		// Block openers balanced by "end". Note: while/for are NOT here
		// because their "do" keyword is the actual block opener — counting
		// both would double-increment.
		p.blockDepth++
	case "repeat":
		// repeat...until — until doesn't use "end", but we track the depth
		// to avoid confusion. We pop on "until".
		p.blockDepth++
	case "until":
		if p.blockDepth > 0 {
			p.blockDepth--
		}
	case "end":
		p.handleEnd()
	}
}

// handleLocal looks for "local function name" and records it.
func (p *luaParser) handleLocal() {
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	save := p.s.Pos
	w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	if string(w) == "function" {
		p.parseFunction()
	} else {
		// Check for require in: local x = require(...)
		p.s.Pos = save
	}
}

// parseFunction parses a function declaration after the "function" keyword.
// Handles: name, module.name, module:name
func (p *luaParser) parseFunction() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() {
		return
	}

	// Check for anonymous function (next token is '(')
	if p.s.Peek() == '(' {
		// Anonymous — skip the body
		p.s.SkipBalanced('(', ')', luaStringScanner)
		p.skipFunctionBody()
		return
	}

	if !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}

	// Scan name, possibly dotted or colon-separated
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	for !p.s.EOF() && (p.s.Peek() == '.' || p.s.Peek() == ':') {
		p.s.Pos++ // consume '.' or ':'
		if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
			break
		}
		name = string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	}

	// Skip parameters
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', luaStringScanner)
	}

	// Only emit at file scope (not inside another function)
	if p.inFunction() {
		// Skip body without recording
		p.skipFunctionBody()
		return
	}

	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, LuaSymbol{
		Type: "function", Name: name, StartLine: startLine, Parent: parent,
	})
	// Push function scope — will be closed by matching "end"
	p.stack.Push(lexkit.Scope[luaScopeKind]{
		Data: luaFunction, SymIdx: sym, OpenLine: startLine,
	})
}

// skipFunctionBody skips tokens until the matching "end", accounting for
// nested keyword-terminated blocks.
func (p *luaParser) skipFunctionBody() {
	depth := 1
	for !p.s.EOF() && depth > 0 {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.s.Pos++
		case c == '\n':
			p.s.Next()
		case c == '-' && p.s.PeekAt(1) == '-':
			p.s.Advance(2)
			if !p.s.EOF() && p.s.Peek() == '[' {
				save := p.s.Pos
				if scanLuaLongString(&p.s) {
					continue
				}
				p.s.Pos = save
			}
			p.s.SkipLineComment()
		case c == '"':
			p.s.ScanSimpleString('"')
		case c == '\'':
			p.s.ScanSimpleString('\'')
		case c == '[' && (p.s.PeekAt(1) == '[' || p.s.PeekAt(1) == '='):
			save := p.s.Pos
			if !scanLuaLongString(&p.s) {
				p.s.Pos = save
				p.s.Pos++
			}
		case lexkit.DefaultIdentStart[c]:
			w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			switch string(w) {
			case "function", "if", "do":
				depth++
			case "repeat":
				depth++ // repeat...until pair
			case "until":
				depth--
			case "end":
				depth--
			}
		default:
			p.s.Pos++
		}
	}
}

func (p *luaParser) handleEnd() {
	if p.blockDepth > 0 {
		// Closes a non-function block (if/for/while/do)
		p.blockDepth--
		return
	}
	// Closes a function scope on the stack
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
}

// parseRequire extracts the module path from require("mod") or require "mod".
func (p *luaParser) parseRequire() {
	line := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() {
		return
	}
	c := p.s.Peek()
	var path string
	if c == '(' {
		p.s.Pos++ // consume '('
		p.skipWSAndComments()
		if !p.s.EOF() {
			path = p.scanStringLiteral()
		}
		// skip to closing paren
		for !p.s.EOF() && p.s.Peek() != ')' && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		if !p.s.EOF() && p.s.Peek() == ')' {
			p.s.Pos++
		}
	} else if c == '"' || c == '\'' {
		path = p.scanStringLiteral()
	}
	if path != "" {
		p.result.Imports = append(p.result.Imports, LuaImport{Path: path, Line: line})
	}
}

// scanStringLiteral scans a quoted string and returns its content (unescaped).
func (p *luaParser) scanStringLiteral() string {
	if p.s.EOF() {
		return ""
	}
	quote := p.s.Peek()
	if quote != '"' && quote != '\'' {
		return ""
	}
	p.s.Pos++
	start := p.s.Pos
	for !p.s.EOF() {
		ch := p.s.Peek()
		if ch == quote {
			end := p.s.Pos
			p.s.Pos++
			return string(p.s.Src[start:end])
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
	return ""
}

func (p *luaParser) skipWSAndComments() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' {
			p.s.Pos++
			continue
		}
		if c == '-' && p.s.PeekAt(1) == '-' {
			p.s.Advance(2)
			if !p.s.EOF() && p.s.Peek() == '[' {
				save := p.s.Pos
				if scanLuaLongString(&p.s) {
					continue
				}
				p.s.Pos = save
			}
			p.s.SkipLineComment()
			continue
		}
		break
	}
}
