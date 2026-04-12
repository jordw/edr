package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseRust is a hand-written Rust symbol + import extractor.
//
// Handles:
//   - fn (free, methods in impl/trait, async/const/unsafe modifiers)
//   - struct (named and tuple), enum, trait, impl
//   - mod (with body and external mod;)
//   - type aliases, const, static
//   - use statements (paths with ::, globs, groups)
//   - macro_rules! definitions
//   - pub/pub(crate)/pub(super) visibility
//   - Lifetime disambiguation ('a vs 'x')
//   - Raw strings r#"..."#, byte strings b"..."
//   - Attributes #[...] and #![...]
//   - Line + block comments
//
// Known gaps:
//   - Procedural macros (attribute/derive/function-like) not tracked
//   - Macro invocations foo!{...} can contain arbitrary tokens
//   - impl blocks not recorded as symbols; methods get parent=-1

type RustResult struct {
	Symbols []RustSymbol
	Imports []RustImport
}

type RustSymbol struct {
	Type      string // "function" | "method" | "struct" | "enum" | "interface" | "type" | "constant" | "variable" | "macro"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type RustImport struct {
	Path string
	Line int
}

type rustScopeKind int

const (
	rustBlock    rustScopeKind = iota
	rustMod
	rustImpl
	rustTrait
	rustFunction
)

func ParseRust(src []byte) RustResult {
	p := &rustParser{s: lexkit.New(src)}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type rustParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[rustScopeKind]
	result       RustResult
	pendingScope *lexkit.Scope[rustScopeKind]
}

func rustStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		s.ScanSimpleString('"')
		return true
	case '\'':
		if s.PeekAt(2) == '\'' || s.PeekAt(1) == '\\' {
			s.ScanSimpleString('\'')
		} else {
			s.Pos++
			if lexkit.DefaultIdentStart[s.Peek()] {
				s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
		}
		return true
	case 'r':
		if s.PeekAt(1) == '"' || s.PeekAt(1) == '#' {
			scanRustRawString(s)
			return true
		}
	case 'b':
		if s.PeekAt(1) == '"' {
			s.Pos++
			s.ScanSimpleString('"')
			return true
		}
		if s.PeekAt(1) == '\'' {
			s.Pos++
			s.ScanSimpleString('\'')
			return true
		}
		if s.PeekAt(1) == 'r' && (s.PeekAt(2) == '"' || s.PeekAt(2) == '#') {
			s.Pos++
			scanRustRawString(s)
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

func scanRustRawString(s *lexkit.Scanner) {
	s.Pos++ // skip 'r'
	hashes := 0
	for !s.EOF() && s.Peek() == '#' {
		hashes++
		s.Pos++
	}
	if s.EOF() || s.Peek() != '"' {
		return
	}
	s.Pos++ // opening "
	for !s.EOF() {
		if s.Peek() == '"' {
			s.Pos++
			count := 0
			for count < hashes && !s.EOF() && s.Peek() == '#' {
				count++
				s.Pos++
			}
			if count == hashes {
				return
			}
		} else {
			s.Next()
		}
	}
}

func (p *rustParser) run() {
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			p.s.Next()
		case c == '/' && p.s.PeekAt(1) == '/':
			p.s.SkipLineComment()
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		case c == '"':
			p.s.ScanSimpleString('"')
		case c == '\'':
			if p.s.PeekAt(2) == '\'' || p.s.PeekAt(1) == '\\' {
				p.s.ScanSimpleString('\'')
			} else {
				p.s.Pos++
				if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
					p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				}
			}
		case c == 'r' && (p.s.PeekAt(1) == '"' || p.s.PeekAt(1) == '#'):
			scanRustRawString(&p.s)
		case c == 'b' && (p.s.PeekAt(1) == '"' || p.s.PeekAt(1) == '\'' || p.s.PeekAt(1) == 'r'):
			p.s.Pos++
			if p.s.Peek() == 'r' {
				scanRustRawString(&p.s)
			} else {
				p.s.ScanSimpleString(p.s.Peek())
			}
		case c == '#':
			p.handleAttribute()
		case c == '{':
			p.handleOpenBrace()
		case c == '}':
			p.handleCloseBrace()
		case c == ';':
			p.s.Pos++
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
		}
	}
}

func (p *rustParser) currentKind() rustScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return rustMod
}

func (p *rustParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[rustScopeKind]{Data: rustBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == rustFunction {
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if rustStringScanner(&p.s) {
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
}

func (p *rustParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
}

func (p *rustParser) handleAttribute() {
	p.s.Pos++ // #
	if !p.s.EOF() && p.s.Peek() == '!' {
		p.s.Pos++ // inner attribute #!
	}
	if !p.s.EOF() && p.s.Peek() == '[' {
		p.s.SkipBalanced('[', ']', rustStringScanner)
	}
}

func (p *rustParser) handleIdent(word []byte) {
	kind := p.currentKind()
	switch string(word) {
	case "fn":
		if kind != rustFunction {
			p.parseFn()
		}
		return
	case "struct":
		if kind != rustFunction {
			p.parseStruct()
		}
		return
	case "enum":
		if kind != rustFunction {
			p.parseEnum()
		}
		return
	case "trait":
		if kind != rustFunction {
			p.parseTrait()
		}
		return
	case "impl":
		if kind != rustFunction {
			p.parseImpl()
		}
		return
	case "mod":
		if kind != rustFunction {
			p.parseMod()
		}
		return
	case "type":
		if kind != rustFunction {
			p.parseTypeAlias()
		}
		return
	case "const":
		p.skipWSAndComments()
		if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
			save := p.s.Pos
			saveLine := p.s.Line
			w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			if string(w) == "fn" {
				p.parseFn()
				return
			}
			p.s.Pos = save
			p.s.Line = saveLine
		}
		if kind != rustFunction {
			p.parseConstOrStatic("constant")
		}
		return
	case "static":
		if kind != rustFunction {
			p.parseConstOrStatic("variable")
		}
		return
	case "use":
		if kind != rustFunction {
			p.parseUse()
		}
		return
	case "macro_rules":
		if kind != rustFunction {
			p.parseMacroRules()
		}
		return
	case "pub":
		p.skipPub()
		return
	case "async", "unsafe", "extern":
		return
	}
}

func (p *rustParser) parseFn() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	kind := "function"
	parent := p.stack.NearestSym()
	k := p.currentKind()
	if k == rustImpl || k == rustTrait {
		kind = "method"
	}
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: kind, Name: name, StartLine: startLine, Parent: parent,
	})
	// Skip generics, params, return type until { or ;
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			p.pendingScope = &lexkit.Scope[rustScopeKind]{Data: rustFunction, SymIdx: sym, OpenLine: startLine}
			return
		}
		if c == ';' {
			p.s.Pos++
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', rustStringScanner)
			continue
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		if c == '[' {
			p.s.SkipBalanced('[', ']', rustStringScanner)
			continue
		}
		p.s.Pos++
	}
}

func (p *rustParser) parseStruct() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: "struct", Name: name, StartLine: startLine, Parent: parent,
	})
	// Skip until { (named struct), ( (tuple struct), or ;
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			p.s.SkipBalanced('{', '}', rustStringScanner)
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', rustStringScanner)
			continue
		}
		if c == ';' {
			p.s.Pos++
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		p.s.Pos++
	}
}

func (p *rustParser) parseEnum() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: "enum", Name: name, StartLine: startLine, Parent: parent,
	})
	// Skip to { then balanced-skip body
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			p.s.SkipBalanced('{', '}', rustStringScanner)
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == ';' {
			p.s.Pos++
			p.result.Symbols[sym].EndLine = p.s.Line
			return
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		p.s.Pos++
	}
}

func (p *rustParser) parseTrait() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: "interface", Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[rustScopeKind]{Data: rustTrait, SymIdx: sym, OpenLine: startLine}
	// Skip generics, bounds until {
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			return
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		p.s.Pos++
	}
}

func (p *rustParser) parseImpl() {
	// impl [<generics>] [Trait for] Type { ... }
	// Don't record as symbol — just push scope
	p.pendingScope = &lexkit.Scope[rustScopeKind]{Data: rustImpl, SymIdx: -1, OpenLine: p.s.Line}
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			return
		}
		if c == '<' {
			p.s.SkipAngles()
			continue
		}
		if c == ';' {
			p.s.Pos++
			p.pendingScope = nil
			return
		}
		p.s.Pos++
	}
}

func (p *rustParser) parseMod() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == ';' {
		// External module: mod name;
		p.s.Pos++
		p.result.Symbols = append(p.result.Symbols, RustSymbol{
			Type: "class", Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: parent,
		})
		return
	}
	// Inline module: mod name { ... }
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: "class", Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[rustScopeKind]{Data: rustMod, SymIdx: sym, OpenLine: startLine}
}

func (p *rustParser) parseTypeAlias() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	p.skipToSemicolon()
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: "type", Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: p.stack.NearestSym(),
	})
}

func (p *rustParser) parseConstOrStatic(kind string) {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	p.skipToSemicolon()
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: kind, Name: name, StartLine: startLine, EndLine: p.s.Line, Parent: p.stack.NearestSym(),
	})
}

func (p *rustParser) parseUse() {
	startLine := p.s.Line
	p.skipWSAndComments()
	start := p.s.Pos
	// Scan the use path until ;
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ';' {
			p.s.Pos++
			break
		}
		if c == '{' {
			// use path::{a, b} — record path up to ::
			break
		}
		if c == '\n' {
			p.s.Next()
			continue
		}
		p.s.Pos++
	}
	path := string(p.s.Src[start:p.s.Pos])
	// Clean up: remove trailing ;, trim ws
	for len(path) > 0 && (path[len(path)-1] == ';' || path[len(path)-1] == ' ' || path[len(path)-1] == '\n' || path[len(path)-1] == '\r' || path[len(path)-1] == '\t') {
		path = path[:len(path)-1]
	}
	// For grouped use: skip past the { ... };
	if !p.s.EOF() && p.s.Peek() == '{' {
		p.s.SkipBalanced('{', '}', rustStringScanner)
		p.skipWSAndComments()
		if !p.s.EOF() && p.s.Peek() == ';' {
			p.s.Pos++
		}
	}
	if path != "" {
		p.result.Imports = append(p.result.Imports, RustImport{Path: path, Line: startLine})
	}
}

func (p *rustParser) parseMacroRules() {
	p.skipWSAndComments()
	if p.s.EOF() || p.s.Peek() != '!' {
		return
	}
	p.s.Pos++ // !
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	p.skipWSAndComments()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, RustSymbol{
		Type: "macro", Name: name, StartLine: startLine, Parent: p.stack.NearestSym(),
	})
	// Body can be { ... }, ( ... ), or [ ... ]
	if !p.s.EOF() {
		switch p.s.Peek() {
		case '{':
			p.s.SkipBalanced('{', '}', rustStringScanner)
		case '(':
			p.s.SkipBalanced('(', ')', rustStringScanner)
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == ';' {
				p.s.Pos++
			}
		case '[':
			p.s.SkipBalanced('[', ']', rustStringScanner)
			p.skipWSAndComments()
			if !p.s.EOF() && p.s.Peek() == ';' {
				p.s.Pos++
			}
		}
	}
	p.result.Symbols[sym].EndLine = p.s.Line
}

func (p *rustParser) skipPub() {
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', rustStringScanner)
	}
}

func (p *rustParser) skipToSemicolon() {
	depth := 0
	for !p.s.EOF() {
		if rustStringScanner(&p.s) {
			continue
		}
		c := p.s.Peek()
		switch {
		case c == '(' || c == '[' || c == '{':
			depth++
			p.s.Pos++
		case c == ')' || c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
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

func (p *rustParser) skipWSAndComments() {
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