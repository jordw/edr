// Package kotlin is the Kotlin scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single Kotlin
// (.kt / .kts) source file. Handles file / class / interface / object /
// function / block scopes and
// class/interface/object/enum/method/function/property/field/param/
// local-var/import/generic-type-param declarations. Identifiers not in
// a declaration position are emitted as Refs and resolved via
// scope-chain walk to the innermost matching Decl, with fallbacks for
// signature-position generics, implicit-`this` field access, and
// language builtins.
//
// v1 deferred items (intentional simplifications):
//   - Extension-function receiver binding: `fun List<Int>.sum()` — the
//     receiver type emits as a ref; `sum` is a plain KindFunction at the
//     declaring scope. No receiver scope is pushed.
//   - Single-expression functions `fun f(x: Int) = x * x` — we emit the
//     function decl but do NOT push a scope; the params end up as refs
//     in the enclosing scope. Block-body functions are correct.
//   - `by` delegation: `val x by lazy { 42 }` — the delegate lambda's
//     contents are parsed, but we don't treat `lazy` specially.
//   - Destructuring `val (a, b) = pair` — emits only the first ident.
//   - Smart casts, inline classes, context resolution — parsed through.
//   - Type aliases `typealias Name = List<Int>` — emits Name as KindType.
//   - Method overloading: multiple decls share name; refs-to matches by
//     name. Signature-based disambiguation is a later pass.
//   - String templates `"hi $name"` — `$name` is consumed inside the
//     string body and not treated as a ref (v1 acceptable gap).
//   - Companion objects `companion object { ... }`: the `object` keyword
//     pushes a class scope; if no explicit name follows, no decl name is
//     emitted for the companion itself, but its members are still class
//     members in that scope.
//   - Annotations `@Foo`, `@JvmStatic`, `@file:Suppress(...)` — the `@`
//     flag emits the next ident as a ref (no special handling).
//   - Package declarations emit no decl.
package kotlin

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
)

// Parse extracts a scope.Result from a Kotlin source buffer. file is the
// canonical file path used to stamp Decl.File and Ref.File; pass the
// same path the caller will use when querying.
func Parse(file string, src []byte) *scope.Result {
	b := &builder{
		file:             file,
		res:              &scope.Result{File: file},
		s:                lexkit.New(src),
		pendingOwnerDecl: -1,
	}
	b.openScope(scope.ScopeFile, 0)
	b.stmtStart = true
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	return b.res
}

// scopeEntry is per-stack-frame data.
type scopeEntry struct {
	kind scope.ScopeKind
	id   scope.ScopeID
	// ownerDeclIdx: index in res.Decls of the decl that owns this scope;
	// closeTopScope patches FullSpan.EndByte. -1 if none.
	ownerDeclIdx int
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	// stmtStart is true at the top of a fresh statement.
	stmtStart bool

	// prevByte tracks the last non-whitespace, non-comment byte.
	prevByte byte

	// prevIdentIsThis / prevIdentIsSuper: the most recent identifier was
	// `this` or `super`, so a following `.X` can resolve against the
	// enclosing class's NSField decls.
	prevIdentIsThis  bool
	prevIdentIsSuper bool

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push.
	pendingScope *scope.ScopeKind

	// declContext classifies the next identifier as a declaration of this
	// kind. Set by class/interface/object/enum/fun/val/var/typealias.
	declContext scope.DeclKind

	// pendingFullStart: byte position+1 of the most recent decl keyword,
	// used as FullSpan.StartByte for scope-owning decls. 0 means unset.
	pendingFullStart uint32

	// pendingOwnerDecl: index in res.Decls of the last scope-owning decl.
	// Consumed by the next openScope.
	pendingOwnerDecl int

	// paramListPending: after a fun name, the next '(' begins a param
	// list whose idents become KindParam decls.
	paramListPending bool

	// ctorListPending: after a class name, the next '(' begins a primary
	// constructor whose `val`/`var` idents become KindField decls in the
	// class scope.
	ctorListPending bool

	// inParamList: inside (...) of a fun param list.
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool
	// paramSawColon: during a parameter section, the first ident is the
	// param name; after we encounter ':', subsequent idents are the
	// type (emitted as refs).
	paramSawColon bool

	// inCtorList: inside (...) of a class primary constructor. Behaves
	// like param list but `val`/`var` promote the next ident to a field.
	inCtorList       bool
	ctorDepth        int
	ctorSectionStart bool
	ctorValVar       bool // next ident in section is a promoted field
	ctorSawColon     bool

	// pendingParams collects param decls during (...) — emitted when
	// the fun body '{' opens its scope.
	pendingParams []pendingParam

	// genericParamsExpected: after a decl name, the next '<' begins a
	// generic type-param list.
	genericParamsExpected bool

	// inGenericParams + genericDepth + genericSectionNeedsName mirror
	// the param-list state machine for generic <...>.
	inGenericParams         bool
	genericDepth            int
	genericSectionNeedsName bool

	// pendingGenerics collects type-param decls from a class/fun generic
	// header. Flushed into the newly opened class/fun scope when its
	// body '{' opens.
	pendingGenerics []pendingParam

	// parenVarStack saves state at each '(' and '['; restored on ')' / ']'.
	parenVarStack []scope.DeclKind

	// localVarDeclKind remembers the current local-var kind so commas in
	// chained decls stay in context.
	localVarDeclKind scope.DeclKind

	// typePositionIdent: used inside function bodies to mark that the
	// previous ident was a type, so the next ident is the var name.
	typePositionIdent bool

	// isImportDecl / importBuf: consuming an `import foo.bar.Baz` — emit
	// final ident as a KindImport decl on the terminating newline / `;`.
	isImportDecl  bool
	importBuf     []byte
	importBufSpan scope.Span
	importIsStar  bool

	// isPackageDecl: consuming `package a.b.c` — emit nothing.
	isPackageDecl bool

	// annotationExpected: previous byte was '@', so the next ident is
	// part of an annotation and should be emitted as a ref (not a decl).
	annotationExpected bool

	// whenHeaderExpected: `when` was parsed; the following `{` opens a
	// block scope (not a lambda).
	whenHeaderExpected bool

	// forHeaderExpected: `for` was parsed; the next `(` begins a header
	// whose contents declare local vars (`x in coll` — `x` is a val).
	forHeaderExpected bool
	inForHeader       bool
	forHeaderDepth    int

	// lambdaBraceExpected: the last non-ws byte suggests the next '{'
	// opens a lambda (function-call argument, val initializer, etc.),
	// not a plain block.
	lambdaBraceExpected bool

	// blockBraceExpected: set by control-flow keywords (`if`, `else`,
	// `try`, `catch`, `finally`, `do`, `while`, `init`, `when`) that
	// introduce plain block bodies, so the next `{` does NOT open a
	// lambda scope even though prevByte might look like a trailing-lambda
	// context. Cleared once the brace is consumed.
	blockBraceExpected bool

	// extReceiverConsumed: we just emitted an extension-function receiver
	// as a ref; the next ident-after-'.' is the function name (not a
	// property access). Cleared when the fun name is emitted.
	extReceiverConsumed bool
}

type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind
}

func (b *builder) run() {
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.s.Next()
			if b.isImportDecl {
				b.flushImport()
			}
			b.isPackageDecl = false
			b.stmtStart = true
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			// Kotlin has simple and raw (triple-quoted) strings with
			// template interpolation. In v1 we don't emit refs for
			// `$name` templates — just consume the body. Triple quotes
			// scan as raw until matching triple.
			if b.s.PeekAt(1) == '"' && b.s.PeekAt(2) == '"' {
				b.s.Advance(3)
				for !b.s.EOF() {
					if b.s.Peek() == '"' && b.s.PeekAt(1) == '"' && b.s.PeekAt(2) == '"' {
						b.s.Advance(3)
						break
					}
					if b.s.Peek() == '\n' {
						b.s.Next()
						continue
					}
					b.s.Pos++
				}
			} else {
				b.s.ScanSimpleString('"')
			}
			b.stmtStart = false
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.stmtStart = false
			b.prevByte = '\''
		case c == '`':
			b.handleBacktickIdent()
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == ';':
			b.s.Pos++
			b.stmtStart = true
			b.prevByte = ';'
			b.declContext = ""
			b.localVarDeclKind = ""
			b.typePositionIdent = false
			b.paramListPending = false
			b.ctorListPending = false
			b.genericParamsExpected = false
			if b.isImportDecl {
				b.flushImport()
			}
			b.isPackageDecl = false
			b.pendingParams = nil
			b.pendingGenerics = nil
			b.forHeaderExpected = false
			b.whenHeaderExpected = false
			b.lambdaBraceExpected = false
			b.blockBraceExpected = false
		case c == '(':
			b.s.Pos++
			b.parenVarStack = append(b.parenVarStack, b.localVarDeclKind)
			b.prevByte = '('
			if b.ctorListPending {
				b.ctorListPending = false
				b.genericParamsExpected = false
				b.inCtorList = true
				b.ctorDepth = 1
				b.ctorSectionStart = true
				b.ctorValVar = false
				b.ctorSawColon = false
			} else if b.paramListPending {
				b.paramListPending = false
				b.genericParamsExpected = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
				b.paramSawColon = false
			} else if b.inParamList {
				b.paramDepth++
			} else if b.inCtorList {
				b.ctorDepth++
			} else if b.forHeaderExpected {
				b.forHeaderExpected = false
				b.inForHeader = true
				b.forHeaderDepth = 1
				b.stmtStart = true
			} else if b.inForHeader {
				b.forHeaderDepth++
			}
			b.lambdaBraceExpected = false
		case c == ')':
			b.s.Pos++
			b.prevByte = ')'
			if n := len(b.parenVarStack); n > 0 {
				b.localVarDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
					b.paramSawColon = false
				}
			}
			if b.inCtorList {
				b.ctorDepth--
				if b.ctorDepth == 0 {
					b.inCtorList = false
					b.ctorSectionStart = false
					b.ctorValVar = false
					b.ctorSawColon = false
					b.flushCtorFields()
				}
			}
			if b.inForHeader {
				b.forHeaderDepth--
				if b.forHeaderDepth == 0 {
					b.inForHeader = false
					b.typePositionIdent = false
					b.localVarDeclKind = ""
					b.blockBraceExpected = true
				}
			}
			if b.whenHeaderExpected {
				b.whenHeaderExpected = false
				b.blockBraceExpected = true
			}
		case c == '[':
			b.s.Pos++
			b.prevByte = '['
			b.parenVarStack = append(b.parenVarStack, b.localVarDeclKind)
		case c == ']':
			b.s.Pos++
			b.prevByte = ']'
			if n := len(b.parenVarStack); n > 0 {
				b.localVarDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
				b.paramSawColon = false
			}
			if b.inCtorList && b.ctorDepth == 1 {
				b.ctorSectionStart = true
				b.ctorValVar = false
				b.ctorSawColon = false
			}
			if b.inGenericParams && b.genericDepth == 1 {
				b.genericSectionNeedsName = true
			}
		case c == ':':
			b.s.Pos++
			b.prevByte = ':'
			if b.inParamList && b.paramDepth == 1 {
				b.paramSawColon = true
			}
			if b.inCtorList && b.ctorDepth == 1 {
				b.ctorSawColon = true
			}
		case c == '<':
			if b.genericParamsExpected {
				b.genericParamsExpected = false
				b.inGenericParams = true
				b.genericDepth = 1
				b.genericSectionNeedsName = true
				b.s.Pos++
				b.prevByte = '<'
				continue
			}
			if b.inGenericParams {
				b.genericDepth++
				b.s.Pos++
				b.prevByte = '<'
				continue
			}
			b.s.Pos++
			b.prevByte = '<'
		case c == '>':
			if b.inGenericParams {
				b.genericDepth--
				if b.genericDepth == 0 {
					b.inGenericParams = false
					b.genericSectionNeedsName = false
				}
				b.s.Pos++
				b.prevByte = '>'
				continue
			}
			b.s.Pos++
			b.prevByte = '>'
		case c == '-' && b.s.PeekAt(1) == '>':
			// `->` outside a lambda: Kotlin uses `->` in `when` branches
			// and function types `(Int) -> Int`. Lambda arrow is handled
			// inside handleOpenBrace.
			b.s.Advance(2)
			b.prevByte = '>'
		case c == '@':
			b.s.Pos++
			b.prevByte = '@'
			b.annotationExpected = true
		case c == '=':
			b.s.Pos++
			b.prevByte = '='
			b.paramListPending = false
			b.genericParamsExpected = false
			b.lambdaBraceExpected = true
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == '$':
			b.s.Pos++
			b.prevByte = '$'
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' &&
					cc != 'x' && cc != 'X' && cc != 'e' && cc != 'E' &&
					cc != 'L' && cc != 'l' && cc != 'F' && cc != 'f' &&
					cc != 'u' && cc != 'U' && cc != 'b' && cc != 'B' {
					break
				}
				b.s.Pos++
			}
			b.stmtStart = false
			b.prevByte = '0'
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

// handleBacktickIdent scans a `` `identifier` `` token.
func (b *builder) handleBacktickIdent() {
	if b.s.Peek() != '`' {
		return
	}
	start := b.s.Pos
	b.s.Pos++
	for !b.s.EOF() && b.s.Peek() != '`' && b.s.Peek() != '\n' {
		b.s.Pos++
	}
	if !b.s.EOF() && b.s.Peek() == '`' {
		b.s.Pos++
	}
	word := b.s.Src[start:b.s.Pos]
	b.handleIdent(word)
}

// handleIdent classifies a word: keyword (changes parser state), decl
// position, property access, or plain ref.
func (b *builder) handleIdent(word []byte) {
	if len(word) == 0 {
		return
	}
	startByte := uint32(b.s.Pos - len(word))
	endByte := uint32(b.s.Pos)
	name := string(word)
	wasStmtStart := b.stmtStart
	b.stmtStart = false

	// Package declaration: consume dotted name; emit nothing.
	if b.isPackageDecl {
		b.prevByte = 'i'
		return
	}

	// Import declaration: collect final unqualified name.
	if b.isImportDecl {
		b.importBuf = append(b.importBuf[:0], word...)
		b.importBufSpan = mkSpan(startByte, endByte)
		b.prevByte = 'i'
		return
	}

	// Annotation: emit as ref, don't activate decl paths.
	if b.annotationExpected {
		b.annotationExpected = false
		if b.prevIdentIsThis {
			// `this@Foo` — label; keep the this marker.
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Keywords that change parser state.
	switch name {
	case "package":
		b.isPackageDecl = true
		b.prevByte = 'k'
		return
	case "import":
		b.isImportDecl = true
		b.importBuf = b.importBuf[:0]
		b.importIsStar = false
		b.prevByte = 'k'
		return
	case "class":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "interface":
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "object":
		// `object Foo { ... }` singleton, OR `companion object { ... }`.
		// Either way, push a class-like scope.
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "enum":
		// `enum class Foo`. The next token `class` drives the scope push.
		b.prevByte = 'k'
		return
	case "companion":
		// `companion object ...` — fall through so `object` handles the
		// scope push.
		b.prevByte = 'k'
		return
	case "data", "sealed", "annotation", "inner", "inline",
		"public", "protected", "private", "internal", "open", "final",
		"abstract", "override", "lateinit", "vararg", "crossinline",
		"noinline", "reified", "tailrec", "suspend", "external", "operator",
		"infix", "expect", "actual":
		// Modifiers — preserve stmtStart so a following decl keyword still
		// activates.
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "fun":
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "val", "var":
		if b.inCtorList {
			b.ctorValVar = true
			b.prevByte = 'k'
			return
		}
		sk := b.currentScopeKind()
		if sk == scope.ScopeClass || sk == scope.ScopeInterface {
			b.declContext = scope.KindField
		} else {
			b.declContext = scope.KindVar
		}
		b.localVarDeclKind = b.declContext
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "typealias":
		b.declContext = scope.KindType
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "return", "if", "else", "while", "do", "break", "continue",
		"throw", "try", "catch", "finally", "is", "as", "in", "out",
		"where", "by", "constructor", "init", "throws", "super":
		b.prevByte = 'k'
		if name == "super" {
			b.prevIdentIsSuper = true
			b.prevIdentIsThis = false
		}
		switch name {
		case "if", "else", "try", "catch", "finally", "do", "while",
			"init":
			b.blockBraceExpected = true
		case "return", "throw":
			b.lambdaBraceExpected = true
		}
		return
	case "when":
		b.whenHeaderExpected = true
		b.blockBraceExpected = true
		b.prevByte = 'k'
		return
	case "for":
		b.forHeaderExpected = true
		b.prevByte = 'k'
		return
	case "this":
		b.prevIdentIsThis = true
		b.prevIdentIsSuper = false
		b.prevByte = 'k'
		return
	case "true", "false", "null":
		b.prevByte = 'k'
		return
	}

	// Property access after '.'.
	if b.prevByte == '.' {
		// Extension-fun receiver pattern: `fun Receiver.name(...)`. After
		// emitting Receiver as a ref, declContext is still KindFunction;
		// the ident after `.` is the fun name.
		if b.extReceiverConsumed && b.declContext == scope.KindFunction {
			b.extReceiverConsumed = false
			kind := scope.KindFunction
			sk := b.currentScopeKind()
			if sk == scope.ScopeClass || sk == scope.ScopeInterface {
				kind = scope.KindMethod
			}
			b.emitDecl(name, kind, mkSpan(startByte, endByte))
			b.declContext = ""
			b.genericParamsExpected = true
			b.paramListPending = true
			b.prevByte = 'i'
			return
		}
		if b.prevIdentIsThis || b.prevIdentIsSuper {
			b.prevIdentIsThis = false
			b.prevIdentIsSuper = false
			if b.tryResolveThisField(name, mkSpan(startByte, endByte)) {
				b.prevByte = 'i'
				return
			}
		}
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Clear this/super markers on any non-chained ident.
	b.prevIdentIsThis = false
	b.prevIdentIsSuper = false

	// Generic type-param list: first ident per section becomes a pending
	// type decl.
	if b.inGenericParams && b.genericDepth == 1 && b.genericSectionNeedsName {
		if name == "reified" || name == "in" || name == "out" {
			b.prevByte = 'k'
			return
		}
		b.pendingGenerics = append(b.pendingGenerics, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindType,
		})
		b.genericSectionNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Primary constructor param list.
	if b.inCtorList && b.ctorDepth == 1 {
		if b.ctorSectionStart {
			b.ctorSectionStart = false
			kind := scope.KindParam
			if b.ctorValVar {
				kind = scope.KindField
				b.ctorValVar = false
			}
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: name,
				span: mkSpan(startByte, endByte),
				kind: kind,
			})
			b.prevByte = 'i'
			return
		}
		if b.ctorSawColon {
			b.emitRef(name, mkSpan(startByte, endByte))
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Regular param list.
	if b.inParamList && b.paramDepth == 1 {
		if b.paramSectionNeedsName {
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: name,
				span: mkSpan(startByte, endByte),
				kind: scope.KindParam,
			})
			b.paramSectionNeedsName = false
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// declContext set: class/interface/object/fun/val/var/typealias.
	if b.declContext != "" {
		kind := b.declContext
		// For `fun Receiver.name`, the first ident is the receiver type
		// (a ref), and after a '.', the next ident is the function name.
		if kind == scope.KindFunction && b.peekNonWSByte() == '.' {
			b.emitRef(name, mkSpan(startByte, endByte))
			b.extReceiverConsumed = true
			b.prevByte = 'i'
			return
		}
		sk := b.currentScopeKind()
		if kind == scope.KindFunction && (sk == scope.ScopeClass || sk == scope.ScopeInterface) {
			kind = scope.KindMethod
		}
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		switch kind {
		case scope.KindClass, scope.KindInterface, scope.KindEnum:
			b.genericParamsExpected = true
			b.ctorListPending = true
		case scope.KindFunction, scope.KindMethod:
			b.genericParamsExpected = true
			b.paramListPending = true
		case scope.KindType:
			b.genericParamsExpected = true
		}
		b.prevByte = 'i'
		return
	}

	// Fallback: emit as a ref.
	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

// flushCtorFields handles the end of a primary constructor `)`. If a
// class body `{` follows, we leave pendingGenerics / pendingParams
// alone — handleOpenBrace will flush them after the class scope opens.
// If no body follows (`class Foo(val x: Int)` at top level), we open a
// synthetic class scope spanning the class header, emit generics +
// promoted fields into it, then close it, so members have the proper
// NSField namespace and class-scope parent.
func (b *builder) flushCtorFields() {
	if b.classBodyFollows() {
		return // handleOpenBrace will flush
	}
	if b.pendingScope != nil && *b.pendingScope == scope.ScopeClass {
		b.pendingScope = nil
		scopeStart := uint32(b.s.Pos - 1)
		if b.pendingFullStart > 0 {
			scopeStart = b.pendingFullStart - 1
		}
		b.openScope(scope.ScopeClass, scopeStart)
		if len(b.pendingGenerics) > 0 {
			for _, g := range b.pendingGenerics {
				pk := g.kind
				if pk == "" {
					pk = scope.KindType
				}
				b.emitDecl(g.name, pk, g.span)
			}
			b.pendingGenerics = nil
		}
		if len(b.pendingParams) > 0 {
			for _, p := range b.pendingParams {
				pk := p.kind
				if pk == "" {
					pk = scope.KindParam
				}
				b.emitDecl(p.name, pk, p.span)
			}
			b.pendingParams = nil
		}
		b.closeTopScope(uint32(b.s.Pos))
	}
}

// classBodyFollows peeks past optional supertype clauses and `where`
// clauses to see whether the next structural byte is `{`. Does not
// mutate scanner position.
func (b *builder) classBodyFollows() bool {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() {
		b.s.Pos = save
		b.s.Line = saveLine
	}()
	depth := 0
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			b.s.Next()
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			b.s.ScanSimpleString('"')
		case c == '\'':
			b.s.ScanSimpleString('\'')
		case c == '(', c == '[', c == '<':
			depth++
			b.s.Pos++
		case c == ')', c == ']', c == '>':
			if depth > 0 {
				depth--
			}
			b.s.Pos++
		case c == '{':
			return depth == 0
		case c == ';':
			return false
		case c == '}':
			return false
		default:
			if depth == 0 && lexkit.DefaultIdentStart[c] {
				word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				switch string(word) {
				case "class", "interface", "object", "fun", "val", "var",
					"typealias", "package", "import", "enum", "sealed",
					"data", "annotation", "abstract", "open", "final",
					"public", "private", "protected", "internal":
					return false
				}
				continue
			}
			b.s.Pos++
		}
	}
	return false
}

// flushImport emits the collected import ident as a KindImport decl.
// Skips wildcard imports (`import foo.bar.*`).
func (b *builder) flushImport() {
	if b.isImportDecl && len(b.importBuf) > 0 && !b.importIsStar {
		// Wildcard-import check: if the last non-ident byte before the
		// newline/`;` was `*`, skip emission. We don't directly track
		// that; the simplest check is whether the importBuf text ends
		// with an ident (it does, by construction).
		b.emitDecl(string(b.importBuf), scope.KindImport, b.importBufSpan)
	}
	b.isImportDecl = false
	b.importBuf = b.importBuf[:0]
	b.importIsStar = false
}

func (b *builder) handleOpenBrace() {
	// Capture prevByte BEFORE clobbering it; lambda-context check needs it.
	prevB := b.prevByte
	b.s.Pos++
	b.prevByte = '{'
	b.stmtStart = true

	kind := scope.ScopeBlock
	lambdaExpected := b.lambdaBraceExpected
	blockExpected := b.blockBraceExpected
	b.lambdaBraceExpected = false
	b.blockBraceExpected = false
	if b.pendingScope != nil {
		kind = *b.pendingScope
		b.pendingScope = nil
		b.genericParamsExpected = false
	} else if !blockExpected && (lambdaExpected || isLambdaPrev(prevB)) {
		// Lambda expression `{ a, b -> body }` or `{ it * 2 }`. Open a
		// function scope. Params (if any) are detected by scanning the
		// body for `->` and pulling the preceding idents as params.
		kind = scope.ScopeFunction
		params := b.scanLambdaParams()
		b.pendingParams = params
	}

	b.openScope(kind, uint32(b.s.Pos-1))

	// Flush generics into the newly opened scope.
	if kind == scope.ScopeClass || kind == scope.ScopeInterface ||
		kind == scope.ScopeFunction {
		if len(b.pendingGenerics) > 0 {
			for _, g := range b.pendingGenerics {
				pk := g.kind
				if pk == "" {
					pk = scope.KindType
				}
				b.emitDecl(g.name, pk, g.span)
			}
			b.pendingGenerics = nil
		}
	}
	// Flush params / primary-ctor promoted fields.
	if len(b.pendingParams) > 0 {
		if kind == scope.ScopeFunction || kind == scope.ScopeClass ||
			kind == scope.ScopeInterface {
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
}

// isLambdaPrev reports whether the given byte (the last non-ws byte
// before a `{`) suggests a lambda (function-call trailing lambda, var
// initializer, expression-position ident). Does NOT fire after
// control-flow keywords, which return prevByte='k' and are filtered
// via blockBraceExpected.
func isLambdaPrev(b byte) bool {
	switch b {
	case '(', ',', '=', '>':
		return true
	case ')':
		return true
	case 'i':
		return true
	}
	return false
}

// scanLambdaParams peeks ahead for a `->` inside the current lambda body
// and returns any preceding comma-separated idents as pending params.
// Does not mutate scanner position. If no `->` is found before the body
// closes, returns nil (implicit `it` lambda).
func (b *builder) scanLambdaParams() []pendingParam {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() {
		b.s.Pos = save
		b.s.Line = saveLine
	}()

	type candidate struct {
		name   string
		startB uint32
		endB   uint32
	}
	var cands []candidate
	depth := 1 // we're inside the `{`
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			b.s.Next()
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			if b.s.PeekAt(1) == '"' && b.s.PeekAt(2) == '"' {
				b.s.Advance(3)
				for !b.s.EOF() {
					if b.s.Peek() == '"' && b.s.PeekAt(1) == '"' && b.s.PeekAt(2) == '"' {
						b.s.Advance(3)
						break
					}
					b.s.Pos++
				}
			} else {
				b.s.ScanSimpleString('"')
			}
		case c == '\'':
			b.s.ScanSimpleString('\'')
		case c == '{':
			depth++
			b.s.Pos++
		case c == '}':
			depth--
			b.s.Pos++
		case c == '-' && b.s.PeekAt(1) == '>':
			if depth == 1 {
				out := make([]pendingParam, 0, len(cands))
				for _, c := range cands {
					out = append(out, pendingParam{
						name: c.name,
						span: mkSpan(c.startB, c.endB),
						kind: scope.KindParam,
					})
				}
				return out
			}
			b.s.Advance(2)
		case c == ',':
			b.s.Pos++
		case c == ':':
			// Type annotation after a param — skip idents until ',' or '->'.
			b.s.Pos++
			for !b.s.EOF() && depth > 0 {
				cc := b.s.Peek()
				if cc == ',' && depth == 1 {
					break
				}
				if cc == '-' && b.s.PeekAt(1) == '>' && depth == 1 {
					break
				}
				if cc == '{' {
					depth++
				} else if cc == '}' {
					depth--
					if depth == 0 {
						return nil
					}
				}
				b.s.Pos++
			}
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			if len(word) == 0 {
				b.s.Pos++
				continue
			}
			if depth == 1 {
				sword := string(word)
				if sword == "val" || sword == "var" || sword == "in" ||
					sword == "out" {
					continue
				}
				cands = append(cands, candidate{
					name:   sword,
					startB: uint32(b.s.Pos - len(word)),
					endB:   uint32(b.s.Pos),
				})
			}
		case c == '`':
			start := b.s.Pos
			b.s.Pos++
			for !b.s.EOF() && b.s.Peek() != '`' && b.s.Peek() != '\n' {
				b.s.Pos++
			}
			if !b.s.EOF() && b.s.Peek() == '`' {
				b.s.Pos++
			}
			if depth == 1 {
				cands = append(cands, candidate{
					name:   string(b.s.Src[start:b.s.Pos]),
					startB: uint32(start),
					endB:   uint32(b.s.Pos),
				})
			}
		default:
			b.s.Pos++
		}
	}
	return nil
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.prevByte = '}'
	b.closeTopScope(uint32(b.s.Pos))
	b.stmtStart = true
	b.typePositionIdent = false
	b.localVarDeclKind = ""
	b.declContext = ""
}

func (b *builder) openScope(kind scope.ScopeKind, startByte uint32) {
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
			ownerDeclIdx: owner,
		},
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
	if kind == scope.KindField || kind == scope.KindMethod {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			ns = scope.NSField
		}
	}
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
	case scope.KindClass, scope.KindInterface, scope.KindEnum,
		scope.KindMethod, scope.KindFunction:
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

// emitPropertyRef records a ref from a property-access position
// (`x.Name`). BindProbable with Reason="property_access" — name-only,
// no decl link.
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

// tryResolveThisField attempts to resolve `this.name` or `super.name`
// at `span` against the nearest enclosing class's NSField decls.
func (b *builder) tryResolveThisField(name string, span scope.Span) bool {
	entries := b.stack.Entries()
	var classScope scope.ScopeID
	for i := len(entries) - 1; i >= 0; i-- {
		k := entries[i].Data.kind
		if k == scope.ScopeClass || k == scope.ScopeInterface {
			classScope = entries[i].Data.id
			break
		}
	}
	if classScope == 0 {
		return false
	}
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		if d.Scope != classScope || d.Namespace != scope.NSField || d.Name != name {
			continue
		}
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
				Kind:   scope.BindResolved,
				Decl:   d.ID,
				Reason: "this_dot_field",
			},
		})
		return true
	}
	return false
}

// resolveRefs binds each Ref to a Decl via scope-chain walk, falling
// back to signature-position generics, implicit-this field, then
// unresolved.
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
	classField := make(map[key]*scope.Decl, len(b.res.Decls))
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		if d.Namespace == scope.NSField {
			k := key{scope: d.Scope, name: d.Name, ns: scope.NSField}
			if _, ok := classField[k]; !ok {
				classField[k] = d
			}
		}
	}
	scopeByID := make(map[scope.ScopeID]scope.Scope, len(b.res.Scopes))
	for _, s := range b.res.Scopes {
		scopeByID[s.ID] = s
	}
	nearestClass := make(map[scope.ScopeID]scope.ScopeID, len(b.res.Scopes))
	for _, s := range b.res.Scopes {
		cur := s.ID
		found := scope.ScopeID(0)
		for cur != 0 {
			sc, ok := scopeByID[cur]
			if !ok {
				break
			}
			if sc.Kind == scope.ScopeClass || sc.Kind == scope.ScopeInterface {
				found = cur
				break
			}
			cur = sc.Parent
		}
		nearestClass[s.ID] = found
	}

	for i := range b.res.Refs {
		r := &b.res.Refs[i]
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "this_dot_field" {
			continue
		}
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
			if cls := nearestClass[r.Scope]; cls != 0 {
				if d, ok := classField[key{scope: cls, name: r.Name, ns: scope.NSField}]; ok {
					r.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   d.ID,
						Reason: "implicit_this_field",
					}
					resolved = true
				}
			}
		}
		if !resolved {
			for j := range b.res.Decls {
				d := &b.res.Decls[j]
				if d.Kind != scope.KindType || d.Name != r.Name || d.Namespace != r.Namespace {
					continue
				}
				if d.Span.EndByte >= r.Span.StartByte {
					continue
				}
				if int(d.Scope) <= 0 || int(d.Scope) > len(b.res.Scopes) {
					continue
				}
				sc := b.res.Scopes[int(d.Scope)-1]
				if sc.Span.EndByte == 0 || r.Span.EndByte > sc.Span.EndByte {
					continue
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
			if isKotlinBuiltin(r.Name) {
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

// isKotlinBuiltin reports whether name is a Kotlin stdlib globally-
// visible type/value. Conservative v1 list; extend as dogfood surfaces
// real gaps.
func isKotlinBuiltin(name string) bool {
	switch name {
	case "Boolean", "Byte", "Short", "Int", "Long", "Float", "Double",
		"Char", "String", "Unit", "Nothing", "Any", "Number",
		"List", "MutableList", "Set", "MutableSet", "Map", "MutableMap",
		"Collection", "MutableCollection", "Iterable", "MutableIterable",
		"Iterator", "MutableIterator", "ListIterator", "MutableListIterator",
		"Array", "IntArray", "LongArray", "FloatArray", "DoubleArray",
		"BooleanArray", "CharArray", "ByteArray", "ShortArray",
		"Sequence", "Pair", "Triple",
		"listOf", "mutableListOf", "setOf", "mutableSetOf", "mapOf",
		"mutableMapOf", "arrayOf", "arrayListOf", "hashMapOf", "hashSetOf",
		"emptyList", "emptyMap", "emptySet", "emptyArray",
		"println", "print", "error", "check", "require",
		"TODO", "run", "let", "apply", "also", "with", "takeIf", "takeUnless",
		"lazy", "lazyOf",
		"Throwable", "Exception", "RuntimeException", "Error",
		"IllegalArgumentException", "IllegalStateException",
		"NullPointerException", "IndexOutOfBoundsException",
		"ClassCastException", "UnsupportedOperationException",
		"Comparable", "Comparator", "Function", "Runnable",
		"null", "true", "false":
		return true
	}
	return false
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
	h.Write([]byte("<builtin:kotlin>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
