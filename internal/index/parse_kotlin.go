package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParseKotlin is a hand-written Kotlin symbol + import extractor.
//
// Handles:
//   - class / interface / object / enum class / data class / sealed class /
//     sealed interface / companion object
//   - fun declarations (top-level and member)
//   - typealias declarations (recorded as type "type")
//   - import statements (dotted path, optional alias ignored)
//   - package declaration
//   - generics in declarations
//   - nested classes and objects
//   - line + block comments, regular strings, raw strings (triple-quoted),
//     string templates ("$name" and "${expr}")
//   - annotations (@Name, @Name(...))
//
// Known gaps:
//   - Anonymous object expressions not tracked
//   - Lambda expressions not tracked
//   - val/var property declarations not recorded as symbols

type KotlinResult struct {
	Symbols []KotlinSymbol
	Imports []KotlinImport
}

type KotlinSymbol struct {
	Type      string // "class" | "interface" | "function"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type KotlinImport struct {
	Path string
	Line int
}

type kotlinScopeKind int

const (
	kotlinBlock    kotlinScopeKind = iota
	kotlinClass                    // class / interface / object body
	kotlinFunction                 // fun body — skip contents
)

func ParseKotlin(src []byte) KotlinResult {
	p := &kotlinParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type kotlinParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[kotlinScopeKind]
	result       KotlinResult
	pendingScope *lexkit.Scope[kotlinScopeKind]
	memberStart  bool
}

// kotlinStringScanner handles all Kotlin literal forms for use with
// SkipBalanced and the main run loop. Returns true if it consumed input.
func kotlinStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		if s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			// Triple-quoted raw string — no escape processing, no interpolation.
			scanKotlinRawString(s)
		} else {
			// Regular string with $-interpolation.
			s.ScanInterpolatedString('"', "${", func(inner *lexkit.Scanner) {
				inner.SkipBalanced('{', '}', kotlinStringScanner)
			})
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

func scanKotlinRawString(s *lexkit.Scanner) {
	s.Advance(3) // opening """
	for !s.EOF() {
		if s.Peek() == '"' && s.PeekAt(1) == '"' && s.PeekAt(2) == '"' {
			s.Advance(3)
			return
		}
		s.Next()
	}
}

func (p *kotlinParser) run() {
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
				scanKotlinRawString(&p.s)
			} else {
				p.s.ScanInterpolatedString('"', "${", func(inner *lexkit.Scanner) {
					inner.SkipBalanced('{', '}', kotlinStringScanner)
				})
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
		case c == '<':
			p.s.SkipAngles()
		case c == '@':
			p.handleAnnotation()
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *kotlinParser) currentKind() kotlinScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return kotlinBlock
}

func (p *kotlinParser) inFunction() bool {
	return p.stack.Any(func(k kotlinScopeKind) bool { return k == kotlinFunction })
}

func (p *kotlinParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[kotlinScopeKind]{Data: kotlinBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == kotlinFunction {
		// Skip function body without recording nested symbols.
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if kotlinStringScanner(&p.s) {
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

func (p *kotlinParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
}

func (p *kotlinParser) handleAnnotation() {
	p.s.Pos++ // @
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	p.skipWSAndComments()
	if !p.s.EOF() && p.s.Peek() == '(' {
		p.s.SkipBalanced('(', ')', kotlinStringScanner)
	}
}

func (p *kotlinParser) handleIdent(word []byte) {
	switch string(word) {
	case "package":
		p.parsePackage()
		return
	case "import":
		p.parseImport()
		return
	case "class", "object":
		if !p.inFunction() {
			p.parseTypeDecl("class")
			return
		}
	case "interface":
		if !p.inFunction() {
			p.parseTypeDecl("interface")
			return
		}
	case "enum":
		// "enum class Name" — skip the "class" token then parse type.
		if !p.inFunction() {
			p.skipWSAndComments()
			// consume optional "class" keyword
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				save := p.s.Pos
				w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				if string(w) != "class" {
					p.s.Pos = save // put it back; unusual but be safe
				}
			}
			p.parseTypeDecl("class")
			return
		}
	case "data", "sealed":
		// "data class" / "data object" / "sealed class" / "sealed interface"
		if !p.inFunction() {
			p.skipWSAndComments()
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				// the next token is class/interface/object — handled by falling
				// through to parseTypeDecl below via re-encounter in run(), but
				// we need to call it directly here since we consumed the keyword.
			}
			p.parseTypeDecl("class")
			return
		}
	case "typealias":
		if !p.inFunction() {
			p.parseTypealias()
			return
		}
	case "fun":
		if !p.inFunction() {
			p.parseFun()
			return
		}
	case "companion":
		// "companion object [Name]" — treat as class scope
		if !p.inFunction() {
			p.skipWSAndComments()
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				kw := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				if string(kw) == "object" {
					p.parseCompanionObject()
					return
				}
			}
		}
	case "public", "private", "protected", "internal",
		"open", "abstract", "override", "suspend",
		"inline", "infix", "operator", "tailrec",
		"external", "final", "const", "lateinit", "vararg":
		// modifier — preserve memberStart so the next token handles the decl
		return
	}

	// At class scope and memberStart: try to detect fun declarations that
	// start with a type word we didn't recognise above (e.g. extension funs
	// written without modifiers where first token is a type name are unusual,
	// but we fall through gracefully).
	p.memberStart = false
}

func (p *kotlinParser) parsePackage() {
	for !p.s.EOF() && p.s.Peek() != '\n' && p.s.Peek() != ';' {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *kotlinParser) parseImport() {
	startLine := p.s.Line
	// Skip horizontal whitespace only — do not cross lines.
	for !p.s.EOF() && (p.s.Peek() == ' ' || p.s.Peek() == '\t') {
		p.s.Pos++
	}
	start := p.s.Pos
	for !p.s.EOF() {
		c := p.s.Peek()
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		} else if c == '.' {
			p.s.Pos++
		} else if c == '*' {
			p.s.Pos++
		} else {
			break
		}
	}
	path := string(p.s.Src[start:p.s.Pos])
	// Skip optional alias on the same line: "as AliasName"
	// Only skip horizontal whitespace so we don't cross into the next import.
	for !p.s.EOF() && (p.s.Peek() == ' ' || p.s.Peek() == '\t') {
		p.s.Pos++
	}
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		kw := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(kw) == "as" {
			for !p.s.EOF() && (p.s.Peek() == ' ' || p.s.Peek() == '\t') {
				p.s.Pos++
			}
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
		} else {
			p.s.Pos = save
		}
	}
	if path != "" {
		p.result.Imports = append(p.result.Imports, KotlinImport{Path: path, Line: startLine})
	}
	for !p.s.EOF() && p.s.Peek() != '\n' && p.s.Peek() != ';' {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *kotlinParser) parseTypeDecl(kind string) {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Scan forward to find the opening '{', tolerating generics, primary
	// constructor params, and supertype lists.  We stop on:
	//   '{'  — class/object body follows
	//   ';'  — statement terminator (no body)
	//   '\n' — newline after the declaration, with no continuation expected
	//            (data class with no body, e.g. "data class Foo(val x:Int):Bar()")
	//   '}'  — close of enclosing body without finding our own opening brace
	sawParen := false // seen primary-constructor '(...)'
	for !p.s.EOF() {
		c := p.s.Peek()
		switch {
		case c == '{':
			parent := p.stack.NearestSym()
			sym := len(p.result.Symbols)
			p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
				Type: kind, Name: name, StartLine: startLine, Parent: parent,
			})
			p.pendingScope = &lexkit.Scope[kotlinScopeKind]{Data: kotlinClass, SymIdx: sym, OpenLine: startLine}
			return
		case c == '}':
			// Closing brace of enclosing scope — don't consume, record bodyless.
			parent := p.stack.NearestSym()
			p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
				Type: kind, Name: name, StartLine: startLine, Parent: parent,
			})
			return
		case c == ';':
			parent := p.stack.NearestSym()
			p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
				Type: kind, Name: name, StartLine: startLine, Parent: parent,
			})
			p.s.Pos++ // consume ';'
			return
		case c == '\n':
			// Newline: if we've already seen the primary constructor params this
			// looks like a complete (bodyless) declaration — e.g. data class with
			// supertype but no body block.  If no params yet, it might be a
			// multi-line header — peek ahead past whitespace for '{' or ':'.
			if sawParen {
				// Check if next non-blank line starts the body or supertype list.
				save := p.s.Pos
				saveLine := p.s.Line
				p.s.Next() // consume '\n'
				// skip horizontal whitespace
				for !p.s.EOF() && (p.s.Peek() == ' ' || p.s.Peek() == '\t') {
					p.s.Pos++
				}
				nc := p.s.Peek()
				if nc == '{' || nc == ':' {
					// continuation — keep going
					continue
				}
				// bodyless — restore and record
				_ = save
				_ = saveLine
				parent := p.stack.NearestSym()
				p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
					Type: kind, Name: name, StartLine: startLine, Parent: parent,
				})
				p.memberStart = true
				return
			}
			p.s.Next()
		case c == '<':
			p.s.SkipAngles()
		case c == '(':
			sawParen = true
			p.s.SkipBalanced('(', ')', kotlinStringScanner)
		case c == ' ' || c == '\t' || c == '\r':
			p.s.Pos++
		case c == '/' && p.s.PeekAt(1) == '/':
			p.s.SkipLineComment()
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		default:
			p.s.Pos++
		}
	}
	// EOF — record as bodyless.
	parent := p.stack.NearestSym()
	p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
		Type: kind, Name: name, StartLine: startLine, Parent: parent,
	})
}

// parseCompanionObject handles "companion object [OptionalName] { ... }".
// We call this after consuming "companion object".
func (p *kotlinParser) parseCompanionObject() {
	startLine := p.s.Line
	p.skipWSAndComments()
	name := "Companion"
	// Optional name
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		// peek: if the next ident is not a Kotlin keyword (e.g. next could be ":"),
		// treat it as the companion name.
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		ws := string(w)
		switch ws {
		case "class", "interface", "object", "fun", "val", "var",
			"override", "public", "private", "protected", "internal":
			// not a name — put back
			p.s.Pos = save
		default:
			name = ws
		}
	}
	// Skip to opening brace
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			break
		}
		if c == ':' {
			// supertype
			p.s.Pos++
			continue
		}
		p.s.Pos++
	}
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
		Type: "class", Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[kotlinScopeKind]{Data: kotlinClass, SymIdx: sym, OpenLine: startLine}
}

// parseFun handles "fun [<typeParams>] [ReceiverType.] name(params) [: ReturnType] { body }"
// Called after consuming the "fun" keyword.
func (p *kotlinParser) parseFun() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() {
		return
	}
	// Skip optional type parameters: <T, R : Foo>
	if p.s.Peek() == '<' {
		p.s.SkipAngles()
		p.skipWSAndComments()
	}
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	// Read what might be a receiver type or the function name.
	// Kotlin syntax: fun ReceiverType.name(...) or fun name(...)
	// Strategy: read the first identifier; if followed by '.', it's the
	// receiver — read the next identifier as the name.
	firstName := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	p.skipWSAndComments()
	name := firstName
	// Handle "TypeParam.functionName" receiver syntax
	for !p.s.EOF() && p.s.Peek() == '.' {
		p.s.Pos++ // consume '.'
		p.skipWSAndComments()
		if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
			break
		}
		name = string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
		p.skipWSAndComments()
	}
	// Now name holds the function name. Scan forward to '{', '=', ';', or '}' (stop).
	sawParams := false
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		switch {
		case c == '{':
			sym := p.recordFunction(name, startLine)
			p.pendingScope = &lexkit.Scope[kotlinScopeKind]{Data: kotlinFunction, SymIdx: sym, OpenLine: startLine}
			p.handleOpenBrace()
			return
		case c == '(':
			sawParams = true
			p.s.SkipBalanced('(', ')', kotlinStringScanner)
		case c == '<':
			p.s.SkipAngles()
		case c == '=':
			// Single-expression function — record symbol, skip rest of expression.
			// We do NOT skip to EOL because the expression may span lines with
			// proper indentation. Just record; the outer run() loop handles the rest.
			p.recordFunction(name, startLine)
			p.memberStart = true
			return
		case c == ';':
			// Abstract / interface fun declaration
			p.recordFunction(name, startLine)
			p.s.Pos++
			p.memberStart = true
			return
		case c == '}':
			// Unmatched close brace — we've reached the end of the enclosing
			// class body. Record the function (bodyless / abstract) and return
			// WITHOUT consuming the '}', so handleCloseBrace can pop the scope.
			if sawParams {
				p.recordFunction(name, startLine)
			}
			p.memberStart = true
			return
		case c == ':':
			p.s.Pos++ // return type annotation — keep scanning
		case lexkit.DefaultIdentStart[c]:
			w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			// If we've already consumed params, a keyword starting a new
			// declaration means this was an abstract fun with no body.
			if sawParams {
				ws := string(w)
				switch ws {
				case "fun", "class", "interface", "object", "enum",
					"val", "var", "companion", "override", "data",
					"sealed", "abstract", "open", "public", "private",
					"protected", "internal", "suspend", "inline":
					// put back and exit
					p.s.Pos -= len(w)
					p.recordFunction(name, startLine)
					p.memberStart = true
					return
				}
			}
		default:
			p.s.Pos++
		}
	}
	// EOF — if we saw params it's a valid (bodyless) function declaration.
	if sawParams {
		p.recordFunction(name, startLine)
	}
}

func (p *kotlinParser) recordFunction(name string, startLine int) int {
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
		Type: "function", Name: name, StartLine: startLine, Parent: parent,
	})
	return sym
}

// parseTypealias handles "typealias Name = Type" declarations.
// Records the alias name as type "type".
func (p *kotlinParser) parseTypealias() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	parent := p.stack.NearestSym()
	p.result.Symbols = append(p.result.Symbols, KotlinSymbol{
		Type: "type", Name: name, StartLine: startLine, EndLine: startLine, Parent: parent,
	})
	// Skip to end of line / semicolon
	for !p.s.EOF() && p.s.Peek() != '\n' && p.s.Peek() != ';' {
		p.s.Pos++
	}
}

func (p *kotlinParser) skipWSAndComments() {
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
