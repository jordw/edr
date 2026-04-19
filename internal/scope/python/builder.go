// Package python is the Python scope + binding extractor.
//
// Python differs from brace-based languages in two key ways:
//
//   1. Blocks are indent-delimited. A def/class at indent N has a body
//      at some indent > N; the body closes when a non-blank, non-comment
//      line at indent <= N appears. No '{' / '}' to push/pop on.
//
//   2. LEGB scoping. Only def/class/lambda/comprehension introduce a new
//      scope — if/for/while/try do NOT. And class bodies are "transparent"
//      for their methods: a method body's LEGB walk skips the enclosing
//      class scope, going straight to the module scope (and builtins).
//
// v1 limitations:
//   - global/nonlocal statements are skipped (don't affect resolution).
//   - Walrus operator `(x := v)` doesn't emit x as a decl; per PEP 572
//     walrus in a comprehension leaks to the enclosing scope, so leaving
//     the current behavior actually matches Python semantics.
//   - Decorators are refs to the decorator name, but no deeper analysis.
//   - Multi-target assignment `a = b = 1` records only the first target.
//   - Destructuring tuple assignment records all comma-separated LHS
//     targets — including parenthesized groupings (`(a, b) = point`)
//     and starred targets (`a, *rest = xs`). Deeply nested patterns
//     like `(a, (b, c)) = ...` extract every leaf name correctly, but
//     only because the extractor flattens — it does not track structure.
//   - `except E as e` doesn't introduce `e` as a decl.
//   - Augmented assignments (`x += 1`) are treated as refs, not decls.
package python

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Python source buffer.
func Parse(file string, src []byte) *scope.Result {
	b := &builder{
		file:             file,
		res:              &scope.Result{File: file},
		s:                lexkit.New(src),
		atLineStart:      true,
		pendingOwnerDecl: -1,
	}
	b.openScope(scope.ScopeFile, 0, -1)
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	return b.res
}

type scopeEntry struct {
	kind scope.ScopeKind
	id   scope.ScopeID
	// startIndent is the indent of the def/class line (not the body).
	// When a non-blank line at indent <= startIndent appears, this scope
	// pops. File scope has startIndent = -1 so it never pops on indent.
	startIndent int
	// ownerDeclIdx is the index in res.Decls of the decl that owns this
	// scope (def/class). closeTopScope patches FullSpan.EndByte. -1 when
	// the scope has no owning decl (file scope).
	ownerDeclIdx int
	// bracket is true for scopes opened by a comprehension bracket; those
	// scopes ignore indent-based closing (they close on the matching
	// bracket instead).
	bracket bool
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	// Current indent of the line being processed.
	currentIndent int
	// atLineStart is true at the start of each physical line (after '\n'
	// but before any non-whitespace).
	atLineStart bool

	// declContext: next ident is a decl of this kind.
	declContext scope.DeclKind

	// pendingScope: a def/class/lambda keyword saw its name; when the
	// body starts (after ':' and a newline with greater indent), push
	// this scope kind.
	pendingScope   *scope.ScopeKind
	pendingScopeAt int // the indent of the def/class line

	// paramList / paramSectionNeedsName: for def(...) signature parsing.
	// In Python, params are collected inside (...) and flushed into the
	// function body scope when it opens.
	paramListPending      bool
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool
	pendingParams         []pendingParam

	// importState: tracking `import X` vs `from X import Y as Z`.
	inImport     bool
	inFromImport bool
	// fromImportTarget is true once we've seen `from X import` and are
	// collecting imported-name decls (until newline).
	fromImportTarget bool

	// Import-graph signature tracking (drives resolveImportsPython in
	// internal/scope/store). For each decl emitted by an import statement
	// we stamp Signature = "<modulePath>\x00<origName>" so the cross-file
	// resolver can rewrite refs to their exported source.
	//
	// fromModulePath accumulates the module path as the import statement
	// is parsed: "foo.bar" for `from foo.bar import X`, "..foo" for
	// `from ..foo import X`, "X.Y" for `import X.Y`. Reset in endStatement.
	fromModulePath string
	// pyPendingImports collects (declIdx, origName) for each binding the
	// current import statement emits. Signature is stamped in endStatement
	// once the path is fully assembled.
	pyPendingImports []pendingPyImport
	// importAsPending is true between the `as` keyword and the alias
	// ident inside an import statement — the next KindImport decl emitted
	// is the alias binding (it retargets the most recent pending entry).
	importAsPending bool

	// assignLHS: at statement start, collect idents that might be the LHS
	// of an assignment. Flushed as decls on '=' (but not '=='), or as
	// refs otherwise.
	assignLHS        []pendingParam
	inAssignLHS      bool
	assignLineIndent int

	// lhsGroupDepth counts open '(' that were opened as part of a
	// tuple-destructuring LHS (e.g. '(a, b) = point'). Such parens are
	// transparent for LHS ident collection and are NOT pushed onto
	// bracketStack. Decremented on the matching ')'.
	lhsGroupDepth int

	// decoratorLine: when we see '@' at line start, the next ident is
	// the decorator name (a ref). Set to true temporarily.
	decoratorLine bool

	// forVarExpected: after `for` keyword at statement start, the next
	// idents (before `in`) are loop variables (decls in enclosing scope).
	forVarExpected bool

	// pendingFullStart (+1 offset; 0 means unset) captures the byte
	// position of the most recent def/class keyword for emitDecl to use
	// as FullSpan.StartByte.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last scope-
	// owning decl (def/class). Consumed by openScope; closeTopScope
	// patches FullSpan.EndByte. -1 when none.
	pendingOwnerDecl int

	// bracketStack tracks open '[', '{', '(' that are not part of a def
	// signature param list. Each entry records whether the bracket opens
	// a comprehension scope and, if so, the scope-stack depth to pop to
	// on the matching close.
	bracketStack []bracketFrame

	prevByte byte
}

// bracketFrame records a single unclosed bracket. comprehension is true
// when the look-ahead at open time detected a top-level `for` keyword
// inside the brackets; scopeDepthAtOpen is the b.stack.Depth() before
// the comprehension scope was pushed, so the matching close can restore
// it exactly.
type bracketFrame struct {
	open             byte
	comprehension    bool
	scopeDepthAtOpen int
}

type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind
}

// pendingPyImport records an import binding decl awaiting Signature
// back-fill at endStatement. declIdx indexes into b.res.Decls; origName
// is the source-module name ("Bar" in `from foo import Bar as B`) or
// "*" for `import X[.Y]` whole-module binds.
type pendingPyImport struct {
	declIdx  int
	origName string
}

func (b *builder) run() {
	for !b.s.EOF() {
		if b.atLineStart {
			b.measureIndent()
			b.atLineStart = false
		}
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.endStatement()
			b.s.Next()
			b.atLineStart = true
			b.prevByte = '\n'
		case c == '#':
			// Line comment
			b.s.SkipLineComment()
		case c == '"' || c == '\'':
			b.scanPyString()
			b.prevByte = c
		case c == '(':
			openPos := b.s.Pos
			prevPrev := b.prevByte
			b.s.Pos++
			b.prevByte = '('
			if b.paramListPending {
				b.paramListPending = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.inParamList {
				b.paramDepth++
			} else if b.lhsGroupDepth > 0 ||
				prevPrev == 0 || prevPrev == '\n' || prevPrev == ';' ||
				b.forVarExpected ||
				(prevPrev == ',' && (b.inAssignLHS || b.lhsGroupDepth > 0)) {
				// Tuple-LHS grouping paren. Transparent for ident collection;
				// do NOT push onto bracketStack (no comprehension check).
				b.lhsGroupDepth++
			} else {
				b.pushBracket('(', openPos)
			}
		case c == ')':
			b.s.Pos++
			b.prevByte = ')'
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
				}
			} else if b.lhsGroupDepth > 0 {
				b.lhsGroupDepth--
			} else {
				b.popBracket('(', uint32(b.s.Pos))
			}
		case c == ':':
			// Could be: def body start, dict key separator, slice,
			// type annotation. If pendingScope is set, this ':' ends
			// the header line — the body is the next indented block.
			b.s.Pos++
			b.prevByte = ':'
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
			}
			if b.inAssignLHS {
				// Next ident may be another LHS target.
			}
			// `from X import A, B as C, D` — after a comma in
			// fromImportTarget mode, the next ident is again an import
			// binding. Re-arm declContext so the handleIdent fast path
			// emits it as a KindImport decl rather than a ref.
			if b.fromImportTarget {
				b.declContext = scope.KindImport
			}
		case c == '=' && b.s.PeekAt(1) != '=':
			b.s.Pos++
			b.prevByte = '='
			// Commit pending LHS idents as decls.
			if b.inAssignLHS && len(b.assignLHS) > 0 {
				for _, p := range b.assignLHS {
					b.emitDecl(p.name, scope.KindVar, p.span)
				}
				b.assignLHS = nil
				b.inAssignLHS = false
			}
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
			// Any pending LHS that included this name should be downgraded
			// to refs — dotted assignments (obj.x = ...) are not local decls.
			if b.inAssignLHS {
				for _, p := range b.assignLHS {
					b.emitRef(p.name, p.span)
				}
				b.assignLHS = nil
				b.inAssignLHS = false
			}
			// Import path accumulation: every '.' extends the path during
			// `from <path> import ...` or `import <path>[ as alias]`.
			// Leading dots for relative imports (`from . import X`,
			// `from ..foo import X`) are meaningful markers.
			if b.inImport || (b.inFromImport && !b.fromImportTarget) {
				b.fromModulePath += "."
			}
		case c == '@':
			b.s.Pos++
			b.prevByte = '@'
			if b.currentIndent == getScopeIndent(&b.stack)+getBodyIndent(&b.stack) || b.atLineStart {
				// Decorator.
			}
			b.decoratorLine = true
		case c == '[':
			openPos := b.s.Pos
			b.s.Pos++
			b.prevByte = '['
			b.pushBracket('[', openPos)
		case c == ']':
			b.s.Pos++
			b.prevByte = ']'
			b.popBracket('[', uint32(b.s.Pos))
		case c == '{':
			openPos := b.s.Pos
			b.s.Pos++
			b.prevByte = '{'
			b.pushBracket('{', openPos)
		case c == '}':
			b.s.Pos++
			b.prevByte = '}'
			b.popBracket('{', uint32(b.s.Pos))
		case c == ';':
			b.s.Pos++
			b.endStatement()
			b.prevByte = ';'
		case lexkit.DefaultIdentStart[c]:
			// Check for string prefixes like f"...", b'...', r"...", etc.
			if isPyStringPrefix(&b.s) {
				// Skip prefix letters, then scan string.
				for !b.s.EOF() {
					cc := b.s.Peek()
					if cc == '"' || cc == '\'' {
						b.scanPyString()
						b.prevByte = cc
						break
					}
					b.s.Pos++
				}
				continue
			}
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !(lexkit.IsASCIIDigit(cc) || cc == '.' || cc == 'e' || cc == 'E' || cc == '_' || cc == 'x' || cc == 'X' || cc == 'j' || cc == 'J') {
					break
				}
				b.s.Pos++
			}
			b.prevByte = '0'
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

// measureIndent counts the indent of the current line (in spaces, with
// tab = 8), pops any scopes whose startIndent >= currentIndent for
// non-blank content lines, and handles pending scope opens.
func (b *builder) measureIndent() {
	startPos := b.s.Pos
	indent := 0
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' {
			indent++
			b.s.Pos++
		} else if c == '\t' {
			indent += 8
			b.s.Pos++
		} else {
			break
		}
	}
	// Blank line or comment-only line: don't affect scope stack.
	if b.s.EOF() || b.s.Peek() == '\n' || b.s.Peek() == '#' {
		b.currentIndent = indent
		return
	}
	// Reset position to start of indent so later scanning still reads it
	// as whitespace (the main loop handles ' '/'\t').
	b.s.Pos = startPos
	b.currentIndent = indent

	// Pop scopes whose startIndent >= currentIndent. File scope has
	// startIndent = -1 so it never pops.
	for {
		top := b.stack.Top()
		if top == nil {
			break
		}
		if top.Data.startIndent < 0 {
			break
		}
		if top.Data.startIndent >= indent {
			b.closeTopScope(uint32(b.s.Pos))
			continue
		}
		break
	}

	// If there's a pendingScope from a prior def/class line and this is
	// the first body line (indent > startIndent), open the scope now and
	// flush any pending params.
	if b.pendingScope != nil && indent > b.pendingScopeAt {
		kind := *b.pendingScope
		b.pendingScope = nil
		b.openScope(kind, uint32(b.s.Pos), b.pendingScopeAt)
		if len(b.pendingParams) > 0 && kind == scope.ScopeFunction {
			for _, p := range b.pendingParams {
				b.emitDecl(p.name, scope.KindParam, p.span)
			}
			b.pendingParams = nil
		}
	}
}

// endStatement fires on '\n' and ';'. Flushes any pending LHS idents
// as REFS (they weren't followed by '=', so they're not assignments).
// Also stamps Signature on any import decls emitted by this statement.
func (b *builder) endStatement() {
	if b.inAssignLHS {
		for _, p := range b.assignLHS {
			b.emitRef(p.name, p.span)
		}
		b.assignLHS = nil
		b.inAssignLHS = false
	}
	// Stamp Signature on each pending import binding. Convention:
	// "<modulePath>\x00<origName>" — consumed by the cross-file import
	// resolver (internal/scope/store/imports_python.go).
	if len(b.pyPendingImports) > 0 && b.fromModulePath != "" {
		for _, pi := range b.pyPendingImports {
			if pi.declIdx < 0 || pi.declIdx >= len(b.res.Decls) {
				continue
			}
			b.res.Decls[pi.declIdx].Signature = b.fromModulePath + "\x00" + pi.origName
		}
	}
	b.pyPendingImports = nil
	b.fromModulePath = ""
	b.importAsPending = false
	b.declContext = ""
	b.decoratorLine = false
	b.forVarExpected = false
	b.inImport = false
	b.inFromImport = false
	b.fromImportTarget = false
	b.lhsGroupDepth = 0
}

func (b *builder) handleIdent(word []byte) {
	if len(word) == 0 {
		return
	}
	startByte := uint32(b.s.Pos - len(word))
	endByte := uint32(b.s.Pos)
	name := string(word)

	switch name {
	case "def":
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingScopeAt = b.currentIndent
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "class":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingScopeAt = b.currentIndent
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "lambda":
		// Lambda: params follow until `:`, then one-expression body.
		// Simplest v1: treat subsequent idents before `:` as pending
		// params in an (implicit) lambda function scope.
		// For now, just emit refs for the body — skip param tracking.
		b.prevByte = 'k'
		return
	case "import":
		fromBefore := b.inFromImport
		b.inImport = !b.inFromImport // if we already saw `from`, this is `from X import ...`
		b.fromImportTarget = b.inFromImport
		b.declContext = scope.KindImport
		b.prevByte = 'k'
		if !fromBefore {
			// Plain `import X[.Y][,...]` — path accumulation starts fresh.
			// (The `from-import` prefix is captured while processing the
			// module prefix idents.)
			b.fromModulePath = ""
		}
		return
	case "from":
		b.inFromImport = true
		b.fromModulePath = ""
		b.prevByte = 'k'
		return
	case "as":
		// `import X as Y` or `from X import Y as Z` or `with x() as y:` or
		// `except E as e:`. In all cases, the NEXT ident is a new decl
		// (kind depends on context). For imports, it's an import decl.
		// For others (with/except), v1 doesn't emit a decl.
		if b.inImport || b.fromImportTarget {
			b.declContext = scope.KindImport
			b.importAsPending = true
		}
		b.prevByte = 'k'
		return
	case "for":
		b.forVarExpected = true
		b.prevByte = 'k'
		return
	case "in":
		b.forVarExpected = false
		b.prevByte = 'k'
		return
	case "if", "elif", "else", "while", "try", "except", "finally",
		"with", "return", "yield", "raise", "pass", "break", "continue",
		"global", "nonlocal", "async", "await", "not", "and", "or", "is",
		"True", "False", "None":
		b.prevByte = 'k'
		return
	}

	// Property access after '.' — probable ref.
	if b.prevByte == '.' {
		// During an import statement, a dotted segment extends the
		// module path. The leading '.' was already appended by the
		// dot-case handler, so we only add the segment name.
		if b.inImport || (b.inFromImport && !b.fromImportTarget) {
			b.fromModulePath += name
		}
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Decorator line: first ident after '@' is a ref (the decorator).
	if b.decoratorLine {
		b.emitRef(name, mkSpan(startByte, endByte))
		b.decoratorLine = false
		b.prevByte = 'i'
		return
	}

	// In param list: first ident per section is a param name.
	if b.inParamList && b.paramDepth == 1 && b.paramSectionNeedsName {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindParam,
		})
		b.paramSectionNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Declaration context (def/class/import).
	if b.declContext != "" {
		kind := b.declContext
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		// After def/class, the next `(` starts a param list.
		if kind == scope.KindFunction {
			b.paramListPending = true
		}
		// Import-binding bookkeeping. Track which decl this emit
		// produced so `as <alias>` can retarget the pending entry, and
		// so endStatement can stamp Signature.
		if kind == scope.KindImport {
			declIdx := len(b.res.Decls) - 1
			switch {
			case b.importAsPending:
				// This emit is the alias (`... as <name>`). Keep origName
				// on the prior pending entry; rebind its declIdx.
				if n := len(b.pyPendingImports); n > 0 {
					b.pyPendingImports[n-1].declIdx = declIdx
				}
				b.importAsPending = false
			case b.fromImportTarget:
				// `from X import <name>[, ...]` — name is a binding.
				b.pyPendingImports = append(b.pyPendingImports, pendingPyImport{
					declIdx:  declIdx,
					origName: name,
				})
			default:
				// `import X[.Y][ as Z]` — origName is "*" (whole module).
				// The first ident IS the start of the module path;
				// subsequent `.Y` segments flow through the property-ref
				// branch and append there.
				b.fromModulePath = name
				b.pyPendingImports = append(b.pyPendingImports, pendingPyImport{
					declIdx:  declIdx,
					origName: "*",
				})
			}
		}
		b.prevByte = 'i'
		return
	}

	// `from X import Y, Z as A`: after `from X`, we collect Y, Z, A as
	// import decls. Skip the module name after `from` (it's at
	// fromImportTarget=false); once we see `import`, flip on.
	if b.inFromImport && !b.fromImportTarget {
		// This is part of the `from X.Y` path — emit as ref, not decl.
		// The first module segment is accumulated here; subsequent dotted
		// segments go through the property-ref branch above.
		b.fromModulePath += name
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// for-loop variable: emit as a decl in the enclosing function scope.
	if b.forVarExpected {
		b.emitDecl(name, scope.KindVar, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Potential LHS of assignment: collect bare idents that MIGHT be on
	// the left of `=`. Trigger: we're at statement start (prevByte is 0,
	// '\n', ';', or an earlier LHS comma); not in a paren/bracket; not
	// after a `.` (already handled above).
	if b.atStatementStartLike() {
		b.assignLHS = append(b.assignLHS, pendingParam{name: name, span: mkSpan(startByte, endByte)})
		b.inAssignLHS = true
		b.prevByte = 'i'
		return
	}

	// Otherwise: reference.
	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

// atStatementStartLike returns true when we're at a position where a
// bare ident could be the LHS of an assignment: statement boundary, or
// after a comma that extends a previous LHS chain.
func (b *builder) atStatementStartLike() bool {
	if b.inParamList {
		return false
	}
	switch b.prevByte {
	case 0, '\n', ';':
		return true
	case ',':
		return b.inAssignLHS || b.lhsGroupDepth > 0 || b.forVarExpected
	case '(':
		// Paren opened as part of a tuple-LHS grouping at statement start.
		return b.lhsGroupDepth > 0
	case '*':
		// Starred target in a tuple LHS: 'a, *rest = xs' or 'for a, *rest in xs'.
		return b.inAssignLHS || b.lhsGroupDepth > 0 || b.forVarExpected
	}
	return false
}

func (b *builder) scanPyString() {
	c := b.s.Peek()
	// Triple-quoted?
	if b.s.PeekAt(1) == c && b.s.PeekAt(2) == c {
		b.s.Advance(3)
		triple := string([]byte{c, c, c})
		b.s.SkipBlockComment(triple)
		return
	}
	b.s.ScanSimpleString(c)
}

func isPyStringPrefix(s *lexkit.Scanner) bool {
	// Prefix letters followed by ' or " indicate a string literal.
	// Valid prefixes: r, b, u, f and combinations (rb, fr, etc.), up to 2 chars.
	for i := 0; i < 3 && !s.EOF(); i++ {
		c := s.PeekAt(i)
		if c == '"' || c == '\'' {
			return i > 0
		}
		lc := c | 0x20
		if lc != 'r' && lc != 'b' && lc != 'u' && lc != 'f' {
			return false
		}
	}
	return false
}

func getScopeIndent(stack *lexkit.ScopeStack[scopeEntry]) int {
	t := stack.Top()
	if t == nil {
		return 0
	}
	return t.Data.startIndent
}

// getBodyIndent is a placeholder for future use; returns 0.
func getBodyIndent(stack *lexkit.ScopeStack[scopeEntry]) int {
	return 0
}

func (b *builder) openScope(kind scope.ScopeKind, startByte uint32, startIndent int) {
	b.openScopeBracket(kind, startByte, startIndent, false)
}

func (b *builder) openScopeBracket(kind scope.ScopeKind, startByte uint32, startIndent int, bracket bool) {
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
		Data:     scopeEntry{kind: kind, id: id, startIndent: startIndent, ownerDeclIdx: owner, bracket: bracket},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
}

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
}

func (b *builder) closeScopesToDepth(depth int) {
	endByte := uint32(len(b.s.Src))
	for b.stack.Depth() > depth {
		b.closeTopScope(endByte)
	}
}

// pushBracket handles '[', '{', '(' (when not a def-signature param list).
// If the brackets enclose a comprehension, a ScopeFunction is opened at
// the opening bracket; popBracket closes it on the matching close.
func (b *builder) pushBracket(open byte, openPos int) {
	frame := bracketFrame{
		open:             open,
		scopeDepthAtOpen: b.stack.Depth(),
	}
	if b.looksLikeComprehension(open, openPos) {
		frame.comprehension = true
		b.openScopeBracket(scope.ScopeFunction, uint32(openPos), -1, true)
	}
	b.bracketStack = append(b.bracketStack, frame)
}

// popBracket matches close to the top of bracketStack. If the open frame
// was a comprehension, any scopes pushed since (including the one opened
// at `[` / `{` / `(`) are closed.
func (b *builder) popBracket(open byte, endByte uint32) {
	n := len(b.bracketStack)
	if n == 0 {
		return
	}
	// Mismatched brackets (e.g., source with syntax error): unwind any
	// trailing frames that wouldn't match, then pop the match.
	for n > 0 {
		top := b.bracketStack[n-1]
		if top.open == open {
			if top.comprehension {
				// Close everything back down to the depth before we pushed
				// the comprehension scope.
				for b.stack.Depth() > top.scopeDepthAtOpen {
					b.closeTopScope(endByte)
				}
			}
			b.bracketStack = b.bracketStack[:n-1]
			return
		}
		// Drop the mismatched frame (best-effort on malformed input).
		if top.comprehension {
			for b.stack.Depth() > top.scopeDepthAtOpen {
				b.closeTopScope(endByte)
			}
		}
		b.bracketStack = b.bracketStack[:n-1]
		n--
	}
}

// looksLikeComprehension scans forward from openPos (which points at the
// opening bracket) and returns true if a top-level `for` keyword appears
// before the matching close bracket. It honors bracket nesting, string
// literals, and comments. The scan does not mutate the main scanner.
func (b *builder) looksLikeComprehension(open byte, openPos int) bool {
	close := matchingClose(open)
	src := b.s.Src
	i := openPos + 1
	depth := 1
	n := len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '#':
			// Skip to newline.
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'':
			i = skipPyStringAt(src, i)
		case c == '[' || c == '(' || c == '{':
			depth++
			i++
		case c == ']' || c == ')' || c == '}':
			depth--
			i++
			if depth == 0 {
				if c == close {
					return false
				}
				// Mismatched close; give up.
				return false
			}
		case isPyIdentStart(c):
			// Collect the identifier.
			start := i
			for i < n && isPyIdentCont(src[i]) {
				i++
			}
			word := src[start:i]
			// Only the outermost bracket depth decides comprehension-ness.
			if depth == 1 && len(word) == 3 && word[0] == 'f' && word[1] == 'o' && word[2] == 'r' {
				// Must not be touching a larger identifier (e.g. "fore").
				// isPyIdentCont stopped because next byte isn't ident-cont,
				// so this is a real keyword boundary.
				// Also must not be preceded by an ident-cont byte (e.g.
				// "xfor"). Check via start > openPos+1 and prev byte.
				if start == openPos+1 || !isPyIdentCont(src[start-1]) {
					return true
				}
			}
		default:
			i++
		}
	}
	return false
}

func matchingClose(open byte) byte {
	switch open {
	case '[':
		return ']'
	case '(':
		return ')'
	case '{':
		return '}'
	}
	return 0
}

// skipPyStringAt advances past a Python string literal (possibly
// triple-quoted) starting at position i (which points at the opening
// quote). Handles backslash escapes. Returns the position just past the
// closing quote, or len(src) if unterminated.
func skipPyStringAt(src []byte, i int) int {
	n := len(src)
	if i >= n {
		return n
	}
	q := src[i]
	// Triple-quoted?
	if i+2 < n && src[i+1] == q && src[i+2] == q {
		i += 3
		for i+2 < n {
			if src[i] == '\\' {
				i += 2
				continue
			}
			if src[i] == q && src[i+1] == q && src[i+2] == q {
				return i + 3
			}
			i++
		}
		return n
	}
	// Single-quoted.
	i++
	for i < n {
		c := src[i]
		if c == '\\' && i+1 < n {
			i += 2
			continue
		}
		if c == q {
			return i + 1
		}
		if c == '\n' {
			// Unterminated single-line string; stop here to avoid scanning
			// past the end of its statement.
			return i
		}
		i++
	}
	return n
}

func isPyIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 0x80
}

func isPyIdentCont(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c >= 0x80
}

func (b *builder) currentScope() scope.ScopeID {
	if top := b.stack.Top(); top != nil {
		return top.Data.id
	}
	return 0
}

func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	declID := hashDecl(b.file, name, scope.NSValue, scopeID)

	// FullSpan covers [def/class keyword → end of suite]. Scope-owning
	// decls get FullSpan.EndByte patched when the indent-based scope
	// closes. Leaf decls (var, param, import) keep FullSpan = Span.
	//
	// pendingFullStart uses a +1 offset so 0 is unambiguously unset.
	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}

	// Python convention: file-scope names NOT prefixed with '_' are
	// externally visible. v1 gap: we don't yet parse __all__, which
	// would otherwise override this heuristic.
	exported := false
	if len(name) > 0 && name[0] != '_' {
		if top := b.stack.Top(); top != nil && top.Data.kind == scope.ScopeFile {
			exported = true
		}
	}

	idx := len(b.res.Decls)
	b.res.Decls = append(b.res.Decls, scope.Decl{
		ID:        declID,
		LocID:     locID,
		Name:      name,
		Namespace: scope.NSValue,
		Kind:      kind,
		Scope:     scopeID,
		File:      b.file,
		Span:      span,
		FullSpan:  fullSpan,
		Exported:  exported,
	})

	switch kind {
	case scope.KindFunction, scope.KindClass:
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

// resolveRefs walks each ref's scope chain. Python's LEGB is like TS
// scope walking EXCEPT class scopes are transparent — a method body
// skips its enclosing class scope when looking up names.
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
		if r.Binding.Reason == "property_access" {
			continue
		}
		cur := r.Scope
		resolved := false
		// Walk upward. Skip class scopes (Python LEGB).
		for cur != 0 {
			if kindOf[cur] != scope.ScopeClass || cur == r.Scope {
				if d, ok := byKey[key{scope: cur, name: r.Name, ns: r.Namespace}]; ok {
					r.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   d.ID,
						Reason: "direct_scope",
					}
					resolved = true
					break
				}
			}
			p, ok := parent[cur]
			if !ok {
				break
			}
			cur = p
		}
		if !resolved {
			if builtins.Python.Has(r.Name) {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   hashBuiltinDecl(r.Name),
					Reason: "builtin",
				}
			} else {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindUnresolved,
					Reason: "missing_import",
				}
			}
		}
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

func hashBuiltinDecl(name string) scope.DeclID {
	h := sha256.New()
	h.Write([]byte("<builtin:py>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
