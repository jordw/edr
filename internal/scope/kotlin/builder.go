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
//   - Single-expression functions `fun f(x: Int) = x * x` — open a
//     function scope on `=` (unless the RHS is a brace lambda) and
//     close it at the next top-level newline. Multi-line bodies that
//     continue past a newline (`= \n    x * x`) are handled only for
//     the single-line case.
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
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Kotlin source buffer. file is the
// canonical file path used to stamp Decl.File and Ref.File; pass the
// same path the caller will use when querying.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical namespace path
// used to hash file-scope DeclIDs. For Kotlin, canonicalPath is the
// file's package clause (e.g. "kotlin.collections"). When empty,
// behavior reduces to Parse — file-local DeclIDs.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:             file,
		canonicalPath:    canonicalPath,
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
	file          string
	canonicalPath string // "" ⇒ fall back to file for DeclID hashing
	res           *scope.Result
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

	// singleExprFnScope tracks an open single-expression function body
	// (opened on `=` when pendingScope is ScopeFunction and the RHS is
	// not a `{`-lambda). Closed at the next newline where the current
	// scope matches this ID (i.e., we're not nested inside a brace or
	// paren that the expression opened).
	singleExprFnScope scope.ScopeID

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

	// isImportDecl: consuming an `import foo.bar.Baz [as Alias]`. See
	// flushImport for emission; Signature = "<modulePath>\x00<origName>"
	// is stamped for the Phase-1 import graph resolver.
	isImportDecl        bool
	importPathBuf       []byte
	importOrigName      []byte
	importBuf           []byte
	importBufSpan       scope.Span
	importAliasExpected bool
	importIsStar        bool
	importWildcard      bool
	importWildSpan      scope.Span

	// isPackageDecl: consuming `package a.b.c`. Accumulates into
	// packageBuf; on `\n` / `;` flushed (emits a synthetic KindNamespace
	// decl at file scope, and stamps b.packagePath). The resolver reads
	// the KindNamespace to build FQNs of top-level decls.
	isPackageDecl bool
	packageBuf    []byte
	packagePath   string

	// pendingPrivate: `private` modifier seen at stmtStart. Consumed by
	// emitDecl so top-level decls stay Exported=false. Kotlin defaults
	// to public at file scope, so any top-level decl without `private`
	// is treated as Exported (v1: `internal` counts as exported).
	pendingPrivate bool

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
			if b.isPackageDecl {
				b.flushPackage()
			}
			b.stmtStart = true
			// Close single-expression function scope when the newline is
			// at the fn body's own depth (not inside a nested brace/paren
			// opened by the expression itself). We approximate this by
			// requiring currentScope() to still be the single-expr scope.
			if b.singleExprFnScope != 0 && b.currentScope() == b.singleExprFnScope {
				b.closeTopScope(uint32(b.s.Pos - 1))
				b.singleExprFnScope = 0
				b.lambdaBraceExpected = false
				b.declContext = ""
			}
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
			if b.isPackageDecl {
				b.flushPackage()
			}
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
			// Single-expression function body: `fun f(x) = expr`. If
			// pendingScope is still ScopeFunction (unclaimed by a `{`),
			// the RHS is not a brace lambda — open a function scope now
			// so params bind inside the expression instead of leaking.
			if b.pendingScope != nil && *b.pendingScope == scope.ScopeFunction &&
				b.peekNonWSByte() != '{' {
				b.openScope(scope.ScopeFunction, uint32(b.s.Pos))
				for _, g := range b.pendingGenerics {
					pk := g.kind
					if pk == "" {
						pk = scope.KindType
					}
					b.emitDecl(g.name, pk, g.span)
				}
				b.pendingGenerics = nil
				for _, p := range b.pendingParams {
					pk := p.kind
					if pk == "" {
						pk = scope.KindParam
					}
					b.emitDecl(p.name, pk, p.span)
				}
				b.pendingParams = nil
				b.pendingScope = nil
				b.singleExprFnScope = b.currentScope()
				b.lambdaBraceExpected = false
			}
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
		case c == '*':
			// Inside an import after '.', `*` is the wildcard marker:
			// `import com.acme.*`. Elsewhere `*` is arithmetic /
			// vararg spread — skip.
			if b.isImportDecl && b.prevByte == '.' {
				startByte := uint32(b.s.Pos)
				b.s.Pos++
				b.importWildcard = true
				b.importWildSpan = mkSpan(startByte, uint32(b.s.Pos))
				// Promote importBuf into the path so modulePath is
				// complete; clear importBuf so flushImport doesn't emit
				// a decl for it (wildcards are punted in v1).
				if len(b.importBuf) > 0 {
					if len(b.importPathBuf) > 0 {
						b.importPathBuf = append(b.importPathBuf, '.')
					}
					b.importPathBuf = append(b.importPathBuf, b.importBuf...)
					b.importBuf = b.importBuf[:0]
				}
				b.prevByte = '*'
				continue
			}
			b.s.Pos++
			b.prevByte = c
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

	// Package declaration: accumulate dotted name; emit nothing. The
	// complete path is stamped into b.packagePath at `\n` / `;`.
	if b.isPackageDecl {
		if len(b.packageBuf) == 0 {
			b.packageBuf = append(b.packageBuf[:0], word...)
		} else if b.prevByte == '.' {
			b.packageBuf = append(b.packageBuf, '.')
			b.packageBuf = append(b.packageBuf, word...)
		} else {
			// Malformed: adjacent idents with no '.'. Replace defensively.
			b.packageBuf = append(b.packageBuf[:0], word...)
		}
		b.prevByte = 'i'
		return
	}

	// Import declaration: collect the dotted module path, the source-
	// side last segment, and (on `as`) the local alias. See
	// parseImportSignature() in internal/scope/store/imports.go.
	if b.isImportDecl {
		if name == "as" && len(b.importBuf) > 0 && !b.importAliasExpected {
			// `import com.foo.Bar as X` — binding name becomes X;
			// origName stays as Bar. Save Bar, await the alias.
			b.importOrigName = append(b.importOrigName[:0], b.importBuf...)
			b.importAliasExpected = true
			b.prevByte = 'k'
			return
		}
		if b.importAliasExpected {
			// Alias ident becomes the new binding name. origName stays.
			b.importBuf = append(b.importBuf[:0], word...)
			b.importBufSpan = mkSpan(startByte, endByte)
			b.importAliasExpected = false
			b.prevByte = 'i'
			return
		}
		// Regular dotted path segment. Promote any existing importBuf
		// into the path prefix when a `.` precedes this ident.
		if len(b.importBuf) > 0 && b.prevByte == '.' {
			if len(b.importPathBuf) > 0 {
				b.importPathBuf = append(b.importPathBuf, '.')
			}
			b.importPathBuf = append(b.importPathBuf, b.importBuf...)
		}
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
		b.importPathBuf = b.importPathBuf[:0]
		b.importOrigName = b.importOrigName[:0]
		b.importAliasExpected = false
		b.importIsStar = false
		b.importWildcard = false
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
		// activates. Track `private` at stmtStart so the next top-level
		// decl is NOT marked Exported (Kotlin defaults to public).
		if name == "private" && wasStmtStart {
			b.pendingPrivate = true
		}
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

// flushImport emits the collected import as a KindImport decl with
// Signature = "<modulePath>\x00<origName>" for consumption by the
// Phase-1 import graph resolver (internal/scope/store/imports_kotlin.go).
//
// Wildcard imports (`import foo.bar.*`) are not emitted as decls in v1
// — per-file enumeration of wildcard-imported symbols is punted to a
// future pass; the resolver ignores them.
func (b *builder) flushImport() {
	if b.isImportDecl && !b.importWildcard && len(b.importBuf) > 0 {
		// Binding name = importBuf (alias if `as` was used, else last
		// dotted segment). origName = importOrigName if alias was used,
		// else same as binding name.
		bindingName := string(b.importBuf)
		var origName string
		if len(b.importOrigName) > 0 {
			origName = string(b.importOrigName)
		} else {
			origName = bindingName
		}
		modPath := string(b.importPathBuf)
		idx := len(b.res.Decls)
		b.emitDecl(bindingName, scope.KindImport, b.importBufSpan)
		if idx < len(b.res.Decls) {
			b.res.Decls[idx].Signature = modPath + "\x00" + origName
		}
	}
	b.isImportDecl = false
	b.importBuf = b.importBuf[:0]
	b.importPathBuf = b.importPathBuf[:0]
	b.importOrigName = b.importOrigName[:0]
	b.importAliasExpected = false
	b.importIsStar = false
	b.importWildcard = false
}

// flushPackage finalizes a `package a.b.c` clause. Stamps the dotted
// path onto b.packagePath and emits a synthetic KindNamespace decl at
// file scope with Name = full dotted path, so the Phase-1 import graph
// resolver (internal/scope/store/imports_kotlin.go) can associate each
// parsed file with its package to build FQNs of top-level decls.
func (b *builder) flushPackage() {
	if b.isPackageDecl {
		if len(b.packageBuf) > 0 {
			b.packagePath = string(b.packageBuf)
			// Emit a synthetic KindNamespace decl at file scope so the
			// resolver can discover each file's package without
			// reparsing. The span is conservative (covers whatever
			// idents were parsed into packageBuf up to the current
			// position); exact span isn't used by the resolver.
			end := uint32(b.s.Pos)
			startU := uint32(len(b.packageBuf))
			var nameStart uint32
			if end >= startU {
				nameStart = end - startU
			}
			b.emitDecl(b.packagePath, scope.KindNamespace,
				mkSpan(nameStart, end))
		}
		b.packageBuf = b.packageBuf[:0]
	}
	b.isPackageDecl = false
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
		// If no explicit params are present (no `->`), synthesize the
		// implicit `it` param so references to `it` resolve.
		kind = scope.ScopeFunction
		params := b.scanLambdaParams()
		if params == nil {
			bracePos := uint32(b.s.Pos - 1)
			params = []pendingParam{{
				name: "it",
				span: mkSpan(bracePos, bracePos+1),
				kind: scope.KindParam,
			}}
		}
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
// closes, returns nil (implicit `it` lambda); the caller is responsible
// for synthesizing the implicit `it` param in that case. A present but
// empty param list `{ -> body }` returns a non-nil empty slice so the
// caller will NOT synthesize `it`.
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
	// Canonical DeclID for file-scope decls — same identity across
	// every file in the same Kotlin package, enabling cross-file
	// rename to match an import-resolved ref to the target decl by ID.
	hashPath := b.file
	if scopeID == scope.ScopeID(1) && b.canonicalPath != "" {
		hashPath = b.canonicalPath
	}
	declID := hashDecl(hashPath, name, ns, scopeID)

	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}

	// Exported: Kotlin defaults to `public` at file scope, so any top-
	// level decl without `private` is importable. Only applies to file-
	// scope decls (not nested members, not imports — imports are local
	// bindings, not exports — and not package-marker namespaces).
	exported := false
	if kind != scope.KindImport && kind != scope.KindParam &&
		kind != scope.KindNamespace &&
		b.currentScopeKind() == scope.ScopeFile && !b.pendingPrivate {
		exported = true
	}

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
		Exported:  exported,
	})

	switch kind {
	case scope.KindClass, scope.KindInterface, scope.KindEnum,
		scope.KindMethod, scope.KindFunction:
		b.pendingOwnerDecl = idx
	}
	b.pendingFullStart = 0
	// Clear pendingPrivate once consumed. Imports and package markers
	// don't consume it.
	if kind != scope.KindImport && kind != scope.KindNamespace {
		b.pendingPrivate = false
	}
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
	byKey := make(map[key][]*scope.Decl, len(b.res.Decls))
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		k := key{scope: d.Scope, name: d.Name, ns: d.Namespace}
		byKey[k] = append(byKey[k], d)
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
	// lookupLexical returns the decl whose Span.EndByte is the
	// latest that still precedes refStart. Matches Kotlin's
	// block-scope rule: a local var only becomes visible at the
	// point of declaration, so a ref before the local must bind to
	// an enclosing scope's decl. At file scope we allow forward
	// references (calling a top-level fun defined later in the
	// file).
	lookupLexical := func(k key, refStart uint32, scopeIsFile bool) *scope.Decl {
		var best *scope.Decl
		var bestEnd uint32
		for _, d := range byKey[k] {
			if d.Span.EndByte > refStart {
				continue
			}
			if best == nil || d.Span.EndByte > bestEnd {
				best = d
				bestEnd = d.Span.EndByte
			}
		}
		if best != nil {
			return best
		}
		if scopeIsFile && len(byKey[k]) > 0 {
			return byKey[k][0]
		}
		return nil
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
			curIsFile := scopeByID[cur].Kind == scope.ScopeFile
			if d := lookupLexical(key{scope: cur, name: r.Name, ns: r.Namespace}, r.Span.StartByte, curIsFile); d != nil {
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
				if d := lookupLexical(key{scope: 0, name: r.Name, ns: r.Namespace}, r.Span.StartByte, true); d != nil {
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
			if builtins.Kotlin.Has(r.Name) {
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
	h.Write([]byte("<builtin:kotlin>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
