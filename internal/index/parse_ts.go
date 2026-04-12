package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseTS is a hand-written TypeScript symbol + import extractor built on
// lexkit primitives.
//
// Handles:
//   - function / class / interface / type / enum / namespace / module / const / let / var
//   - class members: methods, getters, setters, constructor, generics, modifiers
//   - async / generator functions, decorators (skipped)
//   - imports: default, named, namespace, side-effect, type-only
//   - line + block comments, single/double/template strings with nested ${}
//   - regex vs division disambiguation
//   - arrow functions opening a function scope
//
// Known gaps:
//   - JSX (tree-sitter can't either)
//   - Destructuring declarations only extract the first name
//   - Object-literal return types in method signatures may confuse body detection
//   - Computed [key]() method names recorded as the first ident
//   - Nested functions inside function/arrow bodies intentionally skipped
//   - Interface member extraction intentionally skipped

type TSResult struct {
	Symbols []TSSymbol
	Imports []TSImport
}

type TSSymbol struct {
	Type      string
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type TSImport struct {
	Path string
	Line int
}

type tsScopeKind int

const (
	tsFile tsScopeKind = iota
	tsBlock
	tsClass
	tsInterface
	tsNamespace
	tsFunction
)

func ParseTS(src []byte) TSResult {
	p := &tsParser{s: lexkit.New(src), memberStart: true, regexOK: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type tsParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[tsScopeKind]
	result       TSResult
	pendingScope *lexkit.Scope[tsScopeKind]
	regexOK      bool
	memberStart  bool
}

// tsSkipTemplateExpr consumes a "${...}" body up to its matching '}'.
func tsSkipTemplateExpr(s *lexkit.Scanner) {
	depth := 1
	for !s.EOF() && depth > 0 {
		c := s.Peek()
		switch {
		case c == '{':
			depth++
			s.Pos++
		case c == '}':
			depth--
			s.Pos++
		case c == '\'':
			s.ScanSimpleString('\'')
		case c == '"':
			s.ScanSimpleString('"')
		case c == '`':
			s.ScanInterpolatedString('`', "${", tsSkipTemplateExpr)
		case c == '/' && s.PeekAt(1) == '/':
			s.SkipLineComment()
		case c == '/' && s.PeekAt(1) == '*':
			s.Advance(2)
			s.SkipBlockComment("*/")
		case c == '\n':
			s.Next()
		default:
			s.Pos++
		}
	}
}

// tsStringScanner is the StringScanner callback for SkipBalanced.
func tsStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '\'':
		s.ScanSimpleString('\'')
		return true
	case '"':
		s.ScanSimpleString('"')
		return true
	case '`':
		s.ScanInterpolatedString('`', "${", tsSkipTemplateExpr)
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

func (p *tsParser) run() {
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			p.s.Pos++
		case c == '\n':
			p.s.Next()
			p.memberStart = true
			p.regexOK = true
		case c == '/' && p.s.PeekAt(1) == '/':
			p.s.SkipLineComment()
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		case c == '/' && p.regexOK:
			p.s.ScanSlashRegex()
			p.regexOK = false
			p.memberStart = false
		case c == '\'':
			p.s.ScanSimpleString('\'')
			p.regexOK = false
			p.memberStart = false
		case c == '"':
			p.s.ScanSimpleString('"')
			p.regexOK = false
			p.memberStart = false
		case c == '`':
			p.s.ScanInterpolatedString('`', "${", tsSkipTemplateExpr)
			p.regexOK = false
			p.memberStart = false
		case c == '{':
			p.handleOpenBrace()
		case c == '}':
			p.handleCloseBrace()
		case c == ';':
			p.s.Pos++
			p.memberStart = true
			p.regexOK = true
		case c == '=' && p.s.PeekAt(1) == '>':
			p.s.Advance(2)
			save := p.s.Pos
			saveLine := p.s.Line
			p.skipWS()
			if !p.s.EOF() && p.s.Peek() == '{' {
				p.pendingScope = &lexkit.Scope[tsScopeKind]{Data: tsFunction, SymIdx: -1, OpenLine: p.s.Line}
			} else {
				p.s.Pos = save
				p.s.Line = saveLine
			}
			p.regexOK = true
			p.memberStart = false
		case c == '@':
			p.s.Pos++
			p.regexOK = false
			p.memberStart = false
		case lexkit.IsDefaultIdentStart(c) || c == '$':
			word := p.s.ScanIdent(tsIdentStart, tsIdentCont)
			p.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !p.s.EOF() {
				cc := p.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' && cc != 'x' && cc != 'e' && cc != 'n' {
					break
				}
				p.s.Pos++
			}
			p.regexOK = false
			p.memberStart = false
		case c == ')' || c == ']':
			p.s.Pos++
			p.regexOK = false
			p.memberStart = false
		default:
			p.s.Pos++
			p.regexOK = true
			p.memberStart = false
		}
	}
}

func tsIdentStart(c byte) bool {
	return c == '_' || c == '$' || lexkit.IsASCIIAlpha(c) || c >= 0x80
}

func tsIdentCont(c byte) bool {
	return tsIdentStart(c) || lexkit.IsASCIIDigit(c)
}

func (p *tsParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[tsScopeKind]{Data: tsBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	p.stack.Push(entry)
	p.memberStart = true
	p.regexOK = true
}

func (p *tsParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
	p.regexOK = false
}

func (p *tsParser) currentKind() tsScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return tsFile
}

func (p *tsParser) inFunction() bool {
	return p.stack.Any(func(k tsScopeKind) bool { return k == tsFunction })
}

func (p *tsParser) handleIdent(word []byte) {
	kind := p.currentKind()
	atMember := p.memberStart

	switch string(word) {
	case "function":
		p.parseFunction()
		return
	case "class":
		p.parseClass()
		return
	case "interface":
		p.parseInterface()
		return
	case "enum":
		p.parseEnum()
		return
	case "namespace", "module":
		if atMember {
			p.parseNamespace()
			return
		}
	case "type":
		if atMember && p.looksLikeTypeAlias() {
			p.parseTypeAlias()
			return
		}
	case "const", "let", "var":
		if atMember && (kind == tsFile || kind == tsNamespace) {
			// `const enum Foo {...}` is a TS compile-time enum, not a var.
			if string(word) == "const" && p.nextIdentIs("enum") {
				p.skipWS()
				p.s.ScanIdent(tsIdentStart, tsIdentCont) // consume 'enum'
				p.parseEnum()
				return
			}
			p.parseVarDecl(string(word))
			return
		}
	case "import":
		if atMember && kind == tsFile {
			p.parseImport()
			return
		}
	case "export", "default", "declare", "abstract":
		p.regexOK = true
		return
	}

	if atMember && kind == tsClass {
		if string(word) == "constructor" {
			p.parseMethod("constructor")
			return
		}
		if tsIsClassModifier(word) {
			p.regexOK = true
			return
		}
		if p.looksLikeMethodAfterName() {
			p.parseMethod(string(word))
			return
		}
		p.skipToStmtEnd()
		return
	}

	switch string(word) {
	case "return", "typeof", "void", "delete", "throw", "new", "in", "of", "instanceof", "yield", "await":
		p.regexOK = true
	default:
		p.regexOK = false
	}
	p.memberStart = false
}

func tsIsClassModifier(word []byte) bool {
	switch string(word) {
	case "static", "public", "private", "protected", "readonly", "override", "async", "abstract", "declare", "get", "set":
		return true
	}
	return false
}

// nextIdentIs reports whether the next identifier token (after
// whitespace) equals want. The scanner is not advanced.
func (p *tsParser) nextIdentIs(want string) bool {
	save := p.s.Pos
	saveLine := p.s.Line
	p.skipWS()
	defer func() {
		p.s.Pos = save
		p.s.Line = saveLine
	}()
	if p.s.EOF() || !tsIdentStart(p.s.Peek()) {
		return false
	}
	id := p.s.ScanIdent(tsIdentStart, tsIdentCont)
	return string(id) == want
}

func (p *tsParser) looksLikeTypeAlias() bool {
	save := p.s.Pos
	saveLine := p.s.Line
	p.skipWS()
	if p.s.EOF() || !tsIdentStart(p.s.Peek()) {
		p.s.Pos = save
		p.s.Line = saveLine
		return false
	}
	p.s.ScanIdent(tsIdentStart, tsIdentCont)
	p.skipWS()
	if !p.s.EOF() && p.s.Peek() == '<' {
		p.s.SkipAngles()
	}
	p.skipWS()
	ok := !p.s.EOF() && p.s.Peek() == '='
	p.s.Pos = save
	p.s.Line = saveLine
	return ok
}

func (p *tsParser) looksLikeMethodAfterName() bool {
	save := p.s.Pos
	saveLine := p.s.Line
	p.skipWS()
	if !p.s.EOF() && p.s.Peek() == '<' {
		p.s.SkipAngles()
	}
	p.skipWS()
	ok := !p.s.EOF() && p.s.Peek() == '('
	p.s.Pos = save
	p.s.Line = saveLine
	return ok
}

func (p *tsParser) parseFunction() {
	startLine := p.s.Line
	p.skipWS()
	if !p.s.EOF() && p.s.Peek() == '*' {
		p.s.Pos++
		p.skipWS()
	}
	var name string
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name = string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
	}
	sym := -1
	if name != "" && !p.inFunction() {
		parent := p.stack.NearestSym()
		sym = len(p.result.Symbols)
		p.result.Symbols = append(p.result.Symbols, TSSymbol{
			Type: "function", Name: name, StartLine: startLine, Parent: parent,
		})
	}
	p.pendingScope = &lexkit.Scope[tsScopeKind]{Data: tsFunction, SymIdx: sym, OpenLine: startLine}
	p.memberStart = false
	p.regexOK = false
}

func (p *tsParser) parseClass() {
	startLine := p.s.Line
	p.skipWS()
	var name string
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name = string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
	}
	sym := -1
	if name != "" && !p.inFunction() {
		parent := p.stack.NearestSym()
		sym = len(p.result.Symbols)
		p.result.Symbols = append(p.result.Symbols, TSSymbol{
			Type: "class", Name: name, StartLine: startLine, Parent: parent,
		})
	}
	p.pendingScope = &lexkit.Scope[tsScopeKind]{Data: tsClass, SymIdx: sym, OpenLine: startLine}
	p.memberStart = false
	p.regexOK = false
}

func (p *tsParser) parseInterface() {
	startLine := p.s.Line
	p.skipWS()
	var name string
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name = string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
	}
	sym := -1
	if name != "" && !p.inFunction() {
		parent := p.stack.NearestSym()
		sym = len(p.result.Symbols)
		p.result.Symbols = append(p.result.Symbols, TSSymbol{
			Type: "interface", Name: name, StartLine: startLine, Parent: parent,
		})
	}
	p.skipToBraceEnd()
	if sym >= 0 {
		p.result.Symbols[sym].EndLine = p.s.Line
	}
	p.memberStart = false
	p.regexOK = false
}

func (p *tsParser) parseEnum() {
	startLine := p.s.Line
	p.skipWS()
	var name string
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name = string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
	}
	sym := -1
	if name != "" && !p.inFunction() {
		parent := p.stack.NearestSym()
		sym = len(p.result.Symbols)
		p.result.Symbols = append(p.result.Symbols, TSSymbol{
			Type: "enum", Name: name, StartLine: startLine, Parent: parent,
		})
	}
	p.skipToBraceEnd()
	if sym >= 0 {
		p.result.Symbols[sym].EndLine = p.s.Line
	}
	p.memberStart = false
	p.regexOK = false
}

func (p *tsParser) parseNamespace() {
	startLine := p.s.Line
	p.skipWS()
	var name string
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name = string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
	}
	sym := -1
	if name != "" && !p.inFunction() {
		parent := p.stack.NearestSym()
		sym = len(p.result.Symbols)
		p.result.Symbols = append(p.result.Symbols, TSSymbol{
			Type: "namespace", Name: name, StartLine: startLine, Parent: parent,
		})
	}
	p.pendingScope = &lexkit.Scope[tsScopeKind]{Data: tsNamespace, SymIdx: sym, OpenLine: startLine}
	p.memberStart = false
	p.regexOK = false
}

func (p *tsParser) parseTypeAlias() {
	startLine := p.s.Line
	p.skipWS()
	var name string
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name = string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
	}
	if name != "" && !p.inFunction() {
		parent := p.stack.NearestSym()
		p.result.Symbols = append(p.result.Symbols, TSSymbol{
			Type: "type", Name: name, StartLine: startLine, EndLine: startLine, Parent: parent,
		})
	}
	p.memberStart = false
	p.regexOK = true
}

func (p *tsParser) parseVarDecl(kind string) {
	startLine := p.s.Line
	p.skipWS()
	if !p.s.EOF() && tsIdentStart(p.s.Peek()) {
		name := string(p.s.ScanIdent(tsIdentStart, tsIdentCont))
		if !p.inFunction() {
			parent := p.stack.NearestSym()
			p.result.Symbols = append(p.result.Symbols, TSSymbol{
				Type: kind, Name: name, StartLine: startLine, EndLine: startLine, Parent: parent,
			})
		}
	}
	p.memberStart = false
	p.regexOK = true
}

func (p *tsParser) parseMethod(name string) {
	startLine := p.s.Line
	p.skipWS()
	if !p.s.EOF() && p.s.Peek() == '<' {
		p.s.SkipAngles()
	}
	p.skipWS()
	if p.s.EOF() || p.s.Peek() != '(' {
		p.skipToStmtEnd()
		return
	}
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, TSSymbol{
		Type: "method", Name: name, StartLine: startLine, Parent: parent,
	})
	p.s.SkipBalanced('(', ')', tsStringScanner)
	// Skip return type annotation until '{' or ';'
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '{' {
			p.pendingScope = &lexkit.Scope[tsScopeKind]{Data: tsFunction, SymIdx: sym, OpenLine: startLine}
			p.memberStart = false
			p.regexOK = false
			return
		}
		if c == ';' || c == '\n' {
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == '/' && p.s.PeekAt(1) == '/' {
			p.s.SkipLineComment()
			continue
		}
		p.s.Pos++
	}
}

func (p *tsParser) parseImport() {
	startLine := p.s.Line
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ';' {
			return
		}
		if c == '\n' {
			p.s.Next()
			continue
		}
		if c == '/' && p.s.PeekAt(1) == '/' {
			p.s.SkipLineComment()
			continue
		}
		if c == '\'' || c == '"' {
			quote := c
			p.s.Pos++
			start := p.s.Pos
			for !p.s.EOF() && p.s.Peek() != quote && p.s.Peek() != '\n' {
				p.s.Pos++
			}
			path := string(p.s.Src[start:p.s.Pos])
			if !p.s.EOF() && p.s.Peek() == quote {
				p.s.Pos++
			}
			p.result.Imports = append(p.result.Imports, TSImport{Path: path, Line: startLine})
			return
		}
		p.s.Pos++
	}
}

func (p *tsParser) skipToBraceEnd() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == '{' {
			p.s.SkipBalanced('{', '}', tsStringScanner)
			return
		}
		if c == ';' || c == '\n' {
			p.s.Next()
			return
		}
		p.s.Pos++
	}
}

func (p *tsParser) skipToStmtEnd() {
	depth := 0
	for !p.s.EOF() {
		c := p.s.Peek()
		if depth == 0 && (c == ';' || c == '\n') {
			p.s.Next()
			p.memberStart = true
			return
		}
		switch {
		case c == '{' || c == '(' || c == '[':
			depth++
			p.s.Pos++
		case c == '}' || c == ')' || c == ']':
			if depth == 0 {
				return
			}
			depth--
			p.s.Pos++
		case c == '\'':
			p.s.ScanSimpleString('\'')
		case c == '"':
			p.s.ScanSimpleString('"')
		case c == '`':
			p.s.ScanInterpolatedString('`', "${", tsSkipTemplateExpr)
		case c == '/' && p.s.PeekAt(1) == '/':
			p.s.SkipLineComment()
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		default:
			p.s.Pos++
		}
	}
}

func (p *tsParser) skipWS() {
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' {
			p.s.Pos++
			continue
		}
		if c == '\n' {
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