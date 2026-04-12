package index

import (
	"strings"

	"github.com/jordw/edr/internal/lexkit"
)

// ParseRuby is a hand-written Ruby symbol + import extractor built on
// lexkit primitives.
//
// Handles:
//   - class / module / method declarations (incl. self. receivers)
//   - Ruby 3.0 endless methods (def foo = expr)
//   - nested blocks via class/module/def/if/unless/while/until/case/begin/for/do ... end
//   - single-line modifier forms (x if y) do NOT open a block
//   - require / require_relative / load imports
//   - line comments (#), single/double/backtick strings, string interpolation
//   - heredocs: <<TAG, <<-TAG, <<~TAG with optional quoted tags
//   - regex vs division disambiguation
//   - symbols (:foo, :"foo")
//
// Known gaps:
//   - %q{} %Q{} %w[] %i[] literals
//   - =begin/=end pod comments
//   - class << self singleton class shorthand
//   - complex operator method names (<=>, []=, +@) partially handled
//   - __END__ sentinel

type RubyResult struct {
	Symbols []RubySymbol
	Imports []RubyImport
}

type RubySymbol struct {
	Type      string
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type RubyImport struct {
	Kind string
	Path string
	Line int
}

func ParseRuby(src []byte) RubyResult {
	p := &rbParser{s: lexkit.New(src), stmtStart: true, regexOK: true}
	p.run()
	// Close any still-open scopes at EOF.
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type rbParser struct {
	s               lexkit.Scanner
	stack           lexkit.ScopeStack[struct{}]
	result          RubyResult
	regexOK         bool
	stmtStart       bool
	pendingHeredocs []rbHeredoc
}

type rbHeredoc struct {
	tag      string
	squiggly bool
	interp   bool
}

// rbSkipInterp consumes the body of a Ruby "#{...}" interpolation up to
// the matching '}'. Used as the onInterp callback for double-quoted and
// backtick strings.
func rbSkipInterp(s *lexkit.Scanner) {
	depth := 1
	for !s.EOF() && depth > 0 {
		c := s.Peek()
		switch c {
		case '{':
			depth++
			s.Pos++
		case '}':
			depth--
			s.Pos++
		case '"':
			s.ScanInterpolatedString('"', "#{", rbSkipInterp)
		case '\'':
			s.ScanSimpleString('\'')
		case '\n':
			s.Next()
		default:
			s.Pos++
		}
	}
}

// rbStringScanner is the StringScanner callback for SkipBalanced.
func rbStringScanner(s *lexkit.Scanner) bool {
	switch s.Peek() {
	case '"':
		s.ScanInterpolatedString('"', "#{", rbSkipInterp)
		return true
	case '\'':
		s.ScanSimpleString('\'')
		return true
	case '#':
		s.SkipLineComment()
		return true
	}
	return false
}

func (p *rbParser) run() {
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.s.Pos++
		case c == '\\' && p.s.PeekAt(1) == '\n':
			p.s.Advance(2)
		case c == '\n':
			if len(p.pendingHeredocs) > 0 {
				p.s.Next()
				hs := p.pendingHeredocs
				p.pendingHeredocs = nil
				for _, h := range hs {
					p.readHeredocBody(h)
				}
			} else {
				p.s.Next()
			}
			p.stmtStart = true
			p.regexOK = true
		case c == ';':
			p.s.Pos++
			p.stmtStart = true
			p.regexOK = true
		case c == '#':
			p.s.SkipLineComment()
		case c == '"':
			p.s.ScanInterpolatedString('"', "#{", rbSkipInterp)
			p.regexOK = false
			p.stmtStart = false
		case c == '\'':
			p.s.ScanSimpleString('\'')
			p.regexOK = false
			p.stmtStart = false
		case c == '`':
			p.s.ScanInterpolatedString('`', "#{", rbSkipInterp)
			p.regexOK = false
			p.stmtStart = false
		case c == '/' && p.regexOK:
			p.s.ScanSlashRegex()
			p.regexOK = false
			p.stmtStart = false
		case c == '<' && p.s.PeekAt(1) == '<' && p.regexOK:
			if !p.tryHeredocTag() {
				p.s.Advance(2)
				p.regexOK = true
				p.stmtStart = false
			}
		case c == ':' && (lexkit.IsDefaultIdentStart(p.s.PeekAt(1)) || p.s.PeekAt(1) == '"' || p.s.PeekAt(1) == '\''):
			p.s.Pos++
			switch p.s.Peek() {
			case '"':
				p.s.ScanInterpolatedString('"', "#{", rbSkipInterp)
			case '\'':
				p.s.ScanSimpleString('\'')
			default:
				p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
			}
			p.regexOK = false
			p.stmtStart = false
		case lexkit.IsDefaultIdentStart(c):
			word := p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
			p.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !p.s.EOF() {
				cc := p.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '_' && cc != '.' {
					break
				}
				p.s.Pos++
			}
			p.regexOK = false
			p.stmtStart = false
		case c == ')' || c == ']' || c == '}':
			p.s.Pos++
			p.regexOK = false
			p.stmtStart = false
		default:
			p.s.Pos++
			p.regexOK = true
			p.stmtStart = false
		}
	}
}

func (p *rbParser) handleIdent(word []byte) {
	if p.stmtStart {
		switch string(word) {
		case "class":
			p.parseClassOrModule("class")
			return
		case "module":
			p.parseClassOrModule("module")
			return
		case "def":
			p.parseDef()
			return
		case "if", "unless", "while", "until", "case", "begin", "for":
			p.stack.Push(lexkit.Scope[struct{}]{SymIdx: -1, OpenLine: p.s.Line})
			p.stmtStart = false
			p.regexOK = true
			return
		case "end":
			p.popBlock()
			p.stmtStart = false
			p.regexOK = false
			return
		case "require", "require_relative", "load":
			p.parseRequire(string(word))
			return
		}
	} else {
		switch string(word) {
		case "do":
			p.stack.Push(lexkit.Scope[struct{}]{SymIdx: -1, OpenLine: p.s.Line})
			p.regexOK = true
			return
		case "end":
			p.popBlock()
			p.regexOK = false
			return
		}
	}
	switch string(word) {
	case "return", "and", "or", "not", "in", "then", "else", "elsif", "when", "yield":
		p.regexOK = true
	default:
		p.regexOK = false
	}
	p.stmtStart = false
}

func (p *rbParser) popBlock() {
	e, ok := p.stack.Pop()
	if !ok {
		return
	}
	if e.SymIdx >= 0 {
		p.result.Symbols[e.SymIdx].EndLine = p.s.Line
	}
}

func (p *rbParser) parseClassOrModule(kind string) {
	startLine := p.s.Line
	p.s.SkipSpaces()
	name := p.scanQualifiedIdent()
	if name == "" {
		p.stmtStart = false
		p.regexOK = false
		return
	}
	// For `class Foo::Bar::Baz`, Ruby is defining `Baz` inside the
	// Foo::Bar namespace. Emit just the leaf name so symbol search finds
	// the actual class rather than the namespace prefix.
	if i := strings.LastIndex(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	parent := p.stack.NearestSym()
	idx := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RubySymbol{
		Type: kind, Name: name, StartLine: startLine, Parent: parent,
	})
	p.stack.Push(lexkit.Scope[struct{}]{SymIdx: idx, OpenLine: startLine})
	p.stmtStart = false
	p.regexOK = true
}

func (p *rbParser) parseDef() {
	startLine := p.s.Line
	p.s.SkipSpaces()
	save := p.s.Pos
	if !p.s.EOF() && lexkit.IsDefaultIdentStart(p.s.Peek()) {
		_ = p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
		ws := 0
		for p.s.Pos+ws < len(p.s.Src) && (p.s.Src[p.s.Pos+ws] == ' ' || p.s.Src[p.s.Pos+ws] == '\t') {
			ws++
		}
		if p.s.Pos+ws < len(p.s.Src) && p.s.Src[p.s.Pos+ws] == '.' {
			p.s.Advance(ws + 1)
			p.s.SkipSpaces()
		} else {
			name := string(p.s.Src[save:p.s.Pos])
			if !p.s.EOF() && (p.s.Peek() == '?' || p.s.Peek() == '!') {
				name += string(p.s.Peek())
				p.s.Pos++
			} else if p.s.Peek() == '=' && p.s.PeekAt(1) == '(' {
				name += "="
				p.s.Pos++
			}
			p.finalizeDef(name, startLine)
			return
		}
	}
	name := p.scanMethodName()
	if name == "" {
		p.stmtStart = false
		p.regexOK = false
		return
	}
	p.finalizeDef(name, startLine)
}

func (p *rbParser) scanMethodName() string {
	if p.s.EOF() || !lexkit.IsDefaultIdentStart(p.s.Peek()) {
		return ""
	}
	start := p.s.Pos
	p.s.Pos++
	for !p.s.EOF() && lexkit.IsDefaultIdentCont(p.s.Peek()) {
		p.s.Pos++
	}
	if !p.s.EOF() {
		c := p.s.Peek()
		if c == '?' || c == '!' {
			p.s.Pos++
		} else if c == '=' && p.s.PeekAt(1) == '(' {
			p.s.Pos++
		}
	}
	return string(p.s.Src[start:p.s.Pos])
}

func (p *rbParser) finalizeDef(name string, startLine int) {
	parent := p.stack.NearestSym()
	p.s.SkipSpaces()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', rbStringScanner)
	}
	p.s.SkipSpaces()
	if !p.s.EOF() && p.s.Peek() == '=' && p.s.PeekAt(1) != '=' && p.s.PeekAt(1) != '>' {
		p.result.Symbols = append(p.result.Symbols, RubySymbol{
			Type: "method", Name: name, StartLine: startLine, EndLine: startLine, Parent: parent,
		})
		p.s.Pos++
		p.stmtStart = false
		p.regexOK = true
		return
	}
	idx := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RubySymbol{
		Type: "method", Name: name, StartLine: startLine, Parent: parent,
	})
	p.stack.Push(lexkit.Scope[struct{}]{SymIdx: idx, OpenLine: startLine})
	p.stmtStart = false
	p.regexOK = true
}

func (p *rbParser) parseRequire(kind string) {
	startLine := p.s.Line
	p.s.SkipSpaces()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.Pos++
		p.s.SkipSpaces()
	}
	if p.s.EOF() {
		return
	}
	c := p.s.Peek()
	var path string
	switch c {
	case '\'':
		p.s.Pos++
		start := p.s.Pos
		for !p.s.EOF() && p.s.Peek() != '\'' && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		path = string(p.s.Src[start:p.s.Pos])
		if !p.s.EOF() && p.s.Peek() == '\'' {
			p.s.Pos++
		}
	case '"':
		p.s.Pos++
		start := p.s.Pos
		for !p.s.EOF() && p.s.Peek() != '"' && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		path = string(p.s.Src[start:p.s.Pos])
		if !p.s.EOF() && p.s.Peek() == '"' {
			p.s.Pos++
		}
	default:
		p.stmtStart = false
		p.regexOK = false
		return
	}
	if path != "" {
		p.result.Imports = append(p.result.Imports, RubyImport{Kind: kind, Path: path, Line: startLine})
	}
	p.stmtStart = false
	p.regexOK = false
}

func (p *rbParser) tryHeredocTag() bool {
	src := p.s.Src
	j := p.s.Pos + 2
	squiggly := false
	if j < len(src) && src[j] == '~' {
		squiggly = true
		j++
	} else if j < len(src) && src[j] == '-' {
		j++
	}
	var quote byte
	interp := true
	if j < len(src) && (src[j] == '\'' || src[j] == '"') {
		quote = src[j]
		if quote == '\'' {
			interp = false
		}
		j++
	}
	if j >= len(src) || !lexkit.IsDefaultIdentStart(src[j]) {
		return false
	}
	tagStart := j
	for j < len(src) && lexkit.IsDefaultIdentCont(src[j]) {
		j++
	}
	tag := string(src[tagStart:j])
	if quote != 0 {
		if j >= len(src) || src[j] != quote {
			return false
		}
		j++
	}
	p.s.Pos = j
	p.pendingHeredocs = append(p.pendingHeredocs, rbHeredoc{tag: tag, squiggly: squiggly, interp: interp})
	p.regexOK = false
	p.stmtStart = false
	return true
}

func (p *rbParser) readHeredocBody(h rbHeredoc) {
	for !p.s.EOF() {
		lineStart := p.s.Pos
		for !p.s.EOF() && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		lineText := string(p.s.Src[lineStart:p.s.Pos])
		check := strings.TrimLeft(lineText, " \t")
		trimmed := strings.TrimRight(check, " \t\r")
		if !p.s.EOF() {
			p.s.Next()
		}
		if trimmed == h.tag {
			return
		}
	}
}

func (p *rbParser) scanQualifiedIdent() string {
	start := p.s.Pos
	for !p.s.EOF() {
		if !lexkit.IsDefaultIdentStart(p.s.Peek()) {
			break
		}
		for !p.s.EOF() && lexkit.IsDefaultIdentCont(p.s.Peek()) {
			p.s.Pos++
		}
		if p.s.Pos+1 < len(p.s.Src) && p.s.Peek() == ':' && p.s.PeekAt(1) == ':' {
			p.s.Pos += 2
			continue
		}
		break
	}
	return string(p.s.Src[start:p.s.Pos])
}