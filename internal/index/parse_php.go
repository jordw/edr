package index

import (
	"github.com/jordw/edr/internal/lexkit"
)

// ParsePHP is a hand-written PHP symbol + import extractor.
//
// Handles:
//   - class / interface / trait / enum (→ type "class")
//   - function at file scope (→ "function"), inside class (→ "method")
//   - namespace declaration
//   - use imports: use Foo\Bar; use Foo\Bar as Alias;
//   - modifiers: public, private, protected, static, abstract, final, readonly
//   - strings: '...', "...", <<<HEREDOC, <<<'NOWDOC'
//   - comments: //, /* */, # (line comment)
//   - Variables start with $  — skipped
//   - <?php / <?= opening tags — skipped
//
// Known gaps:
//   - Anonymous classes not tracked
//   - Arrow functions / closures not tracked
//   - match expressions not tracked

type PhpResult struct {
	Symbols []PhpSymbol
	Imports []PhpImport
}

type PhpSymbol struct {
	Type      string // "class" | "function" | "method"
	Name      string
	StartLine int
	EndLine   int
	Parent    int
}

type PhpImport struct {
	Path string
	Line int
}

type phpScopeKind int

const (
	phpBlock    phpScopeKind = iota
	phpClass                 // class/interface/trait/enum body
	phpFunction              // function/method body
)

func ParsePHP(src []byte) PhpResult {
	p := &phpParser{s: lexkit.New(src), memberStart: true}
	p.run()
	for p.stack.Depth() > 0 {
		e, _ := p.stack.Pop()
		if e.SymIdx >= 0 && p.result.Symbols[e.SymIdx].EndLine == 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	return p.result
}

type phpParser struct {
	s            lexkit.Scanner
	stack        lexkit.ScopeStack[phpScopeKind]
	result       PhpResult
	pendingScope *lexkit.Scope[phpScopeKind]
	memberStart  bool
}

func phpStringScanner(s *lexkit.Scanner) bool {
	c := s.Peek()
	switch c {
	case '"':
		s.ScanSimpleString('"')
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
	case '#':
		if s.PeekAt(1) == '[' {
			// PHP 8 attribute: #[Attr(...)], skip balanced brackets
			s.Pos++
			s.SkipBalanced('[', ']', nil)
		} else {
			s.SkipLineComment()
		}
		return true
	}
	return false
}

// scanPHPHeredoc handles <<<LABEL and <<<'LABEL' heredoc/nowdoc syntax.
// Call after consuming the three '<' characters.
func scanPHPHeredoc(s *lexkit.Scanner) {
	// Optional 'LABEL' or "LABEL" — we only support the bare form and single-quoted
	nowdoc := false
	if !s.EOF() && s.Peek() == '\'' {
		nowdoc = true
		s.Pos++
	} else if !s.EOF() && s.Peek() == '"' {
		s.Pos++
	}
	_ = nowdoc

	// Read label name
	if s.EOF() || !lexkit.DefaultIdentStart[s.Peek()] {
		return
	}
	labelStart := s.Pos
	s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	label := string(s.Src[labelStart:s.Pos])

	// Skip optional closing quote of label
	if !s.EOF() && (s.Peek() == '\'' || s.Peek() == '"') {
		s.Pos++
	}

	// Scan until a line that starts with exactly the label followed by ; or newline
	for !s.EOF() {
		// Advance to start of next line
		for !s.EOF() && s.Src[s.Pos] != '\n' {
			s.Pos++
		}
		if s.EOF() {
			return
		}
		s.Next() // consume '\n'
		// Check if this line starts with the label
		end := s.Pos + len(label)
		if end > len(s.Src) {
			continue
		}
		if string(s.Src[s.Pos:end]) == label {
			// Check terminated by ;, newline, or EOF
			after := end
			if after < len(s.Src) && s.Src[after] == ';' {
				after++
			}
			if after >= len(s.Src) || s.Src[after] == '\n' || s.Src[after] == '\r' {
				s.Pos = after
				return
			}
		}
	}
}

func (p *phpParser) run() {
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
		case c == '#':
			if p.s.PeekAt(1) == '[' {
				p.s.Pos++
				p.s.SkipBalanced('[', ']', phpStringScanner)
			} else {
				p.s.SkipLineComment()
			}
		case c == '/' && p.s.PeekAt(1) == '*':
			p.s.Advance(2)
			p.s.SkipBlockComment("*/")
		case c == '"':
			p.s.ScanSimpleString('"')
			p.memberStart = false
		case c == '\'':
			p.s.ScanSimpleString('\'')
			p.memberStart = false
		case c == '<' && p.s.PeekAt(1) == '<' && p.s.PeekAt(2) == '<':
			p.s.Advance(3)
			scanPHPHeredoc(&p.s)
			p.memberStart = false
		case c == '<' && p.s.PeekAt(1) == '?':
			// <?php or <?= opening tag — skip past it
			p.s.Advance(2)
			if !p.s.EOF() && (p.s.Peek() == 'p' || p.s.Peek() == '=') {
				// consume rest of the tag keyword
				for !p.s.EOF() && p.s.Peek() != ' ' && p.s.Peek() != '\t' && p.s.Peek() != '\n' {
					p.s.Pos++
				}
			}
		case c == '{':
			p.handleOpenBrace()
		case c == '}':
			p.handleCloseBrace()
		case c == ';':
			p.s.Pos++
			p.memberStart = true
		case c == '$':
			// PHP variable — skip identifier after $
			p.s.Pos++
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
			p.memberStart = false
		case lexkit.DefaultIdentStart[c]:
			word := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			p.handleIdent(word)
		default:
			p.s.Pos++
			p.memberStart = false
		}
	}
}

func (p *phpParser) currentKind() phpScopeKind {
	if t := p.stack.Top(); t != nil {
		return t.Data
	}
	return phpBlock
}

func (p *phpParser) inFunction() bool {
	return p.stack.Any(func(k phpScopeKind) bool { return k == phpFunction })
}

func (p *phpParser) handleOpenBrace() {
	p.s.Pos++
	entry := lexkit.Scope[phpScopeKind]{Data: phpBlock, SymIdx: -1, OpenLine: p.s.Line}
	if p.pendingScope != nil {
		entry = *p.pendingScope
		p.pendingScope = nil
	}
	if entry.Data == phpFunction {
		// Skip function body without tracking nested declarations.
		depth := 1
		for !p.s.EOF() && depth > 0 {
			if phpStringScanner(&p.s) {
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

func (p *phpParser) handleCloseBrace() {
	p.s.Pos++
	if e, ok := p.stack.Pop(); ok {
		if e.SymIdx >= 0 {
			p.result.Symbols[e.SymIdx].EndLine = p.s.Line
		}
	}
	p.memberStart = true
}

func (p *phpParser) handleIdent(word []byte) {
	switch string(word) {
	case "namespace":
		p.parseNamespace()
		return
	case "use":
		p.parseUse()
		return
	case "class", "interface", "trait", "enum":
		if !p.inFunction() {
			p.parseTypeDecl()
			return
		}
	case "function":
		if !p.inFunction() {
			p.parseFunction()
			return
		}
	case "public", "private", "protected", "static", "abstract",
		"final", "readonly":
		// modifier — preserve memberStart
		return
	case "new", "instanceof", "return", "echo", "print",
		"if", "else", "elseif", "while", "for", "foreach",
		"switch", "case", "default", "break", "continue",
		"try", "catch", "finally", "throw", "match",
		"yield", "do":
		p.memberStart = false
		return
	}
	p.memberStart = false
}

func (p *phpParser) parseNamespace() {
	// namespace Foo\Bar; or namespace Foo\Bar { }
	// We don't record namespace as a symbol, just skip it.
	p.skipWSAndComments()
	for !p.s.EOF() {
		c := p.s.Peek()
		if c == ';' || c == '{' || c == '\n' {
			if c == ';' {
				p.s.Pos++
			}
			break
		}
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *phpParser) parseUse() {
	startLine := p.s.Line
	p.skipWSAndComments()

	// use function / use const — skip the sub-kind keyword
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		ws := string(w)
		switch ws {
		case "function", "const":
			p.skipWSAndComments()
		default:
			p.s.Pos = save
		}
	}

	// Read the FQCN path
	start := p.s.Pos
	for !p.s.EOF() {
		c := p.s.Peek()
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		} else if c == '\\' {
			p.s.Pos++
		} else {
			break
		}
	}
	path := string(p.s.Src[start:p.s.Pos])

	// Skip optional "as Alias"
	p.skipWSAndComments()
	if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
		save := p.s.Pos
		w := p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(w) != "as" {
			p.s.Pos = save
		} else {
			p.skipWSAndComments()
			if !p.s.EOF() && lexkit.DefaultIdentStart[p.s.Peek()] {
				p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
		}
	}

	if path != "" {
		p.result.Imports = append(p.result.Imports, PhpImport{Path: path, Line: startLine})
	}

	// Skip to end of statement
	for !p.s.EOF() && p.s.Peek() != ';' && p.s.Peek() != '\n' {
		p.s.Pos++
	}
	if !p.s.EOF() && p.s.Peek() == ';' {
		p.s.Pos++
	}
	p.memberStart = true
}

func (p *phpParser) parseTypeDecl() {
	startLine := p.s.Line
	p.skipWSAndComments()
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))
	// Skip extends / implements clauses until {
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			break
		}
		if c == ';' {
			p.s.Pos++
			return
		}
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			continue
		}
		if c == '\\' || c == ',' {
			p.s.Pos++
			continue
		}
		p.s.Pos++
	}
	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, PhpSymbol{
		Type: "class", Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[phpScopeKind]{Data: phpClass, SymIdx: sym, OpenLine: startLine}
}

func (p *phpParser) parseFunction() {
	startLine := p.s.Line
	p.skipWSAndComments()
	// Optional & for reference return
	if !p.s.EOF() && p.s.Peek() == '&' {
		p.s.Pos++
		p.skipWSAndComments()
	}
	if p.s.EOF() || !lexkit.DefaultIdentStart[p.s.Peek()] {
		// Anonymous function — skip
		return
	}
	name := string(p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont))

	// Determine type: method if we're inside a class, function otherwise
	symType := "function"

	// Skip parameter list and return type until { or ;
	for !p.s.EOF() {
		p.skipWSAndComments()
		c := p.s.Peek()
		if c == '{' {
			break
		}
		if c == ';' {
			// Abstract method declaration
			p.s.Pos++
			parent := p.stack.NearestSym()
			p.result.Symbols = append(p.result.Symbols, PhpSymbol{
				Type: symType, Name: name, StartLine: startLine, Parent: parent,
			})
			p.memberStart = true
			return
		}
		if c == '(' {
			p.s.SkipBalanced('(', ')', phpStringScanner)
			continue
		}
		if c == ':' {
			p.s.Pos++
			continue
		}
		if c == '?' {
			p.s.Pos++
			continue
		}
		if c == '\\' || c == '|' || c == '&' {
			p.s.Pos++
			continue
		}
		if lexkit.DefaultIdentStart[c] {
			p.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			continue
		}
		p.s.Pos++
	}

	parent := p.stack.NearestSym()
	sym := len(p.result.Symbols)
	p.result.Symbols = append(p.result.Symbols, PhpSymbol{
		Type: symType, Name: name, StartLine: startLine, Parent: parent,
	})
	p.pendingScope = &lexkit.Scope[phpScopeKind]{Data: phpFunction, SymIdx: sym, OpenLine: startLine}
}

func (p *phpParser) skipWSAndComments() {
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
		if c == '#' {
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
