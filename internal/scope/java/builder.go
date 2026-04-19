// Package java is the Java scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single Java source
// file. Handles file / class / interface / method / block scopes and
// class/interface/enum/record/@interface/method/constructor/field/param/
// local-var/import/generic-type-param declarations. Identifiers not in a
// declaration position are emitted as Refs and resolved via scope-chain
// walk to the innermost matching Decl.
//
// v1 deferred items (intentional simplifications):
//   - Method overloading ambiguity: multiple methods with the same name
//     but different signatures all emit as same-name NSField decls in
//     the class scope. refs-to matches by name and will return all
//     overloads. Signature-based disambiguation is a later pass.
//   - `var x = expr` local type inference: x is emitted as KindVar with
//     no type tracked. Same for diamond operator `new ArrayList<>()`.
//   - Inheritance resolution: `extends` / `implements` types are emitted
//     as refs. resolveRefs walks ONE level of same-file inheritance for
//     unqualified and `this.X` field lookups: if a subclass's own fields
//     do not contain name X, its direct supertype's fields (defined in
//     the same file) are checked. v1 punts:
//       * Multi-level chains (`C extends P extends G`) — fields on G
//         are NOT resolved from C.
//       * Cross-file supertypes — if the supertype class is not declared
//         in this file, the ref stays unresolved.
//       * Interface default methods and abstract method inheritance.
//     Reason code for successful inherited lookups is "inherited_field".
//   - Sealed classes, switch expressions, and pattern matching beyond
//     basic scope machinery (no special handling for patterns / yield).
//   - Lambda expressions push a function scope on '(' … ')' … '->' (like
//     TS arrow). Method references (`Foo::bar`) emit a ref on Foo and a
//     property_access ref on bar.
//   - Nested classes other than anonymous inner classes (`new Foo(){}`).
//     Regular named nested classes declare themselves at the enclosing
//     class scope rather than forming a qualified-name hierarchy.
//   - Package-private vs public visibility: all decls treated equally.
//   - Static members: treated identically to instance members for v1.
//   - Package declarations (`package com.foo;`) are consumed but emit no
//     decls.
package java

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Java source buffer.
// file is the canonical file path used to stamp Decl.File and Ref.File;
// pass the same path the caller will use when querying.
func Parse(file string, src []byte) *scope.Result {
	b := &builder{
		file:                file,
		res:                 &scope.Result{File: file},
		s:                   lexkit.New(src),
		pendingOwnerDecl:    -1,
		pendingClassDeclIdx: -1,
		classSuperTypes:     make(map[int][]string),
		classBodyScope:      make(map[int]scope.ScopeID),
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
	// ownerDeclIdx is the index in res.Decls of the decl that owns
	// this scope; closeTopScope patches FullSpan.EndByte on that decl.
	// -1 when the scope was not introduced by a decl (plain block).
	ownerDeclIdx int
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	// stmtStart is true at the top of a fresh statement — after '{',
	// ';', or a newline at the top level.
	stmtStart bool

	// prevByte tracks the last non-whitespace, non-comment byte.
	// Property-access detection: `x.Name` — Name has prevByte == '.'.
	prevByte byte

	// prevIdentIsThis / prevIdentIsSuper track whether the most recent
	// identifier was `this` or `super`, so a following `.X` resolves
	// against the enclosing class's NSField decls.
	prevIdentIsThis  bool
	prevIdentIsSuper bool

	// pendingScope, if non-nil, is consumed by the next '{' as the
	// scope kind to push. Set by class/interface/enum/record/method
	// headers.
	pendingScope *scope.ScopeKind


	// declContext classifies the next identifier as a declaration of
	// this kind. Set by class/interface/enum/record.
	declContext scope.DeclKind

	// pendingFullStart: byte position+1 of the most recent decl
	// keyword, used as FullSpan.StartByte for scope-owning decls.
	// 0 means unset.
	pendingFullStart uint32

	// pendingOwnerDecl: index in res.Decls of the last scope-owning
	// decl. Consumed by the next openScope.
	pendingOwnerDecl int

	// paramListPending: after a method name, the next '(' begins a
	// param list whose idents become KindParam decls.
	paramListPending bool

	// inParamList: inside (...) of a method/ctor/lambda param list.
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool

	// pendingParams collects param decls during (...) — emitted when
	// the method body '{' opens its scope.
	pendingParams []pendingParam

	// genericParamsExpected: after a decl name, the next '<' begins a
	// generic type-param list.
	genericParamsExpected bool

	// inGenericParams + genericDepth + genericSectionNeedsName mirror
	// the param-list state machine for generic <...>.
	inGenericParams         bool
	genericDepth            int
	genericSectionNeedsName bool

	// pendingGenerics collects type-param decls from a class/method
	// generic header. Flushed into the newly opened class/method scope
	// when its body '{' opens.
	pendingGenerics []pendingParam

	// parenVarStack saves state at each '(' and '['; restored on the
	// matching ')' / ']'.
	parenVarStack []scope.DeclKind

	// typePositionIdent: previous ident in this statement was a type;
	// the next ident is the variable / field name.
	typePositionIdent bool

	// localVarDeclKind remembers the current local var kind so commas
	// in a multi-decl `int a, b, c;` can re-enter decl mode.
	localVarDeclKind scope.DeclKind

	// isImportDecl: consuming an `import [static] a.b.c;` — emit final
	// ident as a KindImport decl on `;`.
	isImportDecl     bool
	importStaticFlag bool
	importBuf        []byte
	importBufSpan    scope.Span

	// isPackageDecl: consuming `package a.b.c;` — emit nothing.
	isPackageDecl bool

	// anonInnerExpected: `new Foo(...)` was just parsed; if the next
	// `{` arrives (after the close ')'), treat as anonymous inner class
	// body (ScopeClass) so its methods/fields are classified.
	anonInnerExpected bool

	// forHeaderExpected: `for` was just parsed; the next `(` begins a
	// header whose contents declare local vars (`int x : coll`).
	forHeaderExpected bool

	// inForHeader: inside a `for (...)` header's parens. Treat idents
	// like a function-body statement (type-then-name local var).
	inForHeader bool
	forHeaderDepth int

	// inSuperTypes: between an `extends` / `implements` keyword in a
	// class/interface header and the opening `{` of its body. Idents
	// encountered here are supertype names and are recorded against
	// the pending class decl so resolveRefs can walk one level of
	// inheritance (same-file only) for field lookup.
	inSuperTypes bool

	// superTypeNeedsName: true at the start of a supertype section
	// (after `extends` / `implements` or a top-level `,`). Only the
	// first ident in the section is recorded as the supertype name —
	// subsequent idents inside `<...>` type args are ignored for
	// supertype purposes.
	superTypeNeedsName bool

	// pendingClassDeclIdx: index in res.Decls of the class/interface
	// decl currently being parsed (between its name and its body `{`).
	// -1 when not in a class header. Used to associate supertype names
	// with the right class.
	pendingClassDeclIdx int

	// classSuperTypes maps a class/interface decl's index in res.Decls
	// to the list of supertype names declared via extends/implements
	// in the same file. Used by resolveRefs for one-level same-file
	// inheritance lookup.
	classSuperTypes map[int][]string

	// classBodyScope maps a class/interface decl's index in res.Decls
	// to the ScopeID of its body scope. Populated when the body `{`
	// opens. Used by resolveRefs to find a supertype's class body
	// scope and index its fields.
	classBodyScope map[int]scope.ScopeID
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
			b.stmtStart = true
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			b.s.ScanSimpleString('"')
			b.stmtStart = false
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.stmtStart = false
			b.prevByte = '\''
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
			b.genericParamsExpected = false
			if b.isImportDecl {
				if len(b.importBuf) > 0 {
					b.emitDecl(string(b.importBuf), scope.KindImport, b.importBufSpan)
				}
				b.isImportDecl = false
				b.importStaticFlag = false
				b.importBuf = b.importBuf[:0]
			}
			b.isPackageDecl = false
			b.pendingParams = nil
			b.pendingGenerics = nil
			b.anonInnerExpected = false
			b.forHeaderExpected = false
			// Safety: clear class-header state if we somehow hit `;`
			// (e.g. malformed source or `class Foo;` forward decls).
			b.inSuperTypes = false
			b.superTypeNeedsName = false
			b.pendingClassDeclIdx = -1
		case c == '(':
			b.s.Pos++
			b.parenVarStack = append(b.parenVarStack, b.localVarDeclKind)
			b.prevByte = '('
			if b.paramListPending {
				b.paramListPending = false
				b.genericParamsExpected = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.inParamList {
				b.paramDepth++
			} else if b.forHeaderExpected {
				b.forHeaderExpected = false
				b.inForHeader = true
				b.forHeaderDepth = 1
				// Start of for-header acts like stmt start for type/name.
				b.stmtStart = true
			} else if b.inForHeader {
				b.forHeaderDepth++
			}
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
				}
			}
			if b.inForHeader {
				b.forHeaderDepth--
				if b.forHeaderDepth == 0 {
					b.inForHeader = false
					b.typePositionIdent = false
					b.localVarDeclKind = ""
				}
			}
		case c == '[':
			b.s.Pos++
			b.prevByte = '['
			b.parenVarStack = append(b.parenVarStack, b.localVarDeclKind)
			if b.inParamList {
				b.paramDepth++
			}
		case c == ']':
			b.s.Pos++
			b.prevByte = ']'
			if n := len(b.parenVarStack); n > 0 {
				b.localVarDeclKind = b.parenVarStack[n-1]
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
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
			}
			if b.inGenericParams && b.genericDepth == 1 {
				b.genericSectionNeedsName = true
			}
			// Multi-name local var: `int a, b, c;` — comma re-enables
			// typePositionIdent so the next ident is another var name.
			if b.localVarDeclKind != "" && !b.inParamList && !b.inGenericParams {
				b.typePositionIdent = true
			}
			// Supertype list (`implements A, B, C`) — re-arm to capture
			// the next ident as the next supertype name.
			if b.inSuperTypes && !b.inGenericParams {
				b.superTypeNeedsName = true
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
			// Lambda arrow. For v1: if the next non-ws byte after '->'
			// is '{', open a block-body lambda when '{' is processed;
			// the pending params collected in the preceding '(' list
			// (captured as pendingParams) will flush. Expression-body
			// lambdas open a function scope right here that closes at
			// the next ';' or ',' or ')' terminator — at those points
			// it falls out naturally via closeTopScope... except that
			// we don't have a matching '}'. For v1, we treat
			// expression-body lambdas by opening a function scope here
			// and closing it when its host enclosing brace/paren
			// closes. In practice v1 only reliably captures block-body
			// lambdas; expression bodies get conservative handling.
			arrowStart := uint32(b.s.Pos)
			b.s.Advance(2)
			b.prevByte = '>'
			k := scope.ScopeFunction
			b.pendingScope = &k
			if b.pendingFullStart == 0 {
				b.pendingFullStart = arrowStart + 1
			}
		case c == '@':
			b.s.Pos++
			b.prevByte = '@'
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' &&
					cc != 'x' && cc != 'X' && cc != 'e' && cc != 'E' &&
					cc != 'p' && cc != 'P' && cc != 'L' && cc != 'l' &&
					cc != 'F' && cc != 'f' && cc != 'D' && cc != 'd' {
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
		if name == "static" && !b.importStaticFlag && len(b.importBuf) == 0 {
			b.importStaticFlag = true
			b.prevByte = 'k'
			return
		}
		b.importBuf = append(b.importBuf[:0], word...)
		b.importBufSpan = mkSpan(startByte, endByte)
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
		b.importStaticFlag = false
		b.importBuf = b.importBuf[:0]
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
		// `@interface Foo` — preceded by '@' — is an annotation type.
		// For v1 treat identically to regular interface.
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			if b.prevByte == '@' {
				b.pendingFullStart = startByte // cover '@'
			} else {
				b.pendingFullStart = startByte + 1
			}
		}
		b.prevByte = 'k'
		return
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeClass // scope behaves like class
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "record":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "public", "protected", "private", "static", "final", "abstract",
		"synchronized", "native", "strictfp", "default", "transient",
		"volatile", "sealed":
		// Modifiers preserve stmtStart so a following decl keyword still
		// triggers decl context.
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "return", "if", "else", "while", "do", "switch", "case",
		"break", "continue", "throw", "try", "catch", "finally",
		"instanceof", "assert", "throws",
		"yield", "permits":
		b.prevByte = 'k'
		return
	case "extends", "implements":
		// Inside a class/interface header (pendingClassDeclIdx is set),
		// idents that follow until the body `{` are supertype names —
		// capture them for one-level same-file inheritance resolution.
		if b.pendingClassDeclIdx >= 0 {
			b.inSuperTypes = true
			b.superTypeNeedsName = true
		}
		b.prevByte = 'k'
		return
	case "new":
		// `new` precedes a constructor call. Flag so the following
		// `{` (after `(...)`) is recognized as an anonymous inner
		// class body rather than a plain block.
		b.anonInnerExpected = true
		b.prevByte = 'k'
		return
	case "for":
		// `for (...)` header: the next `(` begins a local-var-like
		// context (`int x : coll` / `int i = 0; ...`). Flag so the
		// `(` handler treats its contents like a local var statement.
		b.forHeaderExpected = true
		b.prevByte = 'k'
		return
	case "this":
		b.prevIdentIsThis = true
		b.prevIdentIsSuper = false
		b.prevByte = 'k'
		return
	case "super":
		b.prevIdentIsSuper = true
		b.prevIdentIsThis = false
		b.prevByte = 'k'
		return
	case "true", "false", "null":
		b.prevByte = 'k'
		return
	case "void":
		// `void` is a type keyword that precedes a method name. Treat
		// as a modifier-like keyword: preserve stmtStart, emit nothing.
		// Emit a ref so refs-to picks up `void` uses? It's a keyword,
		// not an identifier, so skip emission.
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "var":
		// Java 10+ local var inference. Only at statement start.
		if wasStmtStart {
			// Treat as a type-position marker: next ident is a var decl.
			b.typePositionIdent = true
			b.localVarDeclKind = scope.KindVar
			b.prevByte = 'k'
			return
		}
	}

	// Property access after '.'.
	if b.prevByte == '.' {
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

	// Supertype capture: in a class header after `extends` / `implements`,
	// the first ident per comma-section is the supertype's simple name.
	// Record it against the pending class decl, then fall through so the
	// ident is still emitted as a normal ref (existing behavior).
	if b.inSuperTypes && b.superTypeNeedsName && b.pendingClassDeclIdx >= 0 &&
		!b.inGenericParams {
		b.classSuperTypes[b.pendingClassDeclIdx] = append(
			b.classSuperTypes[b.pendingClassDeclIdx], name)
		b.superTypeNeedsName = false
	}

	// Generic type-param list: first ident per section becomes a pending
	// type decl. `T extends Foo` — `extends` is a keyword, so `Foo`
	// arrives here with genericSectionNeedsName=false and is emitted as
	// a ref below.
	if b.inGenericParams && b.genericDepth == 1 && b.genericSectionNeedsName {
		b.pendingGenerics = append(b.pendingGenerics, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindType,
		})
		b.genericSectionNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Param list at top depth: alternate type / name. First ident per
	// section is the type (ref); second is the param name.
	if b.inParamList && b.paramDepth == 1 {
		if name == "final" {
			b.prevByte = 'k'
			return
		}
		if b.paramSectionNeedsName {
			// First ident = type ref; next ident is the param name.
			b.emitRef(name, mkSpan(startByte, endByte))
			b.paramSectionNeedsName = false
			b.typePositionIdent = true
			b.prevByte = 'i'
			return
		}
		if b.typePositionIdent {
			// Second ident = param name.
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: name,
				span: mkSpan(startByte, endByte),
				kind: scope.KindParam,
			})
			b.typePositionIdent = false
			b.prevByte = 'i'
			return
		}
		// Later idents in the same section (rare) emit as refs.
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// declContext set (class / interface / enum / record name follows).
	if b.declContext != "" {
		kind := b.declContext
		// Record the decl index so any following `extends` / `implements`
		// idents can be attached to this class for inheritance resolution.
		if kind == scope.KindClass || kind == scope.KindInterface || kind == scope.KindEnum {
			b.pendingClassDeclIdx = len(b.res.Decls)
		}
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		b.genericParamsExpected = true
		// For record: the next '(' begins the compact header component
		// list, which declares implicit fields. For v1 we treat the
		// record header like a method param list — idents inside become
		// KindParam in the pendingParams list, which then flush into
		// the class body scope as params (not fields). Correctness-wise
		// this is close: refs to those names inside the record body
		// bind correctly via scope walk.
		b.prevByte = 'i'
		return
	}

	scopeK := b.currentScopeKind()

	// Class / interface body.
	if scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface {
		nextCh := b.peekNonWSByte()

		if nextCh == '(' || nextCh == '<' {
			// Method or constructor. Both emit as KindMethod; Java's
			// constructor detection (name matches enclosing class) is
			// implicit — refs-to consumers match by name.
			// Stamp pendingFullStart before emit so FullSpan.StartByte
			// covers the method name itself (there's no preceding
			// method-specific keyword — modifiers like `public` and
			// return types like `void` are handled elsewhere).
			if b.pendingFullStart == 0 {
				b.pendingFullStart = startByte + 1
			}
			b.emitDecl(name, scope.KindMethod, mkSpan(startByte, endByte))
			b.paramListPending = true
			b.genericParamsExpected = true
			fs := scope.ScopeFunction
			b.pendingScope = &fs
			b.typePositionIdent = false
			b.prevByte = 'i'
			return
		}
		if b.typePositionIdent {
			// Previous ident was the field type; this ident is the name.
			b.emitDecl(name, scope.KindField, mkSpan(startByte, endByte))
			b.typePositionIdent = false
			// Stay in multi-field awareness: a following ',' re-enables
			// typePositionIdent via the ',' branch of run().
			b.localVarDeclKind = scope.KindField
			b.prevByte = 'i'
			return
		}
		// First ident on the line in class body — emit as ref (it's
		// the type); prime typePositionIdent so the next ident is the
		// field name. `void foo()` routes around this via the `void`
		// keyword case above.
		b.emitRef(name, mkSpan(startByte, endByte))
		b.typePositionIdent = true
		b.prevByte = 'i'
		return
	}

	// Method body / function scope.
	if scopeK == scope.ScopeFunction {
		if b.typePositionIdent {
			// Ident after a type — could be a var name, or a method
			// call `Foo.bar(...)` where we mis-primed. Peek at what
			// follows to decide.
			nextCh := b.peekNonWSByte()
			switch nextCh {
			case '=', ';', ',', ':':
				// Var name: `int x = ...`, `String s;`, `int a, b`,
				// `for (int x : coll)`.
				b.emitDecl(name, scope.KindVar, mkSpan(startByte, endByte))
				b.typePositionIdent = false
				b.localVarDeclKind = scope.KindVar
				b.prevByte = 'i'
				return
			case '(':
				// Method call like `foo(...)` after a type ref that
				// turned out to be wrong — clear state and emit ref.
				b.typePositionIdent = false
				b.emitRef(name, mkSpan(startByte, endByte))
				b.prevByte = 'i'
				return
			case '[':
				// Could be `int[] a` (type array) or `arr[0]` access.
				// Peek past `[`: if next non-ws is `]`, it's array type,
				// and the ident AFTER `]` is the var name. Otherwise
				// it's array index access. For v1 simplicity, assume
				// it's a type array — emit the ident as a type ref and
				// keep typePositionIdent primed.
				b.emitRef(name, mkSpan(startByte, endByte))
				b.prevByte = 'i'
				return
			default:
				// Unclear — treat as var name if followed by nothing
				// we recognize; otherwise as ref.
				b.emitDecl(name, scope.KindVar, mkSpan(startByte, endByte))
				b.typePositionIdent = false
				b.localVarDeclKind = scope.KindVar
				b.prevByte = 'i'
				return
			}
		}
		if wasStmtStart {
			// First ident at statement start in a function body. Could be
			// a type (for a local var decl) or a bare expression ident.
			// Peek ahead: if followed by an ident-start char, it's a
			// type ref for a var decl.
			save := b.s.Pos
			saveLine := b.s.Line
			b.skipWS()
			nextIsIdentOrBracket := false
			if !b.s.EOF() {
				nc := b.s.Peek()
				if lexkit.DefaultIdentStart[nc] || nc == '<' || nc == '[' {
					nextIsIdentOrBracket = true
				}
			}
			b.s.Pos = save
			b.s.Line = saveLine
			if nextIsIdentOrBracket {
				b.emitRef(name, mkSpan(startByte, endByte))
				b.typePositionIdent = true
				b.localVarDeclKind = scope.KindVar
				b.prevByte = 'i'
				return
			}
			// Bare ident at stmt-start (assignment LHS, method call, etc.).
			b.emitRef(name, mkSpan(startByte, endByte))
			b.prevByte = 'i'
			return
		}
	}

	// Check for bare-ident lambda: `x -> body`.
	if !b.inParamList && !b.inGenericParams && b.declContext == "" &&
		b.prevByte != '.' && b.peekArrow() {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindParam,
		})
		b.prevByte = 'i'
		return
	}

	// Fallback: emit as a ref.
	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

func (b *builder) handleOpenBrace() {
	prev := b.prevByte
	b.s.Pos++
	b.prevByte = '{'
	b.stmtStart = true

	kind := scope.ScopeBlock
	if b.pendingScope != nil {
		kind = *b.pendingScope
		b.pendingScope = nil
		b.genericParamsExpected = false
	} else if b.anonInnerExpected && prev == ')' {
		// Anonymous inner class body: `new Foo() { ... }`. Push a
		// ScopeClass so its member decls are classified as fields /
		// methods. We do not emit a Decl for the anonymous class itself
		// (no name), so ownerDeclIdx stays -1.
		b.anonInnerExpected = false
		kind = scope.ScopeClass
	}
	b.openScope(kind, uint32(b.s.Pos-1))
	// If we just opened a class/interface body with a pending class decl,
	// map the decl index to this body scope for later inheritance lookup,
	// and clear the class-header state.
	if (kind == scope.ScopeClass || kind == scope.ScopeInterface) &&
		b.pendingClassDeclIdx >= 0 {
		b.classBodyScope[b.pendingClassDeclIdx] = b.currentScope()
		b.pendingClassDeclIdx = -1
		b.inSuperTypes = false
		b.superTypeNeedsName = false
	}
	// Flush generics into the newly opened scope (class or method).
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
	// Flush params into a function scope.
	if kind == scope.ScopeFunction && len(b.pendingParams) > 0 {
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

// peekArrow reports whether the next non-ws token is '->'. Does not
// advance the scanner.
func (b *builder) peekArrow() bool {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() {
		b.s.Pos = save
		b.s.Line = saveLine
	}()
	b.skipWS()
	if b.s.EOF() {
		return false
	}
	return b.s.Peek() == '-' && b.s.PeekAt(1) == '>'
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
// at `span` against the nearest enclosing class's NSField decls. If a
// match is found it emits a resolved ref and returns true. If no match
// is found within the class's own fields but an enclosing class exists,
// it emits a pending NSField ref (marked "this_dot_field_pending") so
// resolveRefs can try one level of same-file supertype inheritance,
// and returns true. Returns false only if no enclosing class exists.
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
	// Not found locally; emit a pending NSField ref for resolveRefs to
	// try one level of same-file supertype inheritance.
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
			Kind:   scope.BindUnresolved,
			Reason: "this_dot_field_pending",
		},
	})
	return true
}

// resolveRefs binds each Ref to a Decl via scope-chain walk, falling
// back to signature-position generics, Java builtins, then unresolved.
//
// One level of same-file inheritance is resolved here: for unqualified
// refs and `this.X` refs inside a subclass whose own fields don't have
// X, the direct supertype's fields (same file) are checked and bound
// with Reason "inherited_field". Multi-level chains and cross-file
// supertypes are out of scope for v1.
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
	// Class NSField index: maps (classScope, name, NSField) so an
	// implicit-this bare ident (like `value` inside a method without
	// the `this.` prefix) can resolve to the enclosing class's field.
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

	// classNameToBodyScope maps a class/interface's simple name to the
	// ScopeID of its body scope. Used to look up a supertype's fields
	// by name during one-level inheritance resolution. Duplicate names
	// (multiple classes with the same simple name in one file) resolve
	// to the first declared — v1 punt.
	classNameToBodyScope := make(map[string]scope.ScopeID, len(b.classBodyScope))
	for declIdx, bodyScope := range b.classBodyScope {
		if declIdx < 0 || declIdx >= len(b.res.Decls) {
			continue
		}
		name := b.res.Decls[declIdx].Name
		if _, ok := classNameToBodyScope[name]; !ok {
			classNameToBodyScope[name] = bodyScope
		}
	}
	// classScopeSupers maps a class body scope ID to the list of
	// supertype body scope IDs declared via extends/implements (same
	// file). A supertype name that isn't declared in this file is
	// omitted (no cross-file resolution in v1).
	classScopeSupers := make(map[scope.ScopeID][]scope.ScopeID, len(b.classBodyScope))
	for declIdx, bodyScope := range b.classBodyScope {
		names := b.classSuperTypes[declIdx]
		if len(names) == 0 {
			continue
		}
		supers := make([]scope.ScopeID, 0, len(names))
		for _, n := range names {
			if s, ok := classNameToBodyScope[n]; ok {
				supers = append(supers, s)
			}
		}
		if len(supers) > 0 {
			classScopeSupers[bodyScope] = supers
		}
	}

	// tryInheritedField walks one level of same-file supertypes of
	// classScope looking for an NSField decl named `name`. Returns the
	// decl if found, nil otherwise. Does NOT recurse — v1 is one level.
	tryInheritedField := func(classScope scope.ScopeID, name string) *scope.Decl {
		supers, ok := classScopeSupers[classScope]
		if !ok {
			return nil
		}
		for _, sup := range supers {
			if d, ok := classField[key{scope: sup, name: name, ns: scope.NSField}]; ok {
				return d
			}
		}
		return nil
	}

	for i := range b.res.Refs {
		r := &b.res.Refs[i]
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "this_dot_field" {
			continue
		}
		// `this.X` / `super.X` where X wasn't found in the current
		// class's own fields — try one level of same-file inheritance.
		if r.Binding.Reason == "this_dot_field_pending" {
			if cls := nearestClass[r.Scope]; cls != 0 {
				if d := tryInheritedField(cls, r.Name); d != nil {
					r.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   d.ID,
						Reason: "inherited_field",
					}
					continue
				}
			}
			r.Binding = scope.RefBinding{
				Kind:   scope.BindUnresolved,
				Reason: "missing_import",
			}
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
			// Implicit-this field access.
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
			// Inherited implicit-this: one level of same-file supertypes.
			if cls := nearestClass[r.Scope]; cls != 0 {
				if d := tryInheritedField(cls, r.Name); d != nil {
					r.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   d.ID,
						Reason: "inherited_field",
					}
					resolved = true
				}
			}
		}
		if !resolved {
			// Signature-position generics: decl lives in a later-opened
			// scope that encloses the ref.
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
			if builtins.Java.Has(r.Name) {
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
	h.Write([]byte("<builtin:java>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
