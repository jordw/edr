package index

import (
	"strings"

	"github.com/jordw/edr/internal/lexkit"
)

// ParseGo is a hand-written Go symbol + import extractor built on
// lexkit primitives.
//
// Handles:
//   - package declaration
//   - import (single and block, with aliases)
//   - func (free and methods with receivers, incl. pointer and generic)
//   - type (single and block, struct/interface/alias/generic)
//   - const / var (single and block, multi-name declarations)
//   - line + block comments, double/single/backtick strings
//
// Because all Go symbols of interest live at top level and have clear
// brace-delimited bodies, this parser never descends into function
// bodies: it consumes them wholesale via SkipBalanced. No scope stack
// is needed.
//
// Known gaps:
//   - Multi-name const/var declarations only record the first name
//   - Struct fields are not recorded as symbols
//   - Interface method definitions are not recorded as symbols

type GoResult struct {
	Symbols []GoSymbol
	Imports []GoImport
}

type GoSymbol struct {
	Type      string // "function" | "method" | "type" | "const" | "var"
	Name      string
	Receiver  string // for methods, the bare type name (no '*')
	StartLine int
	EndLine   int
}

type GoImport struct {
	Path  string
	Alias string // "", ".", "_", or alias name
	Line  int
}

func ParseGo(src []byte) GoResult {
	p := &goParser{s: lexkit.New(src)}
	p.run()
	return p.result
}

type goParser struct {
	s      lexkit.Scanner
	result GoResult
}

// goStringScanner is the StringScanner callback for SkipBalanced. It
// also skips line and block comments.
func goStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		s.ScanSimpleString('"')
		return true
	case '\'':
		s.ScanSimpleString('\'')
		return true
	case '`':
		// Go raw string — no escapes, can span lines.
		s.Pos++
		for !s.EOF() && s.Peek() != '`' {
			s.Next()
		}
		if !s.EOF() {
			s.Pos++
		}
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

func (p *goParser) run() {
	for !p.s.EOF() {
		if goStringScanner(&p.s) {
			continue
		}
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.s.Next()
		case lexkit.IsDefaultIdentStart(c):
			word := p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
			p.handleKeyword(word)
		default:
			p.s.Pos++
		}
	}
}

func (p *goParser) handleKeyword(word []byte) {
	switch string(word) {
	case "import":
		p.parseImport()
	case "func":
		p.parseFunc()
	case "type":
		p.parseType()
	case "const":
		p.parseConstOrVar("constant")
	case "var":
		p.parseConstOrVar("variable")
	}
}

func (p *goParser) skipWSAndComments() {
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

func (p *goParser) parseImport() {
	p.s.SkipSpaces()
	if p.s.EOF() {
		return
	}
	if p.s.Peek() == '(' {
		p.s.Pos++
		for !p.s.EOF() {
			p.skipWSAndComments()
			if p.s.EOF() {
				return
			}
			if p.s.Peek() == ')' {
				p.s.Pos++
				return
			}
			p.parseOneImport()
		}
		return
	}
	p.parseOneImport()
}

func (p *goParser) parseOneImport() {
	line := p.s.Line
	p.s.SkipSpaces()
	// Optional alias: identifier, _, or .
	var alias string
	if !p.s.EOF() {
		c := p.s.Peek()
		if c == '.' {
			alias = "."
			p.s.Pos++
			p.s.SkipSpaces()
		} else if c == '_' {
			alias = "_"
			p.s.Pos++
			p.s.SkipSpaces()
		} else if lexkit.IsDefaultIdentStart(c) {
			// Lookahead: is this an alias followed by a string, or something else?
			save := p.s.Pos
			id := p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
			p.s.SkipSpaces()
			if !p.s.EOF() && p.s.Peek() == '"' {
				alias = string(id)
			} else {
				p.s.Pos = save
			}
		}
	}
	if !p.s.EOF() && p.s.Peek() == '"' {
		p.s.Pos++
		start := p.s.Pos
		for !p.s.EOF() && p.s.Peek() != '"' && p.s.Peek() != '\n' {
			p.s.Pos++
		}
		path := string(p.s.Src[start:p.s.Pos])
		if !p.s.EOF() && p.s.Peek() == '"' {
			p.s.Pos++
		}
		if path != "" {
			p.result.Imports = append(p.result.Imports, GoImport{Path: path, Alias: alias, Line: line})
		}
	}
	for !p.s.EOF() && p.s.Peek() != '\n' {
		p.s.Pos++
	}
}

func (p *goParser) parseFunc() {
	startLine := p.s.Line
	p.s.SkipSpaces()
	var receiver string
	if !p.s.EOF() && p.s.Peek() == '(' {
		recvStart := p.s.Pos
		p.s.SkipBalanced('(', ')', goStringScanner)
		receiver = extractReceiverType(p.s.Src[recvStart:p.s.Pos])
		p.s.SkipSpaces()
	}
	if p.s.EOF() || !lexkit.IsDefaultIdentStart(p.s.Peek()) {
		return
	}
	name := string(p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont))
	p.s.SkipSpaces()
	if !p.s.EOF() && p.s.Peek() == '[' {
		p.s.SkipBalanced('[', ']', goStringScanner)
	}
	p.s.SkipSpaces()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', goStringScanner)
	}
	// Skip optional return type(s) until we hit '{' or a newline-terminated
	// declaration (interface method, function declaration without body).
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '{' || c == '\n' {
			break
		}
		if c == '/' && p.s.PeekAt(1) == '/' {
			p.s.SkipLineComment()
			break
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', goStringScanner)
			continue
		}
		if c == '[' {
			p.s.SkipBalanced('[', ']', goStringScanner)
			continue
		}
		if goStringScanner(&p.s) {
			continue
		}
		p.s.Pos++
	}
	endLine := p.s.Line
	if !p.s.EOF() && p.s.Peek() == '{' {
		p.s.SkipBalanced('{', '}', goStringScanner)
		endLine = p.s.Line
	}
	kind := "function"
	if receiver != "" {
		kind = "method"
	}
	p.result.Symbols = append(p.result.Symbols, GoSymbol{
		Type: kind, Name: name, Receiver: receiver,
		StartLine: startLine, EndLine: endLine,
	})
}

// extractReceiverType parses a byte slice like "(r *Type)" or "(Type)"
// and returns the bare receiver type name ("Type"), stripping any
// leading '*' and trailing generic parameters.
func extractReceiverType(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, " "); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	s = strings.TrimPrefix(s, "*")
	if i := strings.Index(s, "["); i >= 0 {
		s = s[:i]
	}
	return s
}

func (p *goParser) parseType() {
	p.s.SkipSpaces()
	if p.s.EOF() {
		return
	}
	if p.s.Peek() == '(' {
		p.s.Pos++
		for !p.s.EOF() {
			p.skipWSAndComments()
			if p.s.EOF() {
				return
			}
			if p.s.Peek() == ')' {
				p.s.Pos++
				return
			}
			before := p.s.Pos
			p.parseOneType()
			if p.s.Pos == before {
				for !p.s.EOF() && p.s.Peek() != '\n' {
					p.s.Pos++
				}
				if !p.s.EOF() {
					p.s.Next()
				}
			}
		}
		return
	}
	p.parseOneType()
}

func (p *goParser) parseOneType() {
	startLine := p.s.Line
	p.s.SkipSpaces()
	if p.s.EOF() || !lexkit.IsDefaultIdentStart(p.s.Peek()) {
		return
	}
	name := string(p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont))
	p.s.SkipSpaces()
	if !p.s.EOF() && p.s.Peek() == '[' {
		p.s.SkipBalanced('[', ']', goStringScanner)
	}
	p.s.SkipSpaces()
	// Optional '=' for type aliases (`type Foo = Bar`)
	if !p.s.EOF() && p.s.Peek() == '=' {
		p.s.Pos++
		p.s.SkipSpaces()
	}
	// Detect kind by looking at the RHS: struct {, interface {, or other.
	kind := "type"
	if !p.s.EOF() && lexkit.IsDefaultIdentStart(p.s.Peek()) {
		save := p.s.Pos
		saveLine := p.s.Line
		kwd := p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
		switch string(kwd) {
		case "struct":
			kind = "struct"
		case "interface":
			kind = "interface"
		}
		p.s.Pos = save
		p.s.Line = saveLine
	}
	p.skipToDeclEnd()
	p.result.Symbols = append(p.result.Symbols, GoSymbol{
		Type: kind, Name: name, StartLine: startLine, EndLine: p.s.Line,
	})
}

func (p *goParser) parseConstOrVar(kind string) {
	p.s.SkipSpaces()
	if p.s.EOF() {
		return
	}
	if p.s.Peek() == '(' {
		p.s.Pos++
		for !p.s.EOF() {
			p.skipWSAndComments()
			if p.s.EOF() {
				return
			}
			if p.s.Peek() == ')' {
				p.s.Pos++
				return
			}
			before := p.s.Pos
			p.parseOneConstOrVar(kind)
			// Safety: if the sub-parser couldn't advance, force progress
			// by consuming the rest of the line.
			if p.s.Pos == before {
				for !p.s.EOF() && p.s.Peek() != '\n' {
					p.s.Pos++
				}
				if !p.s.EOF() {
					p.s.Next()
				}
			}
		}
		return
	}
	p.parseOneConstOrVar(kind)
}

func (p *goParser) parseOneConstOrVar(kind string) {
	startLine := p.s.Line
	p.s.SkipSpaces()
	if p.s.EOF() || !lexkit.IsDefaultIdentStart(p.s.Peek()) {
		return
	}
	name := string(p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont))
	p.skipToDeclEnd()
	p.result.Symbols = append(p.result.Symbols, GoSymbol{
		Type: kind, Name: name, StartLine: startLine, EndLine: p.s.Line,
	})
}

// skipToDeclEnd advances past a declaration's RHS until we reach a
// newline at bracket/brace depth 0. String and comment tokens are
// skipped so their contents don't affect depth.
func (p *goParser) skipToDeclEnd() {
	depth := 0
	for !p.s.EOF() {
		if goStringScanner(&p.s) {
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
		case c == '\n':
			if depth == 0 {
				p.s.Next()
				return
			}
			p.s.Next()
		default:
			p.s.Pos++
		}
	}
}