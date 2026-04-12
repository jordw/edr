package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParsePython is a hand-written Python symbol + import extractor built
// on lexkit primitives.
//
// Handles:
//   - def / async def functions
//   - class declarations
//   - method detection (def inside class scope)
//   - import X, import X.Y, import X as Y, import X, Y
//   - from X import Y, from X import (Y, Z), from . import X
//   - line comments (#), single/double/triple-quoted strings
//   - string prefixes (r, b, f, u, rb, fr, etc.)
//   - decorators (@name)
//   - indent-based scope tracking (tab = 8 spaces)
//
// Scope is tracked by indent level: a def/class at indent N establishes
// a scope that closes when the next non-blank line at indent <= N
// appears. Nested defs inside functions are still recorded but with the
// enclosing function as their parent.
//
// Known gaps:
//   - Destructuring / tuple assignment not recorded as variables
//   - f-string {expr} interpolation is scanned as literal text
//     (harmless — we'd skip expression tokens anyway)
//   - Line continuations with backslash not tracked for statement
//     boundaries (rare at module scope)

type PyResult struct {
	Symbols []PySymbol
	Imports []PyImport
}

type PySymbol struct {
	Type      string // "function" | "method" | "class"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type PyImport struct {
	Path string
	Line int
}

func ParsePython(src []byte) PyResult {
	p := &pyParser{s: lexkit.New(src), atLineStart: true}
	p.run()
	lastLine := p.s.Line
	for len(p.stack) > 0 {
		sc := p.stack[len(p.stack)-1]
		p.stack = p.stack[:len(p.stack)-1]
		if sc.symIdx >= 0 && p.result.Symbols[sc.symIdx].EndLine == 0 {
			p.result.Symbols[sc.symIdx].EndLine = lastLine
		}
	}
	return p.result
}

type pyParser struct {
	s           lexkit.Scanner
	result      PyResult
	stack       []pyScope
	atLineStart bool
}

type pyScope struct {
	indent  int
	symIdx  int
	isClass bool
}

func (p *pyParser) run() {
	for !p.s.EOF() {
		if p.atLineStart {
			p.handleLineStart()
			continue
		}
		c := p.s.Peek()
		switch {
		case c == '\n':
			p.s.Next()
			p.atLineStart = true
		case c == '#':
			p.s.SkipLineComment()
		case c == '\'':
			p.scanPyString()
		case c == '"':
			p.scanPyString()
		case lexkit.DefaultIdentStart[c]:
			// In body mode, fast-skip the ident without the full scan.
			// String prefixes (r"...", f'...', etc.) don't need special
			// handling — when we hit the quote byte next, scanPyString
			// skips the body regardless of escape semantics.
			p.s.Pos++
			for p.s.Pos < len(p.s.Src) && lexkit.DefaultIdentCont[p.s.Src[p.s.Pos]] {
				p.s.Pos++
			}
		case lexkit.IsASCIIDigit(c):
			for !p.s.EOF() && (lexkit.IsASCIIDigit(p.s.Peek()) || p.s.Peek() == '.' || p.s.Peek() == '_') {
				p.s.Pos++
			}
		default:
			p.s.Pos++
		}
	}
}

// scanPyString handles both single-line and triple-quoted strings.
func (p *pyParser) scanPyString() {
	c := p.s.Peek()
	if c != '"' && c != '\'' {
		return
	}
	if p.s.PeekAt(1) == c && p.s.PeekAt(2) == c {
		quote := c
		p.s.Advance(3)
		for !p.s.EOF() {
			if p.s.Peek() == quote && p.s.PeekAt(1) == quote && p.s.PeekAt(2) == quote {
				p.s.Advance(3)
				return
			}
			if p.s.Peek() == '\\' && p.s.Pos+1 < len(p.s.Src) {
				if p.s.PeekAt(1) == '\n' {
					p.s.Line++
				}
				p.s.Pos += 2
				continue
			}
			p.s.Next()
		}
		return
	}
	p.s.ScanSimpleString(c)
}

func (p *pyParser) handleLineStart() {
	indent := p.measureIndent()
	if p.s.EOF() {
		return
	}
	c := p.s.Peek()
	if c == '\n' {
		p.s.Next()
		return
	}
	if c == '#' {
		p.s.SkipLineComment()
		return
	}
	p.atLineStart = false
	p.popAbove(indent)
	if c == '@' {
		// Decorator — skip the rest of the line, but stay at a state
		// where the next line is still "line start".
		for !p.s.EOF() && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		return
	}
	if !lexkit.DefaultIdentStart[c] {
		return
	}
	word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	switch string(word) {
	case "def":
		p.parseDef(indent, false)
	case "async":
		p.s.SkipSpaces()
		next := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(next) == "def" {
			p.parseDef(indent, true)
		}
	case "class":
		p.parseClass(indent)
	case "import":
		p.parseImport()
	case "from":
		p.parseFromImport()
	}
}

// measureIndent advances past leading whitespace on the current line
// and returns the indent width. Tabs count as 8 spaces.
func (p *pyParser) measureIndent() int {
	indent := 0
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ' ' {
			indent++
			p.s.Pos++
		} else if c == '\t' {
			indent += 8
			p.s.Pos++
		} else {
			break
		}
	}
	return indent
}

// popAbove pops all scopes whose indent is >= the given indent,
// setting their EndLine to the previous source line.
func (p *pyParser) popAbove(indent int) {
	for len(p.stack) > 0 && p.stack[len(p.stack)-1].indent >= indent {
		sc := p.stack[len(p.stack)-1]
		p.stack = p.stack[:len(p.stack)-1]
		if sc.symIdx >= 0 && p.result.Symbols[sc.symIdx].EndLine == 0 {
			endLine := p.s.Line - 1
			if endLine < p.result.Symbols[sc.symIdx].StartLine {
				endLine = p.result.Symbols[sc.symIdx].StartLine
			}
			p.result.Symbols[sc.symIdx].EndLine = endLine
		}
	}
}

func (p *pyParser) parseDef(indent int, isAsync bool) {
	_ = isAsync
	startLine := p.s.Line
	p.s.SkipSpaces()
	if p.s.EOF() || !!p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Match regex convention: any def with an enclosing scope (class or
	// function) is a "method". Only top-level defs are "function".
	kind := "function"
	parent := -1
	if len(p.stack) > 0 {
		parent = p.stack[len(p.stack)-1].symIdx
		kind = "method"
	}
	idx := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, PySymbol{
		Type: kind, Name: name, StartLine: startLine, Parent: parent,
	})
	p.stack = append(p.stack, pyScope{indent: indent, symIdx: idx, isClass: false})
	p.skipToStmtEnd()
}

func (p *pyParser) parseClass(indent int) {
	startLine := p.s.Line
	p.s.SkipSpaces()
	if p.s.EOF() || !!p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := -1
	if len(p.stack) > 0 {
		parent = p.stack[len(p.stack)-1].symIdx
	}
	idx := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, PySymbol{
		Type: "class", Name: name, StartLine: startLine, Parent: parent,
	})
	p.stack = append(p.stack, pyScope{indent: indent, symIdx: idx, isClass: true})
	p.skipToStmtEnd()
}

// skipToStmtEnd advances past a def/class header which may span
// multiple lines if parens/brackets/braces are unclosed. Stops at the
// newline that terminates the statement.
func (p *pyParser) skipToStmtEnd() {
	depth := 0
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '\n' && depth == 0 {
			p.s.Next()
			p.atLineStart = true
			return
		}
		switch {
		case c == '(' || c == '[' || c == '{':
			depth++
			p.s.Pos++
		case c == ')' || c == ']' || c == '}':
			depth--
			p.s.Pos++
		case c == '\n':
			p.s.Next()
		case c == '#':
			p.s.SkipLineComment()
		case c == '\'' || c == '"':
			p.scanPyString()
		default:
			p.s.Pos++
		}
	}
}

func (p *pyParser) parseImport() {
	startLine := p.s.Line
	p.s.SkipSpaces()
	for !p.s.EOF() {
		if !!p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
			break
		}
		start := p.s.Pos
		for !p.s.EOF() {
			if !!p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				break
			}
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			if !p.s.EOF() && p.s.Peek() == '.' {
				p.s.Pos++
				continue
			}
			break
		}
		path := string(p.s.Src[start:p.s.Pos])
		if path != "" {
			p.result.Imports = append(p.result.Imports, PyImport{Path: path, Line: startLine})
		}
		p.s.SkipSpaces()
		// Optional "as alias"
		if !p.s.EOF() && p.s.Peek() == 'a' {
			save := p.s.Pos
			asWord := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			if string(asWord) == "as" {
				p.s.SkipSpaces()
				p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			} else {
				p.s.Pos = save
			}
		}
		p.s.SkipSpaces()
		if !p.s.EOF() && p.s.Peek() == ',' {
			p.s.Pos++
			p.s.SkipSpaces()
			continue
		}
		break
	}
	for !p.s.EOF() && p.s.Peek() != '\n' {
		p.s.Pos++
	}
}

func (p *pyParser) parseFromImport() {
	startLine := p.s.Line
	p.s.SkipSpaces()
	start := p.s.Pos
	// Leading dots for relative imports
	for !p.s.EOF() && p.s.Peek() == '.' {
		p.s.Pos++
	}
	for !p.s.EOF() && !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if !p.s.EOF() && p.s.Peek() == '.' {
			p.s.Pos++
			continue
		}
		break
	}
	path := string(p.s.Src[start:p.s.Pos])
	if path != "" {
		p.result.Imports = append(p.result.Imports, PyImport{Path: path, Line: startLine})
	}
	// Skip rest of line, allowing parens to span lines.
	depth := 0
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '\n' && depth == 0 {
			return
		}
		switch {
		case c == '(':
			depth++
			p.s.Pos++
		case c == ')':
			depth--
			p.s.Pos++
		case c == '\n':
			p.s.Next()
		case c == '#':
			p.s.SkipLineComment()
		default:
			p.s.Pos++
		}
	}
}