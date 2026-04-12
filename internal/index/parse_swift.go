package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseSwift is a hand-written Swift symbol + import extractor.
//
// Handles:
//   - class / struct / enum / protocol (→ type "class")
//   - extension (→ type "impl")
//   - func declarations (→ type "function" or "method" when nested in a type)
//   - init / deinit (→ type "method")
//   - import statements
//   - modifiers: public, private, fileprivate, internal, open, final,
//     static, class (as modifier), override, mutating
//   - annotations: @available, @objc, etc. — skipped with balanced parens
//   - strings: "...", multi-line """..."""
//   - comments: // and /* */
//
// Known gaps:
//   - subscript, computed property bodies not tracked as symbols
//   - Closure expressions not tracked

type SwiftResult struct {
	Symbols []SwiftSymbol
	Imports []SwiftImport
}

type SwiftSymbol struct {
	Type      string // "class" | "impl" | "function" | "method"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type SwiftImport struct {
	Path string
	Line int
}

type swiftScopeKind int

const (
	swiftBlock    swiftScopeKind = iota
	swiftTypeDecl                // class/struct/enum/protocol/extension body
	swiftFunction                // func/init/deinit body
)

func ParseSwift(src []byte) SwiftResult {
	p := &swiftParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type swiftParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[swiftScopeKind]
	result       SwiftResult
	pendingScope *lexkit.Scope[swiftScopeKind]
	memberStart  bool
}

func swiftStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		if s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			scanSwiftMultilineString(s)
		} else {
			s.ScanSimpleString('"')
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

func scanSwiftMultilineString(s *lexkit.Scanner) {
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

func (p *swiftParser) run() {
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
				scanSwiftMultilineString(&p.s)
			} else {
				p.s.ScanSimpleString('"')
			}
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
		case c == '#':
			// #if, #else, #endif, #available — skip to end of line token
			p.s.SkipLineComment()
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *swiftParser) currentKind() swiftScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return swiftBlock
}

func (p *swiftParser) inFunction() bool {
	return p.stack.Any(func(k swiftScopeKind) bool { return k == swiftFunction })
}

func (p *swiftParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[swiftScopeKind]{Data: swiftBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == swiftFunction {
		// Skip function body — don't record nested symbols
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if swiftStringScanner(&p.s) {
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

func (p *swiftParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
}

func (p *swiftParser) handleAnnotation() {
	p.s.Pos++ // @
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	p.skipWSAndComments()
	// Some annotations have argument lists: @available(iOS 14, *)
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', swiftStringScanner)
	}
}

func (p *swiftParser) handleIdent(word []byte) {
	switch string(word) {
	case "import":
		p.parseImport()
		return
	case "class", "struct", "enum", "protocol":
		if !p.inFunction() {
			p.parseTypeDecl(string(word), "class")
			return
		}
	case "extension":
		if !p.inFunction() {
			p.parseTypeDecl("extension", "impl")
			return
		}
	case "func":
		if !p.inFunction() {
			p.parseFunc()
			return
		}
	case "init":
		if !p.inFunction() {
			p.parseInitOrDeinit("init")
			return
		}
	case "deinit":
		if !p.inFunction() {
			p.parseDeinit()
			return
		}
	case "public", "private", "fileprivate", "internal", "open",
		"final", "static", "override", "mutating", "nonmutating",
		"lazy", "weak", "unowned", "dynamic", "required", "convenience",
		"indirect", "nonisolated", "isolated":
		// modifier — preserve memberStart
		return
	case "var", "let":
		// property declarations — not recorded as symbols
		p.memberStart = false
		return
	}
	p.memberStart = false
}

func (p *swiftParser) parseImport() {
	startLine := p.s.Line
	p.skipWSAndComments()
	// Optional import kind: class, struct, enum, protocol, func, var, typealias
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		ws := string(w)
		switch ws {
		case "class", "struct", "enum", "protocol", "func", "var", "typealias":
			p.skipWSAndComments()
		default:
			p.s.Pos = save
		}
	}
	// Read dotted module path (e.g., Foundation, UIKit.UIView)
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
	path := string(p.s.Src[start:p.s.Pos])
	if path != "" {
		p.result.Imports = append(p.result.Imports, SwiftImport{Path: path, Line: startLine})
	}
	// Skip to end of line
	for !p.s.EOF() && p.s.Peek() != '\n' {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *swiftParser) parseTypeDecl(keyword, symType string) {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Skip generics, inheritance clause, where clause until {
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
	p.result.Symbols = append(p.result.Symbols, SwiftSymbol{
		Type: symType, Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[swiftScopeKind]{Data: swiftTypeDecl, SymIdx: sym, OpenLine: startLine}
}

func (p *swiftParser) parseFunc() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		// Operator func: func +(...)  — skip
		p.skipToFuncBodyOrEnd()
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	sym := p.recordFunc(name, "function", startLine)
	p.skipToFuncBodyOrEnd(sym)
}

func (p *swiftParser) parseInitOrDeinit(keyword string) {
	startLine := p.s.Line
	// init may have a '?' or '!' suffix for failable initializers
	if !p.s.EOF() && (p.s.Peek() == '?' || p.s.Peek() == '!') {
		p.s.Pos++
	}
	sym := p.recordFunc(keyword, "method", startLine)
	p.skipToFuncBodyOrEnd(sym)
}

func (p *swiftParser) parseDeinit() {
	startLine := p.s.Line
	sym := p.recordFunc("deinit", "method", startLine)
	p.skipToFuncBodyOrEnd(sym)
}

func (p *swiftParser) recordFunc(name, symType string, startLine int) int {
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, SwiftSymbol{
		Type: symType, Name: name, StartLine: startLine, Parent: parent,
	})
	return sym
}

// skipToFuncBodyOrEnd scans forward past generics, parameters, return
// type, async/throws, and where clauses, then either opens the function
// body via handleOpenBrace or returns on ';' / EOF.
func (p *swiftParser) skipToFuncBodyOrEnd(symIdx ...int) {
	idx := -1
	if len(symIdx) > 0 {
		idx = symIdx[0]
	}
	pastParams := false
	for !p.s.EOF() {
		p.skipWSAndComments()
		if p.s.EOF() {
			return
		}
		c := p.s.Peek()
		switch {
		case c == '{':
			p.pendingScope = &lexkit.Scope[swiftScopeKind]{Data: swiftFunction, SymIdx: idx, OpenLine: p.s.Line}
			p.handleOpenBrace()
			return
		case c == '(':
			p.s.SkipBalanced('(', ')', swiftStringScanner)
			pastParams = true
		case c == '<':
			p.s.SkipAngles()
		case c == '-' && p.s.PeekAt(1) == '>':
			p.s.Advance(2)
		case c == ';':
			if idx >= 0 {
				p.result.Symbols[idx].EndLine = p.s.Line
			}
			p.s.Pos++
			p.memberStart = true
			return
		case c == '\n':
			if pastParams {
				// Bodyless declaration (protocol requirement, etc.)
				if idx >= 0 {
					p.result.Symbols[idx].EndLine = p.s.Line
				}
				p.s.Next()
				p.memberStart = true
				return
			}
			p.s.Next()
			p.memberStart = true
		case c == '}':
			// Hit closing brace of enclosing scope — bodyless declaration
			if idx >= 0 {
				p.result.Symbols[idx].EndLine = p.s.Line
			}
			return
		case lexkit.DefaultIdentStart[c]:
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		default:
			p.s.Pos++
		}
	}
}

func (p *swiftParser) skipWSAndComments() {
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
