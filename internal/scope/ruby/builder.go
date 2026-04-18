// Package ruby is the Ruby scope + binding extractor.
//
// Ruby is end-based rather than brace-based: each `def`, `class`, `module`,
// `begin`, `do`, `if`, `unless`, `while`, `until`, `for`, and `case`
// opens a scope that closes at a matching `end` token. Brace blocks
// (`{ |x| ... }`) also open block scopes; hash and string-interpolation
// braces do not.
//
// v1 limitations:
//   - Lambda literals `->(x) { ... }` and `lambda { |x| ... }` are treated
//     as ordinary brace blocks; `->` itself is passed through as tokens.
//   - Method visibility (public/private/protected) is not tracked.
//   - `alias` and `alias_method` are not tracked.
//   - `class << self` (singleton classes) is parsed as an ordinary class.
//   - Complex ivar patterns — each `@foo` in a class body emits one
//     per-class decl on first sight; subsequent refs bind to it.
//   - BEGIN/END blocks are not specially recognised.
//   - Heredocs are scanned just well enough not to break the scope scan.
//   - Pattern matching (`case ... in ... end`, Ruby 3.0+) is treated the
//     same as a normal `case ... end`.
//   - `require_relative 'name'` emits an import decl; we do not normalise
//     or resolve the path.
package ruby

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
)

// Parse extracts a scope.Result from a Ruby source buffer.
func Parse(file string, src []byte) *scope.Result {
	b := &builder{
		file:             file,
		res:              &scope.Result{File: file},
		s:                lexkit.New(src),
		pendingOwnerDecl: -1,
		stmtStart:        true,
		regexOK:          true,
	}
	b.openScope(scope.ScopeFile, 0, rbOpenerFile)
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	return b.res
}

// rbOpener classifies what construct opened a scope. Used mainly as a
// documentation aid — all non-file scopes simply close on `end` or the
// matching brace, so the classification is informational.
type rbOpener int

const (
	rbOpenerFile rbOpener = iota
	rbOpenerClass
	rbOpenerModule
	rbOpenerDef
	rbOpenerBrace // `{ |x| ... }` block
	rbOpenerDo    // `do |x| ... end` block
	rbOpenerBegin
	rbOpenerIf
	rbOpenerUnless
	rbOpenerWhile
	rbOpenerUntil
	rbOpenerFor
	rbOpenerCase
)

type scopeEntry struct {
	kind         scope.ScopeKind
	id           scope.ScopeID
	opener       rbOpener
	ownerDeclIdx int
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	// stmtStart is true at the start of each statement (after \n or ;).
	// Keyword-as-modifier disambiguation ("x if y" at end of statement
	// vs. "if cond") depends on it.
	stmtStart bool

	// regexOK is true when `/` at the current position should start a
	// regex literal rather than denote division.
	regexOK bool

	// braceIsBlock is true when the next `{` should be treated as a
	// block (and pushed as a ScopeBlock) rather than a hash literal.
	// Set after `)`, `]`, ident (not a keyword), and `end` — expression
	// positions where a block argument is valid. Cleared after each use.
	braceIsBlock bool

	// pendingFullStart captures the byte position (+1; 0 == unset) of the
	// most recent def/class/module keyword, so emitDecl can stamp
	// FullSpan.StartByte on the matching decl.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last scope-owning
	// decl (def/class/module). The next openScope consumes it so
	// closeTopScope can patch FullSpan.EndByte. -1 when none.
	pendingOwnerDecl int

	// pendingDefIsSelf is true when we saw `def self.` — the next ident
	// is a class-method name rather than an instance method name. (The
	// method decl goes in the enclosing class scope regardless.)
	pendingDefIsSelf bool

	// pipeParamsExpected is true on the first line of a do-block or
	// brace-block when we're looking for an optional `|x, y|` param list.
	pipeParamsExpected bool

	// inPipeParams is true while scanning identifiers between a pair of
	// `|` tokens as block params.
	inPipeParams bool

	// pendingHeredocs tracks heredoc tags whose bodies start at the next
	// newline. Kept minimal — we only need to skip past them.
	pendingHeredocs []rbHeredoc

	// localSets tracks which identifier names have been seen as
	// assignment targets within each scope on the stack. Used by the
	// resolver (and by emission logic) so that `foo = 1; foo` treats the
	// second `foo` as a local-variable ref rather than a method call.
	// Parallel to the scope stack: localSets[i] corresponds to the i-th
	// scope on the stack (bottom = file scope = index 0).
	localSets []map[string]bool

	// ivarsByClassScope records which @ivars have already emitted a decl
	// per enclosing class scope, so that `@x = ...; @x` only emits one
	// KindField decl. Keyed by the enclosing class ScopeID.
	ivarsByClassScope map[scope.ScopeID]map[string]bool

	// assignLHS buffers idents that MIGHT be an assignment target. When
	// we see `=` (not `==`, `=>`, `=~`), they become decls; otherwise
	// they become refs.
	assignLHS        []pendingAssign
	assignHadDot     bool
	assignStopped    bool

	prevByte byte
	// prevWasIdent is true when the last-scanned token was an identifier
	// (not a keyword). Used for brace-disambiguation.
	prevWasIdent bool
}

type pendingAssign struct {
	name string
	span scope.Span
	// ivar is true for @name / @@name — decls go in the enclosing class
	// scope with namespace NSField rather than the current scope.
	ivar bool
}

type rbHeredoc struct {
	tag      string
	squiggly bool
	interp   bool
}

func (b *builder) run() {
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\\' && b.s.PeekAt(1) == '\n':
			b.s.Advance(2)
		case c == '\n':
			if len(b.pendingHeredocs) > 0 {
				b.s.Next()
				hs := b.pendingHeredocs
				b.pendingHeredocs = nil
				for _, h := range hs {
					b.readHeredocBody(h)
				}
			} else {
				b.s.Next()
			}
			b.endStatement()
			b.stmtStart = true
			b.regexOK = true
			b.prevByte = '\n'
		case c == ';':
			b.s.Pos++
			b.endStatement()
			b.stmtStart = true
			b.regexOK = true
			b.prevByte = ';'
		case c == '#':
			b.s.SkipLineComment()
		case c == '=' && b.s.PeekAt(1) == 'b' && b.stmtStart:
			// =begin ... =end pod comment (start of line).
			if b.skipPodComment() {
				continue
			}
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '='
		case c == '=':
			// Disambiguate =, ==, ===, =>, =~.
			nxt := b.s.PeekAt(1)
			switch nxt {
			case '=':
				b.s.Advance(2)
				if b.s.Peek() == '=' {
					b.s.Pos++
				}
				b.flushAssignLHSAsRefs()
				b.regexOK = true
				b.prevByte = '='
			case '>':
				b.s.Advance(2)
				b.flushAssignLHSAsRefs()
				b.regexOK = true
				b.prevByte = '>'
			case '~':
				b.s.Advance(2)
				b.flushAssignLHSAsRefs()
				b.regexOK = true
				b.prevByte = '~'
			default:
				b.s.Pos++
				// Single = : commit any pending LHS idents as decls.
				b.commitAssignLHS()
				b.regexOK = true
				b.prevByte = '='
			}
			b.stmtStart = false
			b.prevWasIdent = false
		case c == '"':
			b.flushAssignLHSAsRefs()
			b.scanInterpolatedString('"')
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '"'
			b.prevWasIdent = false
		case c == '\'':
			b.flushAssignLHSAsRefs()
			b.s.ScanSimpleString('\'')
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '\''
			b.prevWasIdent = false
		case c == '`':
			b.flushAssignLHSAsRefs()
			b.scanInterpolatedString('`')
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '`'
			b.prevWasIdent = false
		case c == '/' && b.regexOK:
			b.s.ScanSlashRegex()
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '/'
			b.prevWasIdent = false
		case c == '<' && b.s.PeekAt(1) == '<' && b.regexOK:
			if b.tryHeredocTag() {
				continue
			}
			b.s.Advance(2)
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '<'
			b.prevWasIdent = false
		case c == ':' && b.s.PeekAt(1) == ':':
			// `::` module-path separator. Treat following ident as property.
			b.s.Advance(2)
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = ':'
			b.prevWasIdent = false
			// Mark so the next ident is a property_access ref.
			b.scanPropertyAfter()
			continue
		case c == ':' && (lexkit.DefaultIdentStart[b.s.PeekAt(1)] || b.s.PeekAt(1) == '"' || b.s.PeekAt(1) == '\''):
			// Symbol literal :foo or :"foo".
			b.s.Pos++
			switch b.s.Peek() {
			case '"':
				b.s.ScanInterpolatedString('"', "#{", b.rbSkipInterp)
			case '\'':
				b.s.ScanSimpleString('\'')
			default:
				b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = ':'
			b.prevWasIdent = false
		case c == ':':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = ':'
			b.prevWasIdent = false
		case c == '.':
			b.s.Pos++
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = '.'
			b.prevWasIdent = false
			b.assignHadDot = true
			b.scanPropertyAfter()
		case c == '(':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '('
			b.prevWasIdent = false
		case c == ')':
			b.s.Pos++
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = ')'
			b.prevWasIdent = false
		case c == '[':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '['
			b.prevWasIdent = false
		case c == ']':
			b.s.Pos++
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = ']'
			b.prevWasIdent = false
		case c == '{':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			if b.braceIsBlock {
				b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1), rbOpenerBrace)
				b.pipeParamsExpected = true
				b.scanPipeParams()
				b.braceIsBlock = false
			}
			b.prevByte = '{'
			b.prevWasIdent = false
		case c == '}':
			b.s.Pos++
			// Close a block scope if the top is a brace-opened block.
			if top := b.stack.Top(); top != nil && top.Data.opener == rbOpenerBrace {
				b.closeTopScope(uint32(b.s.Pos))
			}
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '}'
			b.prevWasIdent = false
		case c == ',':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = ','
			b.prevWasIdent = false
			// A comma in a comma-separated LHS keeps the pending LHS alive.
		case c == '|':
			// `|` outside of inPipeParams is logical-or (`||`) or bitwise-or.
			// We just pass through; pipe param scanning is invoked right
			// after the opener (`do` or `{`).
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '|'
			b.prevWasIdent = false
		case c == '@':
			b.handleIvar()
		case c == '$':
			b.handleGlobalVar()
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			// Optional trailing `?` or `!` on method-style names.
			if !b.s.EOF() {
				nc := b.s.Peek()
				if nc == '?' || nc == '!' {
					b.s.Pos++
					word = append([]byte{}, word...)
					word = append(word, nc)
				}
			}
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '_' && cc != '.' && cc != 'e' && cc != 'E' && cc != 'x' && cc != 'X' {
					break
				}
				b.s.Pos++
			}
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = false
			b.prevByte = '0'
			b.prevWasIdent = false
		default:
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = c
			b.prevWasIdent = false
		}
	}
}

// endStatement fires at '\n' and ';'. Any pending LHS idents that never
// saw a `=` become refs.
func (b *builder) endStatement() {
	b.flushAssignLHSAsRefs()
	b.assignHadDot = false
	b.assignStopped = false
}

// flushAssignLHSAsRefs converts buffered LHS idents to refs. Called when
// a token proves the statement is not an assignment (==, =>, newline, ...).
func (b *builder) flushAssignLHSAsRefs() {
	if len(b.assignLHS) == 0 {
		return
	}
	for _, p := range b.assignLHS {
		if p.ivar {
			// @x without =: reference the class-scope field decl
			// (which handleIvar already emitted on first sight).
			b.emitIvarRef(p.name, p.span)
			continue
		}
		b.emitRef(p.name, p.span)
	}
	b.assignLHS = nil
}

// commitAssignLHS converts buffered LHS idents to decls (fired by `=`).
func (b *builder) commitAssignLHS() {
	if len(b.assignLHS) == 0 {
		return
	}
	for _, p := range b.assignLHS {
		if p.ivar {
			b.ensureIvarDecl(p.name, p.span)
			continue
		}
		if b.assignHadDot {
			// `obj.x = ...` — this is attribute assignment, not a local.
			b.emitRef(p.name, p.span)
			continue
		}
		b.emitDecl(p.name, scope.KindVar, p.span, scope.NSValue)
		b.markLocal(p.name)
	}
	b.assignLHS = nil
	b.assignHadDot = false
}

func (b *builder) handleIdent(word []byte) {
	if len(word) == 0 {
		return
	}
	startByte := uint32(b.s.Pos - len(word))
	endByte := uint32(b.s.Pos)
	name := string(word)
	wasStmtStart := b.stmtStart
	b.stmtStart = false

	// Property access position: `obj.name` — emit as probable ref with
	// property_access reason.
	if b.prevByte == '.' {
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.regexOK = false
		b.prevByte = 'i'
		b.prevWasIdent = true
		b.braceIsBlock = true
		return
	}

	// Keywords.
	switch name {
	case "def":
		b.handleDefKeyword(startByte)
		return
	case "class":
		b.handleClassKeyword(startByte)
		return
	case "module":
		b.handleModuleKeyword(startByte)
		return
	case "end":
		b.handleEndKeyword(endByte)
		return
	case "do":
		// `do` always opens a block scope (it's not a modifier).
		b.openScope(scope.ScopeBlock, startByte, rbOpenerDo)
		b.pipeParamsExpected = true
		b.scanPipeParams()
		b.regexOK = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	case "begin":
		if wasStmtStart || b.stmtStartAfterOp() {
			b.openScope(scope.ScopeBlock, startByte, rbOpenerBegin)
		}
		b.regexOK = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	case "if", "unless", "while", "until", "case", "for":
		if wasStmtStart {
			var opener rbOpener
			switch name {
			case "if":
				opener = rbOpenerIf
			case "unless":
				opener = rbOpenerUnless
			case "while":
				opener = rbOpenerWhile
			case "until":
				opener = rbOpenerUntil
			case "case":
				opener = rbOpenerCase
			case "for":
				opener = rbOpenerFor
			}
			b.openScope(scope.ScopeBlock, startByte, opener)
		}
		// As a modifier, does not open a scope.
		b.regexOK = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	case "require", "require_relative", "load":
		if wasStmtStart {
			b.parseRequire(name)
			return
		}
		// Otherwise: treat as an ordinary method call (ref).
		b.emitRef(name, mkSpan(startByte, endByte))
		b.regexOK = true
		b.prevByte = 'i'
		b.prevWasIdent = true
		b.braceIsBlock = true
		return
	case "attr_reader", "attr_writer", "attr_accessor":
		b.parseAttrs(name)
		return
	case "return", "yield", "raise", "and", "or", "not", "in", "then",
		"else", "elsif", "when", "break", "next", "redo", "retry",
		"rescue", "ensure", "defined?":
		b.regexOK = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	case "true", "false", "nil", "self", "super", "__FILE__", "__LINE__":
		b.regexOK = false
		b.stmtStart = false
		b.braceIsBlock = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	case "lambda", "proc":
		// Treat as ordinary method calls; a following { or do will push
		// a block scope via the normal machinery.
		b.emitRef(name, mkSpan(startByte, endByte))
		b.regexOK = true
		b.prevByte = 'i'
		b.prevWasIdent = true
		b.braceIsBlock = true
		return
	}

	// Inside a pipe-param list: these idents are block params.
	if b.inPipeParams {
		b.emitDecl(name, scope.KindParam, mkSpan(startByte, endByte), scope.NSValue)
		b.markLocal(name)
		b.prevByte = 'i'
		b.prevWasIdent = true
		b.regexOK = false
		b.braceIsBlock = false
		return
	}

	// Statement-start bare ident: candidate assignment LHS.
	if wasStmtStart || (b.prevByte == ',' && len(b.assignLHS) > 0 && !b.assignStopped) {
		// Peek: if next is non-whitespace and not `=`, `,`, or `(`,
		// it's still ambiguous — keep it as pending LHS. The `=`, `\n`,
		// `==`, `=>`, etc. handlers decide.
		b.assignLHS = append(b.assignLHS, pendingAssign{
			name: name,
			span: mkSpan(startByte, endByte),
		})
		b.regexOK = false
		b.prevByte = 'i'
		b.prevWasIdent = true
		b.braceIsBlock = true
		return
	}

	// Any other ident in expression position: emit as ref. The resolver
	// will bind to a local if the name has been seen as an assignment
	// target in an enclosing scope, else fall through as a probable
	// method call.
	b.emitRef(name, mkSpan(startByte, endByte))
	b.regexOK = false
	b.prevByte = 'i'
	b.prevWasIdent = true
	b.braceIsBlock = true
}

// stmtStartAfterOp returns true when the prev byte is an operator or
// grouping opener — positions where `begin` is still an expression head.
func (b *builder) stmtStartAfterOp() bool {
	switch b.prevByte {
	case 0, '(', '[', '=', ',', '|', '&', ';':
		return true
	}
	return false
}

// handleDefKeyword consumes `def [self.]name[(params)]`. It emits the
// method decl into the current scope (usually a class) and opens a
// function scope for the body. Params are emitted as KindParam inside
// the function scope.
func (b *builder) handleDefKeyword(kwStart uint32) {
	b.pendingFullStart = kwStart + 1
	b.s.SkipSpaces()
	// Check for `def self.foo`.
	b.pendingDefIsSelf = false
	save := b.s.Pos
	if b.s.Peek() == 's' {
		// Look for "self." prefix.
		word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		if string(word) == "self" {
			b.s.SkipSpaces()
			if b.s.Peek() == '.' {
				b.s.Pos++
				b.s.SkipSpaces()
				b.pendingDefIsSelf = true
			} else {
				// Not `def self.` — back up.
				b.s.Pos = save
			}
		} else {
			b.s.Pos = save
		}
	}
	// Receiver.method form: `def receiver.name` (rare). Detect by
	// scanning an ident followed by `.`.
	if !b.pendingDefIsSelf && !b.s.EOF() && lexkit.DefaultIdentStart[b.s.Peek()] {
		svp := b.s.Pos
		_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		// Check for trailing `?` / `!` — only valid on method-name, not on receiver.
		endIdent := b.s.Pos
		b.s.SkipSpaces()
		if !b.s.EOF() && b.s.Peek() == '.' {
			// It's receiver.method: skip the receiver, consume '.'
			b.s.Pos++
			b.s.SkipSpaces()
			b.pendingDefIsSelf = true
			_ = endIdent
		} else {
			b.s.Pos = svp
		}
	}
	// Now scan the method name.
	if b.s.EOF() || !lexkit.DefaultIdentStart[b.s.Peek()] {
		// Couldn't find a method name; treat as a lone keyword.
		b.pendingFullStart = 0
		b.regexOK = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	}
	nameStart := b.s.Pos
	_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	if !b.s.EOF() {
		nc := b.s.Peek()
		if nc == '?' || nc == '!' || nc == '=' {
			// Only take `=` as part of a method name if it's a proper
			// setter (name=), not `def foo = expr` endless method.
			if nc == '=' {
				// Endless method: def foo = expr — do NOT consume `=`.
				// Setter form def foo= is `def foo=(v)`: the `=` is only
				// consumed when immediately followed by `(`. The Python-
				// style rule here:
				next := b.s.PeekAt(1)
				if next == '(' {
					b.s.Pos++
				}
			} else {
				b.s.Pos++
			}
		}
	}
	nameEnd := b.s.Pos
	methodName := string(b.s.Src[nameStart:nameEnd])

	// Emit the method decl in the current scope.
	scopeK := b.currentScopeKind()
	ns := scope.NSValue
	kind := scope.KindFunction
	if scopeK == scope.ScopeClass || scopeK == scope.ScopeNamespace {
		ns = scope.NSField
		kind = scope.KindMethod
	}
	b.emitDecl(methodName, kind, mkSpan(uint32(nameStart), uint32(nameEnd)), ns)

	// Parse optional param list in parens, OR a bare param list terminated
	// by a newline. Collect param names to emit inside the function scope.
	var params []pendingAssign
	b.s.SkipSpaces()
	if !b.s.EOF() && b.s.Peek() == '(' {
		b.s.Pos++
		params = b.scanDefParams(')')
	} else if !b.s.EOF() && b.s.Peek() != '\n' && b.s.Peek() != '#' && b.s.Peek() != ';' {
		// Bare param list — scan until newline or `;`.
		params = b.scanDefParams(0)
	}

	// Endless method: `def foo = expr` — single-line; no body scope.
	b.s.SkipSpaces()
	if !b.s.EOF() && b.s.Peek() == '=' && b.s.PeekAt(1) != '=' && b.s.PeekAt(1) != '>' && b.s.PeekAt(1) != '~' {
		b.s.Pos++
		// Open a transient function scope so params + body refs stay
		// grouped, and close it at end-of-line.
		b.openScope(scope.ScopeFunction, kwStart, rbOpenerDef)
		for _, p := range params {
			b.emitDecl(p.name, scope.KindParam, p.span, scope.NSValue)
			b.markLocal(p.name)
		}
		b.regexOK = true
		b.prevByte = '='
		b.prevWasIdent = false
		// Main loop will close via `\n` + we mark this scope as do-close-on-newline?
		// Simpler: let the main loop run, and when we see `\n` close if still
		// at this scope with an endless-method flag. Rather than add state,
		// just close immediately — the body will be handled as top-level code,
		// which doesn't match semantics perfectly but keeps v1 simple.
		b.closeTopScope(uint32(b.s.Pos))
		return
	}

	// Regular def: open function scope; emit params inside.
	b.openScope(scope.ScopeFunction, kwStart, rbOpenerDef)
	for _, p := range params {
		b.emitDecl(p.name, scope.KindParam, p.span, scope.NSValue)
		b.markLocal(p.name)
	}
	b.regexOK = true
	b.prevByte = 'k'
	b.prevWasIdent = false
}

// scanDefParams reads method parameter names up to a terminator.
// If terminator == ')', stops at and consumes ')'.
// If terminator == 0, stops at newline, ';', or '#'.
// Handles default values, splat (*), double-splat (**), and block (&) markers.
func (b *builder) scanDefParams(terminator byte) []pendingAssign {
	var out []pendingAssign
	needName := true
	depth := 0
	for !b.s.EOF() {
		c := b.s.Peek()
		if terminator == ')' {
			if c == ')' && depth == 0 {
				b.s.Pos++
				return out
			}
		} else {
			if c == '\n' || c == ';' || c == '#' {
				return out
			}
		}
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == ',' && depth == 0:
			b.s.Pos++
			needName = true
		case c == '(' || c == '[' || c == '{':
			b.s.Pos++
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				// Mismatched close — bail.
				return out
			}
			b.s.Pos++
			depth--
		case c == '"':
			b.s.ScanInterpolatedString('"', "#{", b.rbSkipInterp)
		case c == '\'':
			b.s.ScanSimpleString('\'')
		case c == '*' || c == '&':
			// splat/block marker; the name (if any) follows.
			b.s.Pos++
			if b.s.Peek() == '*' {
				b.s.Pos++
			}
		case c == '=' && depth == 0:
			// Default-value: skip past expression until comma/terminator.
			b.s.Pos++
			// consume until , or terminator at depth 0
			for !b.s.EOF() {
				cc := b.s.Peek()
				if cc == '(' || cc == '[' || cc == '{' {
					depth++
					b.s.Pos++
					continue
				}
				if cc == ')' || cc == ']' || cc == '}' {
					if depth == 0 {
						break
					}
					depth--
					b.s.Pos++
					continue
				}
				if cc == ',' && depth == 0 {
					break
				}
				if terminator == 0 && (cc == '\n' || cc == ';' || cc == '#') && depth == 0 {
					break
				}
				if cc == '"' {
					b.s.ScanInterpolatedString('"', "#{", b.rbSkipInterp)
					continue
				}
				if cc == '\'' {
					b.s.ScanSimpleString('\'')
					continue
				}
				b.s.Pos++
			}
		case c == ':':
			// Keyword arg (`name:` or `name: default`). The preceding ident
			// was the param name.
			b.s.Pos++
			needName = false
		case lexkit.DefaultIdentStart[c]:
			ns := b.s.Pos
			_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			ne := b.s.Pos
			if needName && depth == 0 {
				out = append(out, pendingAssign{
					name: string(b.s.Src[ns:ne]),
					span: mkSpan(uint32(ns), uint32(ne)),
				})
				needName = false
			}
		default:
			b.s.Pos++
		}
	}
	return out
}

// handleClassKeyword consumes `class Name [< Super]` and opens a class
// scope. Superclass (if present) is emitted as a ref.
func (b *builder) handleClassKeyword(kwStart uint32) {
	b.pendingFullStart = kwStart + 1
	b.s.SkipSpaces()
	// `class << self` — singleton class; treat as a class scope.
	if b.s.Peek() == '<' && b.s.PeekAt(1) == '<' {
		b.s.Advance(2)
		b.s.SkipSpaces()
		// Consume `self` or receiver ident.
		if lexkit.DefaultIdentStart[b.s.Peek()] {
			_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		}
		b.openScope(scope.ScopeClass, kwStart, rbOpenerClass)
		b.regexOK = true
		b.prevByte = 'k'
		b.prevWasIdent = false
		return
	}
	if !lexkit.DefaultIdentStart[b.s.Peek()] {
		b.pendingFullStart = 0
		b.regexOK = true
		b.prevByte = 'k'
		return
	}
	nameStart := b.s.Pos
	fullName := b.scanQualifiedName()
	// Use the leaf component as the actual class name.
	leaf := fullName
	leafStart := nameStart
	if i := strings.LastIndex(fullName, "::"); i >= 0 {
		leaf = fullName[i+2:]
		leafStart = nameStart + i + 2
	}
	leafEnd := leafStart + len(leaf)
	b.emitDecl(leaf, scope.KindClass, mkSpan(uint32(leafStart), uint32(leafEnd)), scope.NSConstant)
	// Open class scope BEFORE checking the optional `< Super` so that
	// Super is emitted as a ref at the file scope (where it actually lives).
	// Actually, Super should be a ref from the file scope — emit it before
	// opening the class scope.
	b.s.SkipSpaces()
	if !b.s.EOF() && b.s.Peek() == '<' && b.s.PeekAt(1) != '<' {
		b.s.Pos++
		b.s.SkipSpaces()
		if lexkit.DefaultIdentStart[b.s.Peek()] {
			superStart := b.s.Pos
			sName := b.scanQualifiedName()
			superEnd := superStart + len(sName)
			// Only emit the leading component as the ref — the rest is
			// property access. To keep it simple, just ref the full name
			// (we don't care much about resolution here; it's fine for v1).
			leafSuper := sName
			leafSuperStart := superStart
			if i := strings.LastIndex(sName, "::"); i >= 0 {
				leafSuper = sName[i+2:]
				leafSuperStart = superStart + i + 2
			}
			_ = superEnd
			b.emitRef(leafSuper, mkSpan(uint32(leafSuperStart), uint32(leafSuperStart+len(leafSuper))))
		}
	}
	b.openScope(scope.ScopeClass, kwStart, rbOpenerClass)
	b.regexOK = true
	b.prevByte = 'k'
	b.prevWasIdent = false
}

// handleModuleKeyword consumes `module Name` and opens a namespace scope.
func (b *builder) handleModuleKeyword(kwStart uint32) {
	b.pendingFullStart = kwStart + 1
	b.s.SkipSpaces()
	if !lexkit.DefaultIdentStart[b.s.Peek()] {
		b.pendingFullStart = 0
		b.regexOK = true
		b.prevByte = 'k'
		return
	}
	nameStart := b.s.Pos
	fullName := b.scanQualifiedName()
	leaf := fullName
	leafStart := nameStart
	if i := strings.LastIndex(fullName, "::"); i >= 0 {
		leaf = fullName[i+2:]
		leafStart = nameStart + i + 2
	}
	leafEnd := leafStart + len(leaf)
	b.emitDecl(leaf, scope.KindNamespace, mkSpan(uint32(leafStart), uint32(leafEnd)), scope.NSConstant)
	b.openScope(scope.ScopeNamespace, kwStart, rbOpenerModule)
	b.regexOK = true
	b.prevByte = 'k'
	b.prevWasIdent = false
}

// handleEndKeyword closes the top scope. File scope is never closed by
// `end`. Excess `end` (malformed source) is a no-op.
func (b *builder) handleEndKeyword(endByte uint32) {
	top := b.stack.Top()
	if top != nil && top.Data.opener != rbOpenerFile {
		b.closeTopScope(endByte)
	}
	b.regexOK = false
	b.stmtStart = false
	b.prevByte = 'k'
	b.prevWasIdent = false
	b.braceIsBlock = true
}

// handleIvar handles `@name` and `@@name`. On assignment (@x = ...), the
// ivar becomes a class-scope NSField decl. On read, it becomes a ref.
func (b *builder) handleIvar() {
	start := b.s.Pos
	b.s.Pos++ // @
	if b.s.Peek() == '@' {
		b.s.Pos++ // @@
	}
	if !lexkit.DefaultIdentStart[b.s.Peek()] {
		// Just a stray '@' — bail.
		b.regexOK = true
		b.prevByte = '@'
		return
	}
	_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	name := string(b.s.Src[start:b.s.Pos])
	span := mkSpan(uint32(start), uint32(b.s.Pos))

	// If at statement-start (could be LHS) OR extending a comma-LHS, buffer.
	if b.stmtStart || (b.prevByte == ',' && len(b.assignLHS) > 0 && !b.assignStopped) {
		b.assignLHS = append(b.assignLHS, pendingAssign{
			name: name,
			span: span,
			ivar: true,
		})
	} else {
		b.emitIvarRef(name, span)
	}
	b.stmtStart = false
	b.regexOK = false
	b.prevByte = 'i'
	b.prevWasIdent = true
	b.braceIsBlock = true
}

// ensureIvarDecl emits a class-scope NSField decl for `name` (a full
// `@foo` or `@@foo`) if one does not already exist in the enclosing class
// scope. Also emits a ref at the given span so the assignment location is
// still represented in the result's Refs.
func (b *builder) ensureIvarDecl(name string, span scope.Span) {
	classScope := b.enclosingClassScope()
	if classScope == 0 {
		// No enclosing class — e.g. an ivar at the top level of a script.
		// Emit as a file-scope field decl instead.
		classScope = b.currentScope()
	}
	if b.ivarsByClassScope == nil {
		b.ivarsByClassScope = make(map[scope.ScopeID]map[string]bool)
	}
	set, ok := b.ivarsByClassScope[classScope]
	if !ok {
		set = make(map[string]bool)
		b.ivarsByClassScope[classScope] = set
	}
	if !set[name] {
		set[name] = true
		// Emit the decl at the (classScope). We need to temporarily
		// redirect emitDecl's scope; easiest path is to append directly.
		locID := hashLoc(b.file, span, name)
		declID := hashDecl(b.file, name, scope.NSField, classScope)
		b.res.Decls = append(b.res.Decls, scope.Decl{
			ID:        declID,
			LocID:     locID,
			Name:      name,
			Namespace: scope.NSField,
			Kind:      scope.KindField,
			Scope:     classScope,
			File:      b.file,
			Span:      span,
			FullSpan:  span,
		})
	}
	// Emit a resolved ref at this location pointing at the class-scope decl.
	b.emitIvarRef(name, span)
}

// emitIvarRef emits a ref to an ivar name. Namespace is NSField so it
// binds against the class-scope ivar decl rather than any same-named
// top-level decl.
func (b *builder) emitIvarRef(name string, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSField,
		Scope:     scopeID,
	})
}

// handleGlobalVar handles `$name`. Emits a ref (no decl — globals are
// out of scope for per-file extraction).
func (b *builder) handleGlobalVar() {
	start := b.s.Pos
	b.s.Pos++ // $
	if !lexkit.DefaultIdentStart[b.s.Peek()] {
		// Special globals like $!, $_, $1 — skip one char.
		if !b.s.EOF() {
			b.s.Pos++
		}
		b.regexOK = false
		b.prevByte = 'i'
		return
	}
	_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	name := string(b.s.Src[start:b.s.Pos])
	span := mkSpan(uint32(start), uint32(b.s.Pos))
	// Globals always resolve externally; emit with a preset Probable/external
	// binding so the resolver doesn't overwrite it with "missing_import".
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     hashLoc(b.file, span, name),
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSValue,
		Scope:     b.currentScope(),
		Binding: scope.RefBinding{
			Kind:   scope.BindProbable,
			Reason: "global_variable",
		},
	})
	b.stmtStart = false
	b.regexOK = false
	b.prevByte = 'i'
	b.prevWasIdent = true
	b.braceIsBlock = true
}

// parseRequire consumes `require 'path'` or `require_relative "path"`.
// Emits an import decl with the path as the name.
func (b *builder) parseRequire(kind string) {
	startByte := uint32(b.s.Pos - len(kind))
	b.s.SkipSpaces()
	hasParen := false
	if !b.s.EOF() && b.s.Peek() == '(' {
		b.s.Pos++
		b.s.SkipSpaces()
		hasParen = true
	}
	if b.s.EOF() {
		return
	}
	c := b.s.Peek()
	var path string
	var pathStart, pathEnd int
	switch c {
	case '\'':
		b.s.Pos++
		pathStart = b.s.Pos
		for !b.s.EOF() && b.s.Peek() != '\'' && b.s.Peek() != '\n' {
			b.s.Pos++
		}
		pathEnd = b.s.Pos
		if !b.s.EOF() && b.s.Peek() == '\'' {
			b.s.Pos++
		}
		path = string(b.s.Src[pathStart:pathEnd])
	case '"':
		b.s.Pos++
		pathStart = b.s.Pos
		for !b.s.EOF() && b.s.Peek() != '"' && b.s.Peek() != '\n' {
			b.s.Pos++
		}
		pathEnd = b.s.Pos
		if !b.s.EOF() && b.s.Peek() == '"' {
			b.s.Pos++
		}
		path = string(b.s.Src[pathStart:pathEnd])
	default:
		b.stmtStart = false
		b.regexOK = false
		return
	}
	if hasParen {
		b.s.SkipSpaces()
		if !b.s.EOF() && b.s.Peek() == ')' {
			b.s.Pos++
		}
	}
	if path != "" {
		// Emit an import decl at the string-literal span; name is the path.
		span := mkSpan(uint32(pathStart), uint32(pathEnd))
		scopeID := b.currentScope()
		locID := hashLoc(b.file, span, path)
		declID := hashDecl(b.file, path, scope.NSValue, scopeID)
		b.res.Decls = append(b.res.Decls, scope.Decl{
			ID:        declID,
			LocID:     locID,
			Name:      path,
			Namespace: scope.NSValue,
			Kind:      scope.KindImport,
			Scope:     scopeID,
			File:      b.file,
			Span:      span,
			FullSpan:  scope.Span{StartByte: startByte, EndByte: uint32(pathEnd + 1)},
		})
	}
	b.stmtStart = false
	b.regexOK = false
	b.prevByte = 'i'
	b.prevWasIdent = false
}

// parseAttrs consumes `attr_reader :a, :b` / `attr_accessor :x` — each
// symbol argument becomes a field decl in the current (class) scope.
// Also emits a ref for the attr_* method itself.
func (b *builder) parseAttrs(kind string) {
	// The attr_* identifier we just scanned becomes a ref.
	endByte := uint32(b.s.Pos)
	startByte := endByte - uint32(len(kind))
	b.emitRef(kind, mkSpan(startByte, endByte))
	b.s.SkipSpaces()
	hasParen := false
	if !b.s.EOF() && b.s.Peek() == '(' {
		b.s.Pos++
		b.s.SkipSpaces()
		hasParen = true
	}
	// Parse comma-separated :sym or "str" args.
	for !b.s.EOF() {
		b.s.SkipSpaces()
		c := b.s.Peek()
		if c == '\n' || c == ';' || c == '#' {
			break
		}
		if hasParen && c == ')' {
			b.s.Pos++
			break
		}
		switch c {
		case ':':
			// :symbol
			b.s.Pos++
			if !b.s.EOF() && lexkit.DefaultIdentStart[b.s.Peek()] {
				ns := b.s.Pos
				_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				name := string(b.s.Src[ns:b.s.Pos])
				b.emitDecl(name, scope.KindField, mkSpan(uint32(ns), uint32(b.s.Pos)), scope.NSField)
			} else if !b.s.EOF() && (b.s.Peek() == '"' || b.s.Peek() == '\'') {
				quote := b.s.Peek()
				b.s.Pos++
				ns := b.s.Pos
				for !b.s.EOF() && b.s.Peek() != quote && b.s.Peek() != '\n' {
					b.s.Pos++
				}
				ne := b.s.Pos
				if !b.s.EOF() && b.s.Peek() == quote {
					b.s.Pos++
				}
				name := string(b.s.Src[ns:ne])
				b.emitDecl(name, scope.KindField, mkSpan(uint32(ns), uint32(ne)), scope.NSField)
			}
		case '"':
			b.s.Pos++
			ns := b.s.Pos
			for !b.s.EOF() && b.s.Peek() != '"' && b.s.Peek() != '\n' {
				b.s.Pos++
			}
			ne := b.s.Pos
			if !b.s.EOF() && b.s.Peek() == '"' {
				b.s.Pos++
			}
			name := string(b.s.Src[ns:ne])
			b.emitDecl(name, scope.KindField, mkSpan(uint32(ns), uint32(ne)), scope.NSField)
		case '\'':
			b.s.Pos++
			ns := b.s.Pos
			for !b.s.EOF() && b.s.Peek() != '\'' && b.s.Peek() != '\n' {
				b.s.Pos++
			}
			ne := b.s.Pos
			if !b.s.EOF() && b.s.Peek() == '\'' {
				b.s.Pos++
			}
			name := string(b.s.Src[ns:ne])
			b.emitDecl(name, scope.KindField, mkSpan(uint32(ns), uint32(ne)), scope.NSField)
		case ',':
			b.s.Pos++
		default:
			// Unknown — bail out to avoid infinite loop.
			b.s.Pos++
			if !(c == ' ' || c == '\t') {
				// Not whitespace: give up on this arg.
				b.regexOK = false
				b.stmtStart = false
				return
			}
		}
	}
	b.regexOK = true
	b.stmtStart = false
	b.prevByte = 'i'
	b.prevWasIdent = false
}

// scanPipeParams is called right after a scope opener (do or {) to pick
// up an optional `|x, y, *rest, &blk|` parameter list. It advances past
// whitespace and if a `|` is present, scans up to the matching `|`,
// emitting each ident as a KindParam decl in the current scope.
func (b *builder) scanPipeParams() {
	// skip whitespace and continuation backslashes.
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' {
			b.s.Pos++
			continue
		}
		if c == '\\' && b.s.PeekAt(1) == '\n' {
			b.s.Advance(2)
			continue
		}
		break
	}
	if b.s.EOF() || b.s.Peek() != '|' {
		b.pipeParamsExpected = false
		return
	}
	b.s.Pos++ // opening |
	b.inPipeParams = true
	depth := 0
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '|' && depth == 0 {
			b.s.Pos++
			break
		}
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			b.s.Pos++
		case c == ',':
			b.s.Pos++
		case c == '(' || c == '[':
			b.s.Pos++
			depth++
		case c == ')' || c == ']':
			if depth == 0 {
				return
			}
			b.s.Pos++
			depth--
		case c == '*' || c == '&':
			b.s.Pos++
			if b.s.Peek() == '*' {
				b.s.Pos++
			}
		case c == '=':
			// Default value: skip until , or |.
			b.s.Pos++
			for !b.s.EOF() {
				cc := b.s.Peek()
				if (cc == ',' || cc == '|') && depth == 0 {
					break
				}
				if cc == '(' || cc == '[' {
					depth++
					b.s.Pos++
					continue
				}
				if cc == ')' || cc == ']' {
					if depth == 0 {
						break
					}
					depth--
					b.s.Pos++
					continue
				}
				if cc == '"' {
					b.s.ScanInterpolatedString('"', "#{", b.rbSkipInterp)
					continue
				}
				if cc == '\'' {
					b.s.ScanSimpleString('\'')
					continue
				}
				b.s.Pos++
			}
		case lexkit.DefaultIdentStart[c]:
			ns := b.s.Pos
			_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			name := string(b.s.Src[ns:b.s.Pos])
			if depth == 0 {
				b.emitDecl(name, scope.KindParam, mkSpan(uint32(ns), uint32(b.s.Pos)), scope.NSValue)
				b.markLocal(name)
			}
		default:
			b.s.Pos++
		}
	}
	b.inPipeParams = false
	b.pipeParamsExpected = false
	b.regexOK = true
	b.prevByte = '|'
	b.prevWasIdent = false
}

// scanPropertyAfter scans the ident following `.` or `::` and emits it
// as a property-access ref. Leaves position just past the ident.
func (b *builder) scanPropertyAfter() {
	// Skip whitespace.
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' {
			b.s.Pos++
			continue
		}
		break
	}
	if b.s.EOF() || !lexkit.DefaultIdentStart[b.s.Peek()] {
		return
	}
	ns := b.s.Pos
	_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	// Optional ?/!
	if !b.s.EOF() {
		nc := b.s.Peek()
		if nc == '?' || nc == '!' {
			b.s.Pos++
		}
	}
	name := string(b.s.Src[ns:b.s.Pos])
	b.emitPropertyRef(name, mkSpan(uint32(ns), uint32(b.s.Pos)))
	b.regexOK = false
	b.prevByte = 'i'
	b.prevWasIdent = true
	b.braceIsBlock = true
}

// tryHeredocTag attempts to match a heredoc tag at the current position
// (`<<-TAG`, `<<~TAG`, `<<"TAG"`, etc.). On success, queues the tag to
// read at the next newline and returns true.
func (b *builder) tryHeredocTag() bool {
	src := b.s.Src
	j := b.s.Pos + 2
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
	if j >= len(src) || !lexkit.DefaultIdentStart[src[j]] {
		return false
	}
	tagStart := j
	for j < len(src) && lexkit.DefaultIdentCont[src[j]] {
		j++
	}
	tag := string(src[tagStart:j])
	if quote != 0 {
		if j >= len(src) || src[j] != quote {
			return false
		}
		j++
	}
	b.s.Pos = j
	b.pendingHeredocs = append(b.pendingHeredocs, rbHeredoc{tag: tag, squiggly: squiggly, interp: interp})
	b.regexOK = false
	b.stmtStart = false
	return true
}

// readHeredocBody scans lines until it finds a line whose trimmed content
// equals the tag.
func (b *builder) readHeredocBody(h rbHeredoc) {
	for !b.s.EOF() {
		lineStart := b.s.Pos
		for !b.s.EOF() && b.s.Peek() != '\n' {
			b.s.Pos++
		}
		lineText := string(b.s.Src[lineStart:b.s.Pos])
		check := strings.TrimLeft(lineText, " \t")
		trimmed := strings.TrimRight(check, " \t\r")
		if !b.s.EOF() {
			b.s.Next()
		}
		if trimmed == h.tag {
			return
		}
	}
	_ = h.squiggly
	_ = h.interp
}

// skipPodComment advances past a =begin ... =end block comment if the
// scanner is positioned at "=begin" at start-of-line. Returns true if
// one was consumed.
func (b *builder) skipPodComment() bool {
	src := b.s.Src
	pos := b.s.Pos
	end := pos
	for end < len(src) && src[end] != '\n' {
		end++
	}
	line := strings.TrimRight(string(src[pos:end]), "\r")
	if line != "=begin" {
		return false
	}
	b.s.Pos = end
	if b.s.Pos < len(src) {
		b.s.Next()
	}
	for !b.s.EOF() {
		lineStart := b.s.Pos
		lineEnd := lineStart
		for lineEnd < len(src) && src[lineEnd] != '\n' {
			lineEnd++
		}
		lineText := strings.TrimRight(string(src[lineStart:lineEnd]), "\r")
		b.s.Pos = lineEnd
		if b.s.Pos < len(src) {
			b.s.Next()
		}
		if lineText == "=end" {
			break
		}
	}
	b.stmtStart = true
	b.regexOK = true
	return true
}

// scanInterpolatedString scans a double-quoted or backtick-delimited
// string, recursively tokenising any `#{...}` interpolation bodies so
// identifiers inside them are emitted as refs/decls. Handles backslash
// escapes and single-line newlines. Not used for single-quoted strings
// (which don't support interpolation).
func (b *builder) scanInterpolatedString(quote byte) {
	if b.s.EOF() || b.s.Peek() != quote {
		return
	}
	b.s.Pos++ // opening quote
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '\\' && b.s.PeekAt(1) != 0 {
			b.s.Advance(2)
			continue
		}
		if c == quote {
			b.s.Pos++
			return
		}
		if c == '#' && b.s.PeekAt(1) == '{' {
			b.s.Advance(2)
			b.runInterpBody()
			continue
		}
		if c == '\n' {
			b.s.Next()
			continue
		}
		b.s.Pos++
	}
}

// runInterpBody runs the main token loop until a matching `}` closes
// the current `#{...}` body. Tracks brace depth to allow nested { } hashes
// or block arguments inside an interpolation.
func (b *builder) runInterpBody() {
	depth := 1
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.s.Next()
			b.endStatement()
			b.stmtStart = true
			b.regexOK = true
			b.prevByte = '\n'
		case c == '#':
			b.s.SkipLineComment()
		case c == '{':
			depth++
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '{'
			b.prevWasIdent = false
		case c == '}':
			depth--
			b.s.Pos++
			if depth == 0 {
				return
			}
			// Close a brace-block scope if our top opener is one.
			if top := b.stack.Top(); top != nil && top.Data.opener == rbOpenerBrace {
				b.closeTopScope(uint32(b.s.Pos))
			}
			b.regexOK = false
			b.prevByte = '}'
			b.prevWasIdent = false
			b.braceIsBlock = true
		case c == '"':
			b.flushAssignLHSAsRefs()
			b.scanInterpolatedString('"')
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '"'
			b.prevWasIdent = false
		case c == '\'':
			b.flushAssignLHSAsRefs()
			b.s.ScanSimpleString('\'')
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '\''
			b.prevWasIdent = false
		case c == '`':
			b.flushAssignLHSAsRefs()
			b.scanInterpolatedString('`')
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '`'
			b.prevWasIdent = false
		case c == '/' && b.regexOK:
			b.s.ScanSlashRegex()
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = '/'
			b.prevWasIdent = false
		case c == '(':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '('
			b.prevWasIdent = false
		case c == ')':
			b.s.Pos++
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = ')'
			b.prevWasIdent = false
		case c == '[':
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = '['
			b.prevWasIdent = false
		case c == ']':
			b.s.Pos++
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = true
			b.prevByte = ']'
			b.prevWasIdent = false
		case c == ',':
			b.s.Pos++
			b.regexOK = true
			b.prevByte = ','
			b.prevWasIdent = false
		case c == '.':
			b.s.Pos++
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = '.'
			b.prevWasIdent = false
			b.scanPropertyAfter()
		case c == ':' && b.s.PeekAt(1) == ':':
			b.s.Advance(2)
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = ':'
			b.prevWasIdent = false
			b.scanPropertyAfter()
		case c == '@':
			b.handleIvar()
		case c == '$':
			b.handleGlobalVar()
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			if !b.s.EOF() {
				nc := b.s.Peek()
				if nc == '?' || nc == '!' {
					b.s.Pos++
					word = append([]byte{}, word...)
					word = append(word, nc)
				}
			}
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '_' && cc != '.' && cc != 'e' && cc != 'E' && cc != 'x' && cc != 'X' {
					break
				}
				b.s.Pos++
			}
			b.regexOK = false
			b.stmtStart = false
			b.braceIsBlock = false
			b.prevByte = '0'
			b.prevWasIdent = false
		default:
			b.s.Pos++
			b.regexOK = true
			b.stmtStart = false
			b.prevByte = c
			b.prevWasIdent = false
		}
	}
}

// rbSkipInterp skips a `#{...}` interpolation body. This is a closure
// around the scanner so it can be passed to ScanInterpolatedString.
func (b *builder) rbSkipInterp(s *lexkit.Scanner) {
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
			s.ScanInterpolatedString('"', "#{", b.rbSkipInterp)
		case '\'':
			s.ScanSimpleString('\'')
		case '#':
			s.SkipLineComment()
		case '\n':
			s.Next()
		default:
			s.Pos++
		}
	}
}

// scanQualifiedName reads a possibly-qualified constant name like
// `Foo::Bar::Baz`. Returns the full qualified string; callers may split
// on `::` for the leaf component.
func (b *builder) scanQualifiedName() string {
	start := b.s.Pos
	for !b.s.EOF() {
		if !lexkit.DefaultIdentStart[b.s.Peek()] {
			break
		}
		for !b.s.EOF() && lexkit.DefaultIdentCont[b.s.Peek()] {
			b.s.Pos++
		}
		if b.s.Pos+1 < len(b.s.Src) && b.s.Peek() == ':' && b.s.PeekAt(1) == ':' {
			b.s.Pos += 2
			continue
		}
		break
	}
	return string(b.s.Src[start:b.s.Pos])
}

// openScope pushes a new scope onto the stack and allocates a ScopeID.
func (b *builder) openScope(kind scope.ScopeKind, startByte uint32, opener rbOpener) {
	id := scope.ScopeID(len(b.res.Scopes) + 1)
	var parent scope.ScopeID
	if top := b.stack.Top(); top != nil {
		parent = top.Data.id
	}
	b.res.Scopes = append(b.res.Scopes, scope.Scope{
		ID:     id,
		Parent: parent,
		Kind:   kind,
		Span:   scope.Span{StartByte: startByte, EndByte: 0},
	})
	owner := b.pendingOwnerDecl
	b.pendingOwnerDecl = -1
	b.stack.Push(lexkit.Scope[scopeEntry]{
		Data: scopeEntry{
			kind:         kind,
			id:           id,
			opener:       opener,
			ownerDeclIdx: owner,
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
	b.localSets = append(b.localSets, make(map[string]bool))
}

// closeTopScope pops the top scope and patches its EndByte / the owner
// decl's FullSpan.EndByte.
func (b *builder) closeTopScope(endByte uint32) {
	e, ok := b.stack.Pop()
	if !ok {
		return
	}
	idx := int(e.Data.id) - 1
	if idx >= 0 && idx < len(b.res.Scopes) {
		b.res.Scopes[idx].Span.EndByte = endByte
	}
	if o := e.Data.ownerDeclIdx; o >= 0 && o < len(b.res.Decls) {
		if b.res.Decls[o].FullSpan.EndByte < endByte {
			b.res.Decls[o].FullSpan.EndByte = endByte
		}
	}
	if n := len(b.localSets); n > 0 {
		b.localSets = b.localSets[:n-1]
	}
}

func (b *builder) closeScopesToDepth(depth int) {
	endByte := uint32(len(b.s.Src))
	for b.stack.Depth() > depth {
		b.closeTopScope(endByte)
	}
}

func (b *builder) currentScope() scope.ScopeID {
	if top := b.stack.Top(); top != nil {
		return top.Data.id
	}
	return 0
}

func (b *builder) currentScopeKind() scope.ScopeKind {
	if top := b.stack.Top(); top != nil {
		return top.Data.kind
	}
	return ""
}

// enclosingClassScope returns the nearest enclosing ScopeClass ID (or 0
// if none). Used to anchor @ivar decls even when we're inside a method's
// function scope.
func (b *builder) enclosingClassScope() scope.ScopeID {
	entries := b.stack.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Data.kind == scope.ScopeClass {
			return entries[i].Data.id
		}
	}
	return 0
}

// markLocal records `name` as an assignment target in the current scope,
// so subsequent same-named refs resolve to a local.
func (b *builder) markLocal(name string) {
	n := len(b.localSets)
	if n == 0 {
		return
	}
	b.localSets[n-1][name] = true
}

func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span, ns scope.Namespace) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	declID := hashDecl(b.file, name, ns, scopeID)

	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}

	idx := len(b.res.Decls)
	b.res.Decls = append(b.res.Decls, scope.Decl{
		ID:        declID,
		LocID:     locID,
		Name:      name,
		Namespace: ns,
		Kind:      kind,
		Scope:     scopeID,
		File:      b.file,
		Span:      span,
		FullSpan:  fullSpan,
	})

	switch kind {
	case scope.KindFunction, scope.KindMethod, scope.KindClass,
		scope.KindNamespace:
		b.pendingOwnerDecl = idx
	}
	b.pendingFullStart = 0
}

func (b *builder) emitRef(name string, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSValue,
		Scope:     scopeID,
	})
}

func (b *builder) emitPropertyRef(name string, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSField,
		Scope:     scopeID,
		Binding: scope.RefBinding{
			Kind:   scope.BindProbable,
			Reason: "property_access",
		},
	})
}

// resolveRefs walks each ref's scope chain and binds it to the innermost
// matching decl. Unresolved value-namespace refs fall through to a
// probable "method_call" binding (Ruby's dynamic nature means bare idents
// may well be a method on self that we can't see here).
func (b *builder) resolveRefs() {
	parent := make(map[scope.ScopeID]scope.ScopeID, len(b.res.Scopes))
	kindOf := make(map[scope.ScopeID]scope.ScopeKind, len(b.res.Scopes))
	for _, s := range b.res.Scopes {
		parent[s.ID] = s.Parent
		kindOf[s.ID] = s.Kind
	}
	type key struct {
		scope scope.ScopeID
		name  string
		ns    scope.Namespace
	}
	byKey := make(map[key]*scope.Decl, len(b.res.Decls))
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		k := key{scope: d.Scope, name: d.Name, ns: d.Namespace}
		if _, ok := byKey[k]; !ok {
			byKey[k] = d
		}
	}
	for i := range b.res.Refs {
		r := &b.res.Refs[i]
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "global_variable" {
			continue
		}
		cur := r.Scope
		resolved := false
		for cur != 0 {
			if d, ok := byKey[key{scope: cur, name: r.Name, ns: r.Namespace}]; ok {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   d.ID,
					Reason: "direct_scope",
				}
				resolved = true
				break
			}
			p, ok := parent[cur]
			if !ok {
				break
			}
			cur = p
		}
		if !resolved && r.Namespace == scope.NSField {
			// @ivar lookup failed — could be an auto-generated accessor
			// (attr_accessor). Still record as probable.
			r.Binding = scope.RefBinding{
				Kind:   scope.BindProbable,
				Reason: "ivar_undeclared",
			}
			continue
		}
		if !resolved {
			r.Binding = scope.RefBinding{
				Kind:   scope.BindProbable,
				Reason: "method_call",
			}
		}
		_ = kindOf
	}
}

func mkSpan(start, end uint32) scope.Span {
	return scope.Span{StartByte: start, EndByte: end}
}

func hashLoc(file string, span scope.Span, name string) scope.LocID {
	h := sha256.New()
	h.Write([]byte(file))
	h.Write([]byte{0})
	h.Write([]byte(name))
	h.Write([]byte{0})
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], span.StartByte)
	binary.LittleEndian.PutUint32(buf[4:8], span.EndByte)
	h.Write(buf[:])
	sum := h.Sum(nil)
	return scope.LocID(binary.LittleEndian.Uint64(sum[:8]))
}

func hashDecl(canonicalPath, name string, ns scope.Namespace, scopeID scope.ScopeID) scope.DeclID {
	h := sha256.New()
	h.Write([]byte(canonicalPath))
	h.Write([]byte{0})
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(ns))
	h.Write([]byte{0})
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(scopeID))
	h.Write(buf[:])
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
