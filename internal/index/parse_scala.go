package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseScala is a hand-written Scala symbol + import extractor.
//
// Handles:
//   - class / abstract class / case class / sealed class
//   - object / case object (singletons)
//   - trait
//   - def (methods/functions)
//   - type aliases
//   - import statements (dotted paths, groups, wildcards)
//   - package declaration
//   - generics [T] (square brackets)
//   - line + block comments
//   - strings: regular, triple-quoted, interpolated (s"...", f"...", raw"...")
//   - annotations (@name)
//
// Known gaps:
//   - val/var declarations not recorded
//   - Scala 3 indent-based syntax not supported
//   - given/using (Scala 3) not recognized
//   - Pattern-matching declarations not tracked

type ScalaResult struct {
	Symbols []ScalaSymbol
	Imports []ScalaImport
}

type ScalaSymbol struct {
	Type      string // "class" | "interface" | "function" | "type"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type ScalaImport struct {
	Path string
	Line int
}

type scalaScopeKind int

const (
	scalaBlock    scalaScopeKind = iota
	scalaClass
	scalaObject
	scalaTrait
	scalaFunction
)

func ParseScala(src []byte) ScalaResult {
	p := &scalaParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type scalaParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[scalaScopeKind]
	result       ScalaResult
	pendingScope *lexkit.Scope[scalaScopeKind]
	memberStart  bool
}

func scalaStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		if s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			s.Advance(3)
			for !s.EOF() {
				if s.Peek() == '"' && s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
					s.Advance(3)
					return true
				}
				s.Next()
			}
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

func (p *scalaParser) run() {
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
				p.s.Advance(3)
				for !p.s.EOF() {
					if p.s.Peek() == '"' && p.s.PeekAt(1) == '"' && p.s.PeekAt(2) == '"' {
						p.s.Advance(3)
						break
					}
					p.s.Next()
				}
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
		case c == '@':
			p.s.Pos++
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
			if !p.s.EOF() && p.s.Peek() == '(' {
				p.s.SkipBalanced('(', ')', scalaStringScanner)
			}
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *scalaParser) currentKind() scalaScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return scalaBlock
}

func (p *scalaParser) inFunction() bool {
	return p.stack.Any(func(k scalaScopeKind) bool { return k == scalaFunction })
}

func (p *scalaParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[scalaScopeKind]{Data: scalaBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == scalaFunction {
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if scalaStringScanner(&p.s) {
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

func (p *scalaParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
}

func (p *scalaParser) handleIdent(word []byte) {
	switch string(word) {
	case "class":
		if !p.inFunction() {
			p.parseTypeDecl("class")
		}
		return
	case "trait":
		if !p.inFunction() {
			p.parseTypeDecl("interface")
		}
		return
	case "object":
		if !p.inFunction() {
			p.parseTypeDecl("class")
		}
		return
	case "def":
		if !p.inFunction() {
			p.parseDef()
		}
		return
	case "type":
		if !p.inFunction() {
			p.parseTypeAlias()
		}
		return
	case "import":
		p.parseImport()
		return
	case "package":
		p.parsePackage()
		return
	case "case":
		// case class / case object
		p.skipWSAndComments()
		if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
			next := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			switch string(next) {
			case "class":
				if !p.inFunction() {
					p.parseTypeDecl("class")
				}
				return
			case "object":
				if !p.inFunction() {
					p.parseTypeDecl("class")
				}
				return
			}
		}
		return
	case "sealed", "abstract", "final", "implicit", "lazy",
		"override", "private", "protected", "public",
		"extends", "with":
		// modifiers — keep memberStart
		return
	}
	p.memberStart = false
}

func (p *scalaParser) parseTypeDecl(kind string) {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, ScalaSymbol{
		Type: kind, Name: name, StartLine: startLine, Parent: parent,
	})
	scopeKind := scalaClass
	if kind == "interface" {
		scopeKind = scalaTrait
	}
	p.pendingScope = &lexkit.Scope[scalaScopeKind]{Data: scopeKind, SymIdx: sym, OpenLine: startLine}
	// Skip generics [T], constructor params (...), extends/with until {
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			return
		}
		if c == '[' {
			p.s.SkipBalanced('[', ']', scalaStringScanner)
			continue
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', scalaStringScanner)
			continue
		}
		if c == '\n' {
			// No body — singleton or bodyless declaration
			p.result.Symbols[sym].EndLine = p.s.Line
			p.pendingScope = nil
			return
		}
		p.s.Pos++
	}
}

func (p *scalaParser) parseDef() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, ScalaSymbol{
		Type: "function", Name: name, StartLine: startLine, Parent: parent,
	})
	// Skip generics, params, return type until { or =
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			p.pendingScope = &lexkit.Scope[scalaScopeKind]{Data: scalaFunction, SymIdx: sym, OpenLine: startLine}
			return
		}
		if c == '=' {
			p.s.Pos++
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == '{' {
				p.pendingScope = &lexkit.Scope[scalaScopeKind]{Data: scalaFunction, SymIdx: sym, OpenLine: startLine}
				return
			}
			p.skipToExprEnd()
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == '[' {
			p.s.SkipBalanced('[', ']', scalaStringScanner)
			continue
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', scalaStringScanner)
			continue
		}
		if c == ':' {
			p.s.Pos++
			continue
		}
		if c == '\n' || c == '}' || c == ';' {
			// Abstract/bodyless def or enclosing scope closing
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			continue
		}
		p.s.Pos++
	}
}

func (p *scalaParser) parseTypeAlias() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	p.skipToExprEnd()
	p.result.Symbols = append(p.result.Symbols, ScalaSymbol{
		Type: "type", Name: name, StartLine: startLine, EndLine: p.s.Line,
		Parent: p.stack.NearestSym(),
	})
}

func (p *scalaParser) parseImport() {
	startLine := p.s.Line
	p.s.SkipSpaces()
	start := p.s.Pos
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '\n' || c == ';' {
			break
		}
		if c == '{' {
			break
		}
		p.s.Pos++
	}
	path := string(p.s.Src[start:p.s.Pos])
	// Clean up trailing whitespace
	for len(path) > 0 && (path[len(path)-1] == ' ' || path[len(path)-1] == '\t') {
		path = path[:len(path)-1]
	}
	if !p.s.EOF() && p.s.Peek() == '{' {
		p.s.SkipBalanced('{', '}', scalaStringScanner)
	}
	if path != "" {
		p.result.Imports = append(p.result.Imports, ScalaImport{Path: path, Line: startLine})
	}
}

func (p *scalaParser) parsePackage() {
	// Skip to end of line
	for !p.s.EOF() && p.s.Peek() != '\n' {
		p.s.Pos++
	}
}

func (p *scalaParser) skipToExprEnd() {
	depth := 0
	for !p.s.EOF() {
		if scalaStringScanner(&p.s) {
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
		case c == '\n' && depth == 0:
			return
		case c == ';' && depth == 0:
			return
		case c == '\n':
			p.s.Next()
		default:
			p.s.Pos++
		}
	}
}

func (p *scalaParser) skipWSAndComments() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' {
			p.s.Pos++
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