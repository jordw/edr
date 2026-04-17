// Package ts is the TypeScript/JavaScript scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single file.
// Handles file / function / block / class / namespace scopes and
// var/let/const/function/class/interface/type/import/param declarations.
// Identifiers not in declaration position are emitted as Refs and
// resolved via scope-chain walk to the innermost matching Decl.
//
// v1 limitations (to be relaxed):
//   - All decls are in NSValue; type-vs-value namespace split is TODO.
//     Generic type params are emitted as KindType decls but still in
//     NSValue — they bind correctly by name, just not namespace-checked.
//   - Destructuring declarations extract the first name only.
//   - No declaration merging (class+interface+namespace with same name
//     produces three separate Decls; merging is a later reconciliation pass).
//   - Generic type params: body refs bind to the emitted type-param decl,
//     but signature-position refs (in `(x: T)` or `: T =>`) don't — they
//     happen before the function scope opens. Fix would require either
//     scope-push at '<' or a two-phase resolver.
//   - TS return-type-annotated arrows like `(a): RetType => body` — the
//     ':' between `)` and `=>` defeats arrow-paren detection; params are
//     emitted as refs. Common enough to fix eventually.
//   - Property accesses (x.y) emit x as a Ref but not y.
//   - Cross-file import resolution: imported names are local Import decls
//     but their origin-file exports aren't followed.
//   - JSX not supported.
package ts

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a TypeScript/JavaScript source buffer.
// file is the canonical file path used to stamp Decl.File and Ref.File;
// pass the same path the caller will use when querying.
func Parse(file string, src []byte) *scope.Result {
	b := &builder{
		file: file,
		res:  &scope.Result{File: file},
		s:    lexkit.New(src),
	}
	b.openScope(scope.ScopeFile, 0)
	b.regexOK = true
	b.stmtStart = true
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	return b.res
}

// scopeEntry is the per-stack-frame data we carry. savedVarDeclKind
// holds the enclosing var-decl kind when this scope was pushed; restored
// on pop so e.g. commas in an object literal inside a `const` statement
// don't mis-activate declContext from varDeclKind.
type scopeEntry struct {
	kind             scope.ScopeKind
	id               scope.ScopeID
	savedVarDeclKind scope.DeclKind
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner



	stack lexkit.ScopeStack[scopeEntry]

	regexOK   bool
	stmtStart bool

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push. Set by keywords like "function" or "class".
	pendingScope *scope.ScopeKind

	// declContext, when non-empty, classifies the next identifier as a
	// declaration of this kind (rather than a reference).
	declContext scope.DeclKind
	// varDeclKind remembers the current var-statement kind (const/let/var)
	// so a comma can re-enter declContext for the next binder. Cleared at
	// statement end (;, }, or bare \n at top level).
	varDeclKind scope.DeclKind

	// prevByte tracks the last non-whitespace byte, used for property-
	// access detection ('x.y' — y is a property, not a scope ref).
	prevByte byte

	// paramListPending is set after a function-like keyword + name; the
	// next '(' begins a param list whose identifiers are param decls.
	paramListPending bool

	// inParamList is true while inside a function's (...) param list.
	// paramDepth tracks '(' balance within the list so nested parens
	// (default values, parenthesized types) don't confuse section boundaries.
	inParamList bool
	paramDepth  int

	// paramSectionNeedsName is true at the start of each comma-separated
	// param section; the first ident becomes a param decl, after which we
	// skip to the next ',' or closing ')'.
	paramSectionNeedsName bool

	// pendingParams collects param decls (value or type) during (...) or
	// <...> parsing; emitted into the function/class/interface scope when
	// the body '{' opens it.
	pendingParams []pendingParam

	// parenVarStack saves varDeclKind at each '(' or '['; restored on the
	// matching ')' or ']'. Parens/brackets don't push scopes, but they do
	// create containment for var-decl binding reach — e.g. `for (const x
	// of arr)` should not leak x's const-kind past the `)`, and function-
	// call commas `f(a, b)` inside a var decl should not re-activate
	// declContext.
	parenVarStack []scope.DeclKind

	// genericParamsExpected is set after a function/class/interface/type
	// decl name; the next '<' begins a generic type param list whose
	// identifiers become KindType decls.
	genericParamsExpected bool

	// inGenericParams + genericDepth + genericSectionNeedsName mirror the
	// param-list state machine, scoped to generic <...> rather than (...).
	inGenericParams         bool
	genericDepth            int
	genericSectionNeedsName bool

	// inDestructuring is true while inside a `const/let/var { ... } = ...`
	// or `[ ... ] = ...` pattern. Idents at any depth get emitted as decls
	// in the CURRENT scope (the destructuring braces/brackets don't push
	// one). destructureKind stores the var kind for emitted decls.
	inDestructuring   bool
	destructureDepth  int
	destructureKind   scope.DeclKind
}

type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind // KindParam or KindType (for generics)
}

func (b *builder) run() {
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.s.Next()
			b.stmtStart = true
			b.regexOK = true
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '/' && b.regexOK:
			b.s.ScanSlashRegex()
			b.regexOK = false
			b.prevByte = '/'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = '\''
		case c == '"':
			b.s.ScanSimpleString('"')
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = '"'
		case c == '`':
			b.s.ScanInterpolatedString('`', "${", skipTemplateExpr)
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = '`'
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == ';':
			b.s.Pos++
			b.stmtStart = true
			b.regexOK = true
			b.declContext = ""
			b.varDeclKind = ""
			b.prevByte = ';'
			// Interface method signatures (no body) reach ';' with pending
			// params; drop them — no scope to bind against. Same for
			// stranded generics (e.g., type-alias generics we can't attach).
			b.pendingParams = nil
			b.paramListPending = false
			b.genericParamsExpected = false
			// Safety: destructuring state should be closed by } or ],
			// but clear at ; to avoid leaking on malformed input.
			b.inDestructuring = false
			b.destructureDepth = 0
		case c == '=' && b.s.PeekAt(1) == '>':
			b.s.Advance(2)
			save := b.s.Pos
			saveLine := b.s.Line
			b.skipWS()
			if !b.s.EOF() && b.s.Peek() == '{' {
				k := scope.ScopeFunction
				b.pendingScope = &k
			} else {
				b.s.Pos = save
				b.s.Line = saveLine
				// Expression-body arrow; no function scope will open.
				// Drop any pending params to avoid leaking them into a
				// later function. Refs in the expression body resolve to
				// the enclosing scope, not the arrow's params (v1 gap).
				b.pendingParams = nil
			}
			b.regexOK = true
			b.prevByte = '>'
		case c == '(':
			b.s.Pos++
			b.regexOK = true
			b.prevByte = '('
			b.parenVarStack = append(b.parenVarStack, b.varDeclKind)
			b.varDeclKind = ""
			if b.paramListPending {
				b.paramListPending = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.inParamList {
				b.paramDepth++
			} else if b.isArrowParamList() {
				// Arrow function: the preceding '(' starts a param list
				// whose idents will become KindParam decls when the body
				// scope opens (either the '{' after '=>', or an expression
				// body — for v1 we only attach to '=>{' block bodies).
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			}
		case c == ')':
			b.s.Pos++
			b.regexOK = false
			b.prevByte = ')'
			if n := len(b.parenVarStack); n > 0 {
				b.varDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
				}
			}
		case c == ',':
			b.s.Pos++
			b.regexOK = true
			b.prevByte = ','
			// Re-enter declContext from varDeclKind only when a comma genuinely
			// separates binders in a var statement. Inside a param list (arrow
			// function) or destructuring pattern, those paths handle their own
			// idents; polluting declContext here would leak into the body scope
			// (e.g. `const f = (a, b) => { ... }` would treat b as a const decl
			// and the body '{' as destructuring).
			if b.varDeclKind != "" && !b.inParamList && !b.inDestructuring && !b.inGenericParams {
				b.declContext = b.varDeclKind
			} else {
				b.declContext = ""
			}
			// In a param list at top depth, a comma starts the next section.
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
			}
			// Same for generic type-param lists.
			if b.inGenericParams && b.genericDepth == 1 {
				b.genericSectionNeedsName = true
			}
		case lexkit.IsDefaultIdentStart(c) || c == '$':
			word := b.s.ScanIdentTable(&identStart, &identCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' && cc != 'x' && cc != 'e' && cc != 'n' {
					break
				}
				b.s.Pos++
			}
			b.regexOK = false
			b.stmtStart = false
			b.prevByte = '0'
		case c == '.':
			// Rest / spread operator: "..." is lexically three dots; treat
			// it as a separator so the following ident is handled normally
			// (either as a rest param in a param list, or as a spread-ref).
			if b.s.PeekAt(1) == '.' && b.s.PeekAt(2) == '.' {
				b.s.Advance(3)
				b.regexOK = true
				b.prevByte = ' '
				continue
			}
			b.s.Pos++
			b.regexOK = false
			b.prevByte = '.'
		case c == '<':
			// Generic type-parameter list after a function/class/interface/
			// type/method decl name. Otherwise a less-than operator or a
			// type-position angle bracket we don't decode here.
			if b.genericParamsExpected {
				b.genericParamsExpected = false
				b.inGenericParams = true
				b.genericDepth = 1
				b.genericSectionNeedsName = true
				b.s.Pos++
				b.regexOK = true
				b.prevByte = '<'
				continue
			}
			if b.inGenericParams {
				b.genericDepth++
			}
			b.s.Pos++
			b.regexOK = true
			b.prevByte = '<'
		case c == '>':
			if b.inGenericParams {
				b.genericDepth--
				if b.genericDepth == 0 {
					b.inGenericParams = false
					b.genericSectionNeedsName = false
				}
				b.s.Pos++
				b.regexOK = true
				b.prevByte = '>'
				continue
			}
			b.s.Pos++
			b.regexOK = true
			b.prevByte = '>'
		case c == '[':
			b.s.Pos++
			b.regexOK = true
			b.prevByte = '['
			b.parenVarStack = append(b.parenVarStack, b.varDeclKind)
			b.varDeclKind = ""
			if b.inParamList {
				b.paramDepth++
			}
			if b.inDestructuring {
				b.destructureDepth++
			} else if b.declContext == scope.KindConst || b.declContext == scope.KindLet ||
				b.declContext == scope.KindVar {
				// Array destructuring: `const [a, b] = arr`.
				b.inDestructuring = true
				b.destructureDepth = 1
				b.destructureKind = b.declContext
			}
		case c == ']':
			b.s.Pos++
			b.regexOK = false
			b.prevByte = ']'
			if n := len(b.parenVarStack); n > 0 {
				b.varDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
				}
			}
			if b.inDestructuring {
				b.destructureDepth--
				if b.destructureDepth == 0 {
					b.inDestructuring = false
					b.declContext = ""
					b.varDeclKind = ""
				}
			}
		default:
			b.s.Pos++
			b.regexOK = isRegexOKAfter(c)
			b.prevByte = c
		}
	}
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

	// Keywords that change parser state.
	switch name {
	case "function":
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "class":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "interface":
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "type":
		if wasStmtStart || b.prevByte == ';' || b.prevByte == '{' || b.prevByte == '}' {
			b.declContext = scope.KindType
		}
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeBlock
		b.pendingScope = &k
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "namespace", "module":
		b.declContext = scope.KindNamespace
		k := scope.ScopeNamespace
		b.pendingScope = &k
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "const":
		b.declContext = scope.KindConst
		b.varDeclKind = scope.KindConst
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "let":
		b.declContext = scope.KindLet
		b.varDeclKind = scope.KindLet
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "var":
		b.declContext = scope.KindVar
		b.varDeclKind = scope.KindVar
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "import":
		b.handleImport()
		return
	case "export", "default", "declare":
		// Module-level decl prefixes: they don't themselves alter the
		// stmt-start state, so a following decl keyword (type, class,
		// function, etc.) still sees a statement boundary and activates.
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "async", "await", "return", "if", "else",
		"for", "while", "do", "switch", "case", "break", "continue",
		"throw", "try", "catch", "finally", "new", "typeof", "instanceof",
		"in", "of", "void", "delete", "yield", "public", "private",
		"protected", "readonly", "static", "abstract", "override", "as",
		"extends", "implements", "from", "true", "false", "null",
		"undefined", "this", "super", "keyof", "infer", "is",
		"satisfies", "asserts":
		switch name {
		case "return", "throw", "case", "in", "of", "typeof", "instanceof",
			"new", "void", "delete", "yield", "await", "extends",
			"implements", "as", "from":
			b.regexOK = true
		}
		b.prevByte = 'k'
		return
	}

	if b.prevByte == '.' {
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	// Destructuring: emit idents as decls in the current scope, with
	// one wrinkle: in `{ key: local }` renaming syntax, only `local` is
	// the local binding. Detect via one-token lookahead: if this ident
	// is followed by ':' at depth 1+, it's a key — skip and let the
	// next ident emit instead.
	if b.inDestructuring {
		if b.peekNonWSByte() == ':' {
			// Key in `{ key: local }`. Skip emission.
			b.regexOK = false
			b.prevByte = 'i'
			return
		}
		b.emitDecl(name, b.destructureKind, mkSpan(startByte, endByte))
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	// Generic type-param list: first ident per section becomes a pending
	// type decl. Non-first idents (e.g., in `T extends Something`) are
	// handled normally — `extends` is in the keyword list, so the type it
	// refers to falls through as a ref.
	if b.inGenericParams && b.genericDepth == 1 && b.genericSectionNeedsName {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindType,
		})
		b.genericSectionNeedsName = false
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	// Inside a param list, the first ident of each comma-separated section
	// (at top depth, outside destructuring) is a param name to stash for
	// emission when the function scope opens. Skip TS modifiers; treat
	// non-first idents in a section (types, default-value refs) as refs.
	if b.inParamList && b.paramDepth == 1 && b.paramSectionNeedsName {
		switch name {
		case "public", "private", "protected", "readonly", "override":
			// Modifier — next ident is still the param name.
			b.prevByte = 'k'
			return
		}
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindParam,
		})
		b.paramSectionNeedsName = false
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	if b.declContext != "" {
		kind := b.declContext
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = "" // always clear; comma re-enables from varDeclKind
		switch kind {
		case scope.KindFunction:
			b.paramListPending = true
			b.genericParamsExpected = true
		case scope.KindClass, scope.KindInterface, scope.KindType:
			b.genericParamsExpected = true
		}
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	// Inside a class or interface body, an ident might be a method
	// (followed by '(' or '<') or a field (followed by ':', '?', ';',
	// '=', ',', or '}'). Methods open a function scope on their body '{';
	// fields just add to the enclosing class/interface scope.
	scopeK := b.currentScopeKind()
	if scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface {
		nextCh := b.peekNonWSByte()
		switch nextCh {
		case '(', '<':
			kind := scope.KindMethod
			b.emitDecl(name, kind, mkSpan(startByte, endByte))
			b.paramListPending = true
			b.genericParamsExpected = true
			if scopeK == scope.ScopeClass {
				fs := scope.ScopeFunction
				b.pendingScope = &fs
			}
			b.regexOK = false
			b.prevByte = 'i'
			return
		case ':', '?', ';', ',', '=', '}':
			// Field declaration. Emits into the class/interface scope.
			// Note: `=` also matches class field initializers like
			// `x = 1` and array element assignments inside destructuring,
			// but destructuring is handled upstream via inParamList.
			b.emitDecl(name, scope.KindField, mkSpan(startByte, endByte))
			b.regexOK = false
			b.prevByte = 'i'
			return
		}
	}

	b.emitRef(name, mkSpan(startByte, endByte))
	b.regexOK = false
	b.prevByte = 'i'
}

// handleImport parses import forms:
//   import X from '...'
//   import { a, b as c } from '...'
//   import * as ns from '...'
//   import '...'             (side-effect only)
// Consumes up to the 'from' clause; leaves the rest of the line to the
// main loop.
func (b *builder) handleImport() {
	b.skipWS()
	// Optional "type" modifier at statement level.
	save := b.s.Pos
	if b.peekIdentWord("type") {
		b.s.Advance(4)
		b.skipWS()
	} else {
		b.s.Pos = save
	}
	if b.s.Peek() == '\'' || b.s.Peek() == '"' {
		return // side-effect import, no decls
	}
	// Default import: `import foo from '...'`
	if lexkit.IsDefaultIdentStart(b.s.Peek()) {
		word := b.s.ScanIdentTable(&identStart, &identCont)
		if len(word) > 0 {
			start := uint32(b.s.Pos - len(word))
			end := uint32(b.s.Pos)
			b.emitDecl(string(word), scope.KindImport, mkSpan(start, end))
		}
		b.skipWS()
		if b.s.Peek() == ',' {
			b.s.Pos++
			b.skipWS()
		}
	}
	// Namespace import: * as name
	if b.s.Peek() == '*' {
		b.s.Pos++
		b.skipWS()
		if b.peekIdentWord("as") {
			b.s.Advance(2)
			b.skipWS()
			word := b.s.ScanIdentTable(&identStart, &identCont)
			if len(word) > 0 {
				start := uint32(b.s.Pos - len(word))
				end := uint32(b.s.Pos)
				b.emitDecl(string(word), scope.KindImport, mkSpan(start, end))
			}
		}
	}
	// Named imports: { a, b as c, type d }
	if b.s.Peek() == '{' {
		b.s.Pos++
		for !b.s.EOF() {
			b.skipWS()
			if b.s.Peek() == '}' {
				b.s.Pos++
				break
			}
			if b.peekIdentWord("type") {
				b.s.Advance(4)
				b.skipWS()
			}
			word := b.s.ScanIdentTable(&identStart, &identCont)
			if len(word) == 0 {
				b.s.Pos++ // avoid infinite loop on malformed input
				continue
			}
			importedStart := uint32(b.s.Pos - len(word))
			importedEnd := uint32(b.s.Pos)
			imported := string(word)
			b.skipWS()
			if b.peekIdentWord("as") {
				b.s.Advance(2)
				b.skipWS()
				alias := b.s.ScanIdentTable(&identStart, &identCont)
				if len(alias) > 0 {
					start := uint32(b.s.Pos - len(alias))
					end := uint32(b.s.Pos)
					b.emitDecl(string(alias), scope.KindImport, mkSpan(start, end))
				}
			} else {
				b.emitDecl(imported, scope.KindImport, mkSpan(importedStart, importedEnd))
			}
			b.skipWS()
			if b.s.Peek() == ',' {
				b.s.Pos++
			}
		}
	}
}

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	b.regexOK = true
	b.stmtStart = true
	b.prevByte = '{'
	// Inside a param list, '{' is destructuring or an object-type;
	// do NOT push a scope. Track depth so the matching '}' stays inside.
	if b.inParamList {
		b.paramDepth++
		return
	}
	// Var-decl destructuring: `const { a, b } = obj`. Do NOT push a scope;
	// idents inside go into the current scope as var-kind decls.
	if b.declContext == scope.KindConst || b.declContext == scope.KindLet ||
		b.declContext == scope.KindVar {
		b.inDestructuring = true
		b.destructureDepth = 1
		b.destructureKind = b.declContext
		return
	}
	if b.inDestructuring {
		b.destructureDepth++
		return
	}
	kind := scope.ScopeBlock
	if b.pendingScope != nil {
		kind = *b.pendingScope
		b.pendingScope = nil
	}
	b.openScope(kind, uint32(b.s.Pos-1))
	// Flush pending params into the newly-opened scope. Value params
	// flush into function scopes; type params (generics) flush into
	// function/class/interface scopes — whichever opened here.
	if len(b.pendingParams) > 0 &&
		(kind == scope.ScopeFunction || kind == scope.ScopeClass || kind == scope.ScopeInterface) {
		for _, p := range b.pendingParams {
			pk := p.kind
			if pk == "" {
				pk = scope.KindParam
			}
			b.emitDecl(p.name, pk, p.span)
		}
		b.pendingParams = nil
	}
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.regexOK = false
	b.prevByte = '}'
	// Mirror the '{' handling: inside a param list, just track depth.
	if b.inParamList {
		b.paramDepth--
		if b.paramDepth == 0 {
			b.inParamList = false
			b.paramSectionNeedsName = false
		}
		return
	}
	if b.inDestructuring {
		b.destructureDepth--
		if b.destructureDepth == 0 {
			b.inDestructuring = false
			b.declContext = ""
			b.varDeclKind = "" // destructuring closed; expect `= rhs ;`
		}
		return
	}
	b.closeTopScope(uint32(b.s.Pos))
}

func (b *builder) openScope(kind scope.ScopeKind, startByte uint32) {
	id := scope.ScopeID(len(b.res.Scopes) + 1) // 1-based; 0 is the absent parent
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
	b.stack.Push(lexkit.Scope[scopeEntry]{
		Data: scopeEntry{
			kind:             kind,
			id:               id,
			savedVarDeclKind: b.varDeclKind,
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
	// Fresh scope: don't let an outer var-decl kind bleed across into
	// commas inside object literals, function bodies, etc. Restored on pop.
	b.varDeclKind = ""
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
	b.varDeclKind = e.Data.savedVarDeclKind
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

// currentScopeKind returns the kind of the innermost open scope, or "".
func (b *builder) currentScopeKind() scope.ScopeKind {
	if top := b.stack.Top(); top != nil {
		return top.Data.kind
	}
	return ""
}

// isArrowParamList reports whether the '(' we just consumed starts an
// arrow-function parameter list (i.e., the matching ')' is followed by
// '=>', possibly with intervening whitespace/comments). Handles nested
// parens, brackets, braces, strings, comments, and template literals.
// Does not mutate scanner position. v1 limitation: doesn't recognize TS
// return-type-annotated arrows like '(a): RetType =>' — those look like
// arrow syntax but the ':' defeats the immediate '=>' check.
func (b *builder) isArrowParamList() bool {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() {
		b.s.Pos = save
		b.s.Line = saveLine
	}()
	// We've just consumed '('; track paren depth from 1.
	parenDepth := 1
	for !b.s.EOF() && parenDepth > 0 {
		c := b.s.Peek()
		switch {
		case c == '(':
			parenDepth++
			b.s.Pos++
		case c == ')':
			parenDepth--
			b.s.Pos++
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '\'':
			b.s.ScanSimpleString('\'')
		case c == '"':
			b.s.ScanSimpleString('"')
		case c == '`':
			b.s.ScanInterpolatedString('`', "${", skipTemplateExpr)
		default:
			b.s.Next()
		}
	}
	if parenDepth != 0 {
		return false
	}
	// Skip whitespace/comments; check for '=>'. If we hit ':' instead,
	// we're looking at a TS-typed arrow `(a): RetType => body` — scan
	// through the return-type annotation (balanced across <>[](){}) and
	// try again for '=>'.
	skipWS := func() {
		for !b.s.EOF() {
			c := b.s.Peek()
			if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				b.s.Next()
				continue
			}
			if c == '/' && b.s.PeekAt(1) == '/' {
				b.s.SkipLineComment()
				continue
			}
			if c == '/' && b.s.PeekAt(1) == '*' {
				b.s.Advance(2)
				b.s.SkipBlockComment("*/")
				continue
			}
			return
		}
	}
	skipWS()
	if b.s.EOF() {
		return false
	}
	if b.s.Peek() == ':' {
		// TS return-type annotation. Skip through the type expression
		// until we hit '=>' at depth 0 (or a stop token that means
		// this wasn't an arrow after all).
		b.s.Pos++ // consume ':'
		typeDepth := 0
		for !b.s.EOF() {
			c := b.s.Peek()
			if typeDepth == 0 && c == '=' && b.s.PeekAt(1) == '>' {
				return true
			}
			switch c {
			case '<', '[', '(', '{':
				typeDepth++
				b.s.Pos++
			case '>', ']', ')', '}':
				if typeDepth == 0 {
					return false
				}
				typeDepth--
				b.s.Pos++
			case ';', ',':
				if typeDepth == 0 {
					return false
				}
				b.s.Pos++
			case '/':
				if b.s.PeekAt(1) == '/' {
					b.s.SkipLineComment()
				} else if b.s.PeekAt(1) == '*' {
					b.s.Advance(2)
					b.s.SkipBlockComment("*/")
				} else {
					b.s.Pos++
				}
			case '\'':
				b.s.ScanSimpleString('\'')
			case '"':
				b.s.ScanSimpleString('"')
			case '`':
				b.s.ScanInterpolatedString('`', "${", skipTemplateExpr)
			default:
				b.s.Next()
			}
		}
		return false
	}
	return b.s.Peek() == '=' && b.s.PeekAt(1) == '>'
}

// peekNonWSByte returns the next non-whitespace, non-comment byte without
// advancing. Used to look ahead one token's worth, e.g. to tell whether
// an identifier in a class scope is a method (followed by '(').
func (b *builder) peekNonWSByte() byte {
	save := b.s.Pos
	saveLine := b.s.Line
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			b.s.Next()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '/' {
			b.s.SkipLineComment()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '*' {
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
			continue
		}
		b.s.Pos = save
		b.s.Line = saveLine
		return c
	}
	b.s.Pos = save
	b.s.Line = saveLine
	return 0
}

func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	ns := scope.NSValue
	// Class/interface members (field, method) go in the field namespace so
	// they do not shadow same-name top-level decls during scope resolution.
	// Property-access refs (obj.x) are skipped at the tokenizer level, so
	// field/method names never come up as bare refs.
	if kind == scope.KindField || kind == scope.KindMethod {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			ns = scope.NSField
		}
	}
	declID := hashDecl(b.file, name, ns, scopeID)
	b.res.Decls = append(b.res.Decls, scope.Decl{
		ID:        declID,
		LocID:     locID,
		Name:      name,
		Namespace: ns,
		Kind:      kind,
		Scope:     scopeID,
		File:      b.file,
		Span:      span,
	})
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

// resolveRefs walks each Ref's scope chain and binds it to the innermost
// matching Decl, if any.
func (b *builder) resolveRefs() {
	parent := make(map[scope.ScopeID]scope.ScopeID, len(b.res.Scopes))
	for _, s := range b.res.Scopes {
		parent[s.ID] = s.Parent
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
		cur := r.Scope
		resolved := false
		for {
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
			if p == 0 && cur != 0 {
				// Reached file scope; try one more lookup there.
				if d, ok := byKey[key{scope: 0, name: r.Name, ns: r.Namespace}]; ok {
					r.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   d.ID,
						Reason: "direct_scope",
					}
					resolved = true
				}
				break
			}
			if cur == 0 {
				break
			}
			cur = p
		}
		if !resolved {
			// Signature-position generics: `function foo<T>(x: T)` — the
			// ref to T in the param list is emitted at file scope but the
			// T decl lives in the function scope (opened later at '{').
			// Pass 2: for an unresolved ref, look for a KindType decl
			// whose source position precedes the ref AND whose enclosing
			// scope's end-byte follows the ref. Bind with a distinct
			// reason so consumers can tell it apart.
			for j := range b.res.Decls {
				d := &b.res.Decls[j]
				if d.Kind != scope.KindType || d.Name != r.Name || d.Namespace != r.Namespace {
					continue
				}
				if d.Span.EndByte >= r.Span.StartByte {
					continue // decl not yet in source at ref position
				}
				if int(d.Scope) <= 0 || int(d.Scope) > len(b.res.Scopes) {
					continue
				}
				sc := b.res.Scopes[int(d.Scope)-1]
				if sc.Span.EndByte == 0 || r.Span.EndByte > sc.Span.EndByte {
					continue // scope doesn't enclose ref (still open or past end)
				}
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   d.ID,
					Reason: "signature_scope",
				}
				resolved = true
				break
			}
		}
		if !resolved {
			// Fall back to language builtins: Array, Promise, Error, etc.
			if builtins.TypeScript.Has(r.Name) {
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

// hashBuiltinDecl yields a deterministic, file-independent DeclID for a
// language builtin, so every ref to the same builtin across every file
// groups under one ID. Namespaced under "<builtin:ts>" to avoid collision
// with any user decl.
func hashBuiltinDecl(name string) scope.DeclID {
	h := sha256.New()
	h.Write([]byte("<builtin:ts>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}

func (b *builder) skipWS() {
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			b.s.Next()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '/' {
			b.s.SkipLineComment()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '*' {
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
			continue
		}
		break
	}
}

// peekIdentWord reports whether the scanner is at a whole-word match for
// kw (terminated by a non-ident-cont byte or EOF). Does not advance.
func (b *builder) peekIdentWord(kw string) bool {
	if !b.s.StartsWith(kw) {
		return false
	}
	next := b.s.PeekAt(len(kw))
	if next == 0 {
		return true
	}
	return !identCont[next]
}

func mkSpan(start, end uint32) scope.Span {
	return scope.Span{StartByte: start, EndByte: end}
}

// isRegexOKAfter is a rough heuristic for the regex-vs-division question;
// parse_ts.go has a more refined version. For the v1 scope builder this
// is good enough — the common cases are already covered by the explicit
// keyword handling above.
func isRegexOKAfter(c byte) bool {
	switch c {
	case ')', ']', 'i', '0', '}':
		return false
	}
	return true
}

// skipTemplateExpr consumes a `${...}` template-literal expression body
// up to its matching `}`. Mirrors parse_ts.tsSkipTemplateExpr.
func skipTemplateExpr(s *lexkit.Scanner) {
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
			s.ScanInterpolatedString('`', "${", skipTemplateExpr)
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

var identStart [256]bool
var identCont [256]bool

func init() {
	for i := 0; i < 256; i++ {
		c := byte(i)
		identStart[i] = c == '_' || c == '$' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
		identCont[i] = identStart[i] || (c >= '0' && c <= '9')
	}
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
	// ScopeID is deterministic within a file (assigned in parse order),
	// so including it gives distinct DeclIDs for same-name decls in
	// different lexical scopes while staying stable across rebuilds.
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(scopeID))
	h.Write(buf[:])
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
