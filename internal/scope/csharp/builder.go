// Package csharp is the C# scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single C# source
// file. Handles file / namespace / class / interface / struct / record /
// enum / method / block scopes and class/interface/struct/record/enum/
// namespace/method/property/field/param/local-var/using/generic-type-param
// declarations. Identifiers not in a declaration position are emitted as
// Refs and resolved via scope-chain walk to the innermost matching Decl.
//
// v1 deferred items (intentional simplifications):
//   - Method overloading ambiguity: multiple methods with the same name
//     but different signatures all emit as same-name NSField decls in
//     the class scope. refs-to matches by name and will return all
//     overloads. Signature-based disambiguation is a later pass.
//   - Explicit interface implementation (`void IFoo.Bar() {}`) is emitted
//     as KindMethod without any attachment to the interface.
//   - Property getter/setter bodies are treated as opaque — we do not
//     push a function scope for `{ get { ... } set { ... } }`.
//   - Tuple destructuring `(int a, int b) = point;` emits only the
//     leftmost ident as a decl.
//   - LINQ query syntax (`from x in xs`) is not specially handled.
//   - `unsafe` / `fixed` / pointer syntax is parsed through without any
//     dedicated handling.
//   - `partial class` — each partial is its own class decl (no cross-file
//     merging for v1).
//   - Attributes `[Attr(...)]` are consumed but their bodies emit no
//     refs (we drop the identifiers inside them).
//   - `async` / `await` are parsed through — no scope semantics.
package csharp

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a C# source buffer.
// file is the canonical file path used to stamp Decl.File and Ref.File;
// pass the same path the caller will use when querying.
// Parse extracts a scope.Result from a C# source buffer.
// File-scope decls hash with the file path.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path for
// file-scope DeclID hashing.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:                    file,
		canonicalPath:           canonicalPath,
		res:                     &scope.Result{File: file},
		s:                       lexkit.New(src),
		pendingOwnerDecl:        -1,
		recordOwnerDeclIdx:      -1,
		pendingNamespaceDeclIdx: -1,
		namespacePathDeclIdx:    -1,
		pendingClassDeclIdx:     -1,
		classSuperTypes:         map[int][]string{},
	}
	b.openScope(scope.ScopeFile, 0)
	b.stmtStart = true
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	// Copy classSuperTypes onto Decl.SuperTypes for the cross-file
	// hierarchy walker.
	for idx, supers := range b.classSuperTypes {
		if idx >= 0 && idx < len(b.res.Decls) && len(supers) > 0 {
			b.res.Decls[idx].SuperTypes = supers
		}
	}
	scope.MergeDuplicateDecls(b.res)
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
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	// stmtStart is true at the top of a fresh statement — after '{',
	// ';', or a newline at the top level.
	stmtStart bool

	// prevByte tracks the last non-whitespace, non-comment byte.
	// Property-access detection: `x.Name` — Name has prevByte == '.'.
	prevByte byte

	// prevIdentIsThis / prevIdentIsBase track whether the most recent
	// identifier was `this` or `base`, so a following `.X` resolves
	// against the enclosing class/struct/record NSField decls.
	prevIdentIsThis bool
	prevIdentIsBase bool

	// pendingScope, if non-nil, is consumed by the next '{' as the
	// scope kind to push.
	pendingScope *scope.ScopeKind

	// declContext classifies the next identifier as a declaration of
	// this kind. Set by class/interface/struct/record/enum/namespace.
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

	// recordPrimaryCtor: when set, the next '(' after a record name
	// begins the primary constructor parameter list whose idents become
	// KindField decls scoped to the record.
	recordPrimaryCtor bool

	// pendingRecordFields collects primary-constructor fields during
	// the '(...)' of `record Point(int X, int Y)`. These are emitted
	// directly into the record body scope when its '{' opens, OR if no
	// body follows, at the enclosing scope.
	pendingRecordFields []pendingParam
	// recordOwnerDeclIdx: index of the pending record decl so we can
	// attach fields to the record's body scope once it opens.
	recordOwnerDeclIdx int

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
	// the next ident is the variable / field / property name.
	typePositionIdent bool

	// localVarDeclKind remembers the current local var kind so commas
	// in a multi-decl `int a, b, c;` can re-enter decl mode.
	localVarDeclKind scope.DeclKind

	// isUsingDecl: consuming a `using [static] a.b.c;` or
	// `using Alias = System.List;` — emit the appropriate ident as a
	// KindImport decl on ';'.
	//
	// usingBuf holds the most recently consumed ident (the last
	// segment of a dotted name); usingPathBuf accumulates the full
	// dotted prefix so the import-graph resolver can reconstruct
	// the FQN. Signature convention (shared with Java, decoded by
	// parseImportSignature in internal/scope/store/imports.go):
	//   using System;               -> Signature="System\x00*"
	//   using Foo.Bar;              -> Signature="Foo.Bar\x00*"
	//   using Alias = Foo.Bar;      -> Signature="Foo\x00Bar"
	//   using Alias = Foo.Bar.Baz;  -> Signature="Foo.Bar\x00Baz"
	//   using static Foo.Util;      -> Signature="static:Foo.Util\x00*"
	isUsingDecl     bool
	usingStaticFlag bool
	usingIsAlias    bool
	usingAliasName  []byte
	usingAliasSpan  scope.Span
	usingBuf        []byte
	usingBufSpan    scope.Span
	// usingPathBuf: when consuming `using a.b.c;`, on each ident
	// after a '.' we promote the previous usingBuf contents into
	// this prefix (joined with '.') so usingBuf always holds the
	// trailing segment.
	usingPathBuf []byte

	// pendingPublic: the most recent modifier run included
	// `public`. Consumed by emitDecl to set Exported=true on
	// top-level class/interface/enum decls. `internal`/`protected`/
	// `private` stay unexported; Phase-1 treats `internal` as
	// not-exported because assembly boundaries aren't modeled — the
	// conservative-correct choice leaves the ref bound to the local
	// Import decl when we can't prove the target is visible.
	pendingPublic bool

	// namespacePathBuf / namespacePathDeclIdx: accumulate the full
	// dotted namespace name while consuming `namespace A.B.C`. On
	// the first ident after `namespace`, the decl is emitted and
	// its index cached here; subsequent idents (after '.') extend
	// the Signature stamped on that decl. Reset on '{' or ';'.
	namespacePathBuf     []byte
	namespacePathDeclIdx int

	// foreachHeaderExpected: `foreach` was just parsed; the next '('
	// begins a header whose `var x in coll` declares x.
	foreachHeaderExpected bool
	// inForeachHeader: inside `foreach (...)` header parens.
	inForeachHeader    bool
	foreachHeaderDepth int

	// forHeaderExpected / inForHeader mirror the Java state machine
	// for C-style for loops.
	forHeaderExpected bool
	inForHeader       bool
	forHeaderDepth    int

	// attributeDepth: '[...]' attribute in statement-start position.
	// While > 0, all idents are swallowed (no decls/refs).
	attributeDepth int

	// namespaceFileScoped: a file-scoped namespace `namespace N;` was
	// declared, so we've already opened a namespace scope that runs to
	// EOF. No '{' will follow.
	namespaceFileScoped bool

	// pendingNamespaceDeclIdx / pendingNamespaceStartByte: set when we
	// emit a namespace decl and haven't yet decided whether it's block
	// or file-scoped. On ';' we open a ScopeNamespace covering the rest
	// of the file; on '{' the pendingScope machinery handles it.
	pendingNamespaceDeclIdx   int
	pendingNamespaceStartByte uint32

	// usingVarExpected: the previous `using` at statement start was
	// `using var x = ...` (C# 8+). Treat the following `var` like a
	// local var decl.
	usingVarExpected bool

	// Class-supertype tracking — populates Decl.SuperTypes for class
	// / interface decls so the cross-file hierarchy walker
	// (EmitOverrideSpans) can walk both directions.
	//
	// pendingClassDeclIdx holds the just-emitted class/interface
	// decl until its body `{` opens. inSuperTypes is on between the
	// inheritance `:` and the body `{` (or a `where` constraint
	// keyword). supertypeAngleDepth tracks generic args inside the
	// supertype clause (`: Generic<T, U>`); the existing
	// inGenericParams machinery only covers decl-name generics.
	// classBaseLastIdent uses "last ident wins" so qualified names
	// like `Foo.Bar.Base` keep the leaf segment.
	//
	// Partial classes: each per-file declaration of `partial class
	// Foo : Base` records its own SuperTypes on its own Decl. The
	// hierarchy walker iterates all decls in candidate files, so
	// every partial contributes regardless of which file declared
	// the base list.
	pendingClassDeclIdx int
	inSuperTypes        bool
	supertypeAngleDepth int
	classBaseLastIdent  string
	classSuperTypes     map[int][]string
}

type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind
}

func (b *builder) run() {
	// Skip a UTF-8 byte-order mark at the start of the file. C# source is
	// commonly saved with a BOM (Roslyn itself emits ~14K files with it);
	// without this skip, the BOM's three bytes (0xEF 0xBB 0xBF) leak into
	// the first token as an invisible "ident" that resolves to nothing.
	if len(b.s.Src) >= 3 && b.s.Src[0] == 0xEF && b.s.Src[1] == 0xBB && b.s.Src[2] == 0xBF {
		b.s.Pos = 3
	}
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
			// C# verbatim strings @"..." and interpolated $"..." exist;
			// for v1 treat all as simple strings. The lexkit helper
			// handles backslash escapes which is close enough for most
			// code; verbatim-string quote doubling ("") may still be
			// handled incorrectly but produces no false decls.
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
			if b.isUsingDecl {
				b.flushUsingDecl()
			}
			// File-scoped namespace: `namespace A.B;` — open a namespace
			// scope here that runs to EOF. We clear pendingScope so the
			// next '{' (a class/struct) doesn't consume the namespace-kind
			// pending scope.
			if b.pendingNamespaceDeclIdx >= 0 && !b.namespaceFileScoped {
				b.pendingScope = nil
				b.pendingOwnerDecl = b.pendingNamespaceDeclIdx
				b.openScope(scope.ScopeNamespace, b.pendingNamespaceStartByte)
				b.namespaceFileScoped = true
				b.pendingNamespaceDeclIdx = -1
			}
			// Namespace header consumed (file-scoped form or stray
			// ';'): stop extending the namespace's Signature.
			b.namespacePathDeclIdx = -1
			b.stmtStart = true
			b.prevByte = ';'
			b.declContext = ""
			b.localVarDeclKind = ""
			b.typePositionIdent = false
			b.paramListPending = false
			b.genericParamsExpected = false
			b.recordPrimaryCtor = false
			// Statement boundary: drop any leftover `public` that
			// didn't land on a decl.
			b.pendingPublic = false
			// If we had a record with no body (`record Point(int X, int Y);`),
			// flush pending record fields into the current scope.
			if len(b.pendingRecordFields) > 0 {
				for _, f := range b.pendingRecordFields {
					b.emitDecl(f.name, scope.KindField, f.span)
				}
				b.pendingRecordFields = nil
				b.recordOwnerDeclIdx = -1
			}
			b.pendingParams = nil
			b.pendingGenerics = nil
			b.forHeaderExpected = false
			b.foreachHeaderExpected = false
			b.usingVarExpected = false
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
			} else if b.recordPrimaryCtor {
				b.recordPrimaryCtor = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.foreachHeaderExpected {
				b.foreachHeaderExpected = false
				b.inForeachHeader = true
				b.foreachHeaderDepth = 1
				b.stmtStart = true
			} else if b.inForeachHeader {
				b.foreachHeaderDepth++
			} else if b.forHeaderExpected {
				b.forHeaderExpected = false
				b.inForHeader = true
				b.forHeaderDepth = 1
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
					// If this was a record primary ctor, migrate pending
					// params (captured as KindParam) into pendingRecordFields.
					if b.recordOwnerDeclIdx >= 0 && len(b.pendingParams) > 0 {
						for _, p := range b.pendingParams {
							p.kind = scope.KindField
							b.pendingRecordFields = append(b.pendingRecordFields, p)
						}
						b.pendingParams = nil
					}
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
			if b.inForeachHeader {
				b.foreachHeaderDepth--
				if b.foreachHeaderDepth == 0 {
					b.inForeachHeader = false
					b.typePositionIdent = false
					b.localVarDeclKind = ""
				}
			}
		case c == '[':
			// Attribute at stmt-start: `[Attr(...)]` before a decl.
			if b.stmtStart && b.attributeDepth == 0 {
				b.attributeDepth = 1
				b.s.Pos++
				b.prevByte = '['
				continue
			}
			if b.attributeDepth > 0 {
				b.attributeDepth++
				b.s.Pos++
				b.prevByte = '['
				continue
			}
			b.s.Pos++
			b.prevByte = '['
			b.parenVarStack = append(b.parenVarStack, b.localVarDeclKind)
			if b.inParamList {
				b.paramDepth++
			}
		case c == ']':
			if b.attributeDepth > 0 {
				b.attributeDepth--
				b.s.Pos++
				b.prevByte = ']'
				if b.attributeDepth == 0 {
					// Attribute consumed; re-enter stmt start for the
					// decl that follows.
					b.stmtStart = true
				}
				continue
			}
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
				b.typePositionIdent = false
			}
			if b.inGenericParams && b.genericDepth == 1 {
				b.genericSectionNeedsName = true
			}
			if b.localVarDeclKind != "" && !b.inParamList && !b.inGenericParams {
				b.typePositionIdent = true
			}
			// Supertype list separator (`: A, B, C`): flush the
			// captured name and reset for the next section. Skip
			// when inside generic args (`: Generic<T, U>`).
			if b.inSuperTypes && b.supertypeAngleDepth == 0 {
				b.flushSuperTypeArg()
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
			// Generic args inside a supertype clause
			// (`: Dictionary<K, V>`). The decl-name generic
			// machinery doesn't apply here; we keep our own
			// depth so the inner `,` doesn't flush.
			if b.inSuperTypes {
				b.supertypeAngleDepth++
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
			if b.inSuperTypes && b.supertypeAngleDepth > 0 {
				b.supertypeAngleDepth--
				b.s.Pos++
				b.prevByte = '>'
				continue
			}
			b.s.Pos++
			b.prevByte = '>'
		case c == '=' && b.s.PeekAt(1) == '>':
			// Lambda / expression-bodied member arrow. We only open a
			// function scope for true lambdas — when we have pending
			// params (bare ident or paren-list) that need a body scope.
			// Expression-bodied members (`public int X => _x;`) do NOT
			// open a scope; the expression is parsed at the enclosing
			// scope and terminated by ';'.
			arrowStart := uint32(b.s.Pos)
			b.s.Advance(2)
			b.prevByte = '>'
			if len(b.pendingParams) > 0 {
				k := scope.ScopeFunction
				b.pendingScope = &k
				if b.pendingFullStart == 0 {
					b.pendingFullStart = arrowStart + 1
				}
			}
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == ':':
			b.s.Pos++
			b.prevByte = ':'
			// Inheritance `:` after a class/interface header. We
			// detect it by pendingClassDeclIdx still set (cleared
			// on body '{'); other `:` uses (constructor base call,
			// labeled statement) happen inside a body where this is
			// false.
			if b.pendingClassDeclIdx >= 0 && !b.inSuperTypes {
				b.inSuperTypes = true
				b.classBaseLastIdent = ""
				b.supertypeAngleDepth = 0
			}
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' &&
					cc != 'x' && cc != 'X' && cc != 'e' && cc != 'E' &&
					cc != 'L' && cc != 'l' && cc != 'F' && cc != 'f' &&
					cc != 'D' && cc != 'd' && cc != 'M' && cc != 'm' &&
					cc != 'U' && cc != 'u' {
					break
				}
				b.s.Pos++
			}
			b.stmtStart = false
			b.prevByte = '0'
		case c == '#':
			// Preprocessor directive: `#nullable`, `#pragma`, `#if`, `#region`,
			// `#define`, etc. C# directives are line-based (no continuation
			// except inside a comment); consume to end-of-line. Without this,
			// directive body idents (e.g. `nullable disable`, `warning CS0618`)
			// leak as unresolved refs.
			for !b.s.EOF() && b.s.Peek() != '\n' {
				b.s.Pos++
			}
			b.prevByte = '#'
		case c == '@':
			// `@ident` is a C# verbatim identifier (e.g. `@class`).
			// Skip the '@' and let the following ident scan normally.
			b.s.Pos++
			b.prevByte = '@'
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

// flushUsingDecl handles the semicolon at the end of a `using ...;`.
// Emits a KindImport decl and stamps Signature for the import-graph
// resolver — see the builder struct's isUsingDecl comment for the
// encoding table.
func (b *builder) flushUsingDecl() {
	modPath := string(b.usingPathBuf)
	trailing := string(b.usingBuf)

	if b.usingIsAlias {
		if len(b.usingAliasName) > 0 {
			// `using X = A.B.C;` -> Signature="A.B\x00C" Name="X".
			// `using X = Foo;`   -> Signature="\x00Foo"  Name="X".
			idx := len(b.res.Decls)
			b.emitDecl(string(b.usingAliasName), scope.KindImport, b.usingAliasSpan)
			if idx < len(b.res.Decls) {
				orig := trailing
				if orig == "" {
					orig = "*"
				}
				b.res.Decls[idx].Signature = modPath + "\x00" + orig
			}
		}
	} else if len(b.usingBuf) > 0 {
		// Namespace widening: `using Foo.Bar;` ->
		// Signature="Foo.Bar\x00*". `using static Foo.Util;` ->
		// Signature="static:Foo.Util\x00*".
		full := trailing
		if modPath != "" {
			full = modPath + "." + trailing
		}
		idx := len(b.res.Decls)
		b.emitDecl(trailing, scope.KindImport, b.usingBufSpan)
		if idx < len(b.res.Decls) {
			sigPath := full
			if b.usingStaticFlag {
				sigPath = "static:" + full
			}
			b.res.Decls[idx].Signature = sigPath + "\x00*"
		}
	}
	b.isUsingDecl = false
	b.usingStaticFlag = false
	b.usingIsAlias = false
	if b.usingAliasName != nil {
		b.usingAliasName = b.usingAliasName[:0]
	}
	if b.usingBuf != nil {
		b.usingBuf = b.usingBuf[:0]
	}
	if b.usingPathBuf != nil {
		b.usingPathBuf = b.usingPathBuf[:0]
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

	// Attribute body: drop all idents.
	if b.attributeDepth > 0 {
		b.prevByte = 'i'
		return
	}

	// Using declaration: collect final unqualified name + dotted
	// prefix, handling the `using Alias = Target;` form. On each
	// ident after a '.', promote the previous usingBuf contents
	// into usingPathBuf so the trailing segment lives in usingBuf
	// and the prefix in usingPathBuf. flushUsingDecl reads both to
	// build Signature.
	if b.isUsingDecl {
		if name == "static" && !b.usingStaticFlag && len(b.usingBuf) == 0 && !b.usingIsAlias {
			b.usingStaticFlag = true
			b.prevByte = 'k'
			return
		}
		// Peek ahead: if the next non-ws byte is '=', this ident is
		// the alias LHS of `using Alias = Target;`. The alias ident
		// must be the first thing we see after `using` (and any
		// `static` flag), so both buffers are empty.
		if !b.usingIsAlias && len(b.usingBuf) == 0 && len(b.usingPathBuf) == 0 {
			if b.peekNonWSByte() == '=' {
				b.usingIsAlias = true
				b.usingAliasName = append(b.usingAliasName[:0], word...)
				b.usingAliasSpan = mkSpan(startByte, endByte)
				b.prevByte = 'i'
				return
			}
		}
		// Continuing a dotted name: promote old trailing segment
		// into the prefix, joined by '.'.
		if len(b.usingBuf) > 0 && b.prevByte == '.' {
			if len(b.usingPathBuf) > 0 {
				b.usingPathBuf = append(b.usingPathBuf, '.')
			}
			b.usingPathBuf = append(b.usingPathBuf, b.usingBuf...)
		}
		b.usingBuf = append(b.usingBuf[:0], word...)
		b.usingBufSpan = mkSpan(startByte, endByte)
		b.prevByte = 'i'
		return
	}

	// Keywords that change parser state.
	switch name {
	case "using":
		// `using <namespace>;` or `using Alias = X;` or `using static X;`
		// at statement start -> import decl.
		// `using var x = ...;` or `using (resource)` -> statement form.
		if wasStmtStart {
			// Peek: if the token after whitespace is `var`, it's a
			// using-statement with local var decl.
			save := b.s.Pos
			saveLine := b.s.Line
			b.skipWS()
			isUsingVar := false
			if !b.s.EOF() {
				// Read an ident peek.
				if lexkit.DefaultIdentStart[b.s.Peek()] {
					end := b.s.Pos
					for end < len(b.s.Src) && lexkit.DefaultIdentCont[b.s.Src[end]] {
						end++
					}
					if string(b.s.Src[b.s.Pos:end]) == "var" {
						isUsingVar = true
					}
				} else if b.s.Peek() == '(' {
					// `using (resource) { ... }` — treat as a statement.
					isUsingVar = true
				}
			}
			b.s.Pos = save
			b.s.Line = saveLine
			if !isUsingVar {
				b.isUsingDecl = true
				b.usingStaticFlag = false
				b.usingIsAlias = false
				if b.usingBuf != nil {
					b.usingBuf = b.usingBuf[:0]
				}
				if b.usingAliasName != nil {
					b.usingAliasName = b.usingAliasName[:0]
				}
				b.prevByte = 'k'
				return
			}
			// using-statement. Fall through to treat as keyword that
			// preserves stmtStart so a following `var` acts as a var decl.
			b.usingVarExpected = true
			b.stmtStart = true
			b.prevByte = 'k'
			return
		}
		b.prevByte = 'k'
		return
	case "namespace":
		b.declContext = scope.KindNamespace
		k := scope.ScopeNamespace
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
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
	case "struct":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
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
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeClass
		b.pendingScope = &k
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "public", "private", "protected", "internal", "static", "readonly",
		"const", "sealed", "abstract", "virtual", "override", "async",
		"partial", "unsafe", "extern", "volatile", "required", "file":
		// Track `public` so the next-emitted top-level type decl
		// gets Exported=true. Phase-1 approximation: only `public`
		// counts — `internal` is assembly-wide in C# but without
		// assembly analysis the conservative-correct answer at
		// repo scope is to leave it unexported, and cross-file
		// refs to internal types continue to resolve via the
		// per-file scope walk (which sees the decl directly).
		if name == "public" {
			b.pendingPublic = true
		}
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "new":
		// `new` can be a modifier at stmt-start, or a constructor-call
		// operator in expression position. Preserve stmtStart either
		// way so a following decl keyword still classifies.
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "ref", "out", "params":
		// Parameter modifier keywords. Preserve context.
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "in":
		// `in` is a parameter-modifier keyword, a foreach keyword,
		// and a generic variance marker. In foreach header context we
		// track it explicitly; otherwise just a keyword.
		b.prevByte = 'k'
		return
	case "this":
		b.prevIdentIsThis = true
		b.prevIdentIsBase = false
		b.prevByte = 'k'
		return
	case "base":
		b.prevIdentIsBase = true
		b.prevIdentIsThis = false
		b.prevByte = 'k'
		return
	case "return", "if", "else", "while", "do", "switch", "case",
		"break", "continue", "throw", "try", "catch", "finally",
		"is", "as", "yield", "goto", "default", "checked", "unchecked",
		"lock", "await", "when", "global":
		b.prevByte = 'k'
		return
	case "for":
		b.forHeaderExpected = true
		b.prevByte = 'k'
		return
	case "foreach":
		b.foreachHeaderExpected = true
		b.prevByte = 'k'
		return
	case "var":
		if wasStmtStart || b.inForHeader || b.inForeachHeader || b.usingVarExpected {
			b.typePositionIdent = true
			b.localVarDeclKind = scope.KindVar
			b.usingVarExpected = false
			b.prevByte = 'k'
			return
		}
	case "void":
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "true", "false", "null":
		b.prevByte = 'k'
		return
	case "where":
		// `where T : new()` generic constraint clause terminates the
		// supertype section. Flush whatever's captured so far; the
		// constraint idents that follow are not supertypes.
		if b.inSuperTypes {
			b.flushSuperTypeSection()
		}
		b.prevByte = 'k'
		return
	}

	// Property access after '.'.
	if b.prevByte == '.' {
		if b.prevIdentIsThis || b.prevIdentIsBase {
			b.prevIdentIsThis = false
			b.prevIdentIsBase = false
			if b.tryResolveThisField(name, mkSpan(startByte, endByte)) {
				b.prevByte = 'i'
				return
			}
		}
		// Namespace header continuation: `namespace A.B.C` — extend
		// the pending namespace decl's Signature with each dotted
		// segment. Gated by namespacePathDeclIdx so we only do this
		// until the namespace's '{' or ';' closes the header.
		if b.namespacePathDeclIdx >= 0 &&
			b.pendingNamespaceDeclIdx == b.namespacePathDeclIdx {
			b.namespacePathBuf = append(b.namespacePathBuf, '.')
			b.namespacePathBuf = append(b.namespacePathBuf, word...)
			b.res.Decls[b.namespacePathDeclIdx].Signature = string(b.namespacePathBuf)
			b.prevByte = 'i'
			return
		}
		// Qualified supertype name: `: System.Collections.List` —
		// each segment after a `.` overwrites classBaseLastIdent so
		// the leaf wins. The `.` handler doesn't update prevByte to
		// 'i' until we set it below, so this branch is the only
		// place dotted segments hit the supertype capture.
		if b.inSuperTypes && b.supertypeAngleDepth == 0 {
			b.classBaseLastIdent = name
		}
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Clear this/base markers on any non-chained ident.
	b.prevIdentIsThis = false
	b.prevIdentIsBase = false

	// Generic type-param list: first ident per section becomes a pending
	// type decl.
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

	// Param list at top depth: alternate type / name.
	if b.inParamList && b.paramDepth == 1 {
		if b.paramSectionNeedsName {
			b.emitRef(name, mkSpan(startByte, endByte))
			b.paramSectionNeedsName = false
			b.typePositionIdent = true
			b.prevByte = 'i'
			return
		}
		if b.typePositionIdent {
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: name,
				span: mkSpan(startByte, endByte),
				kind: scope.KindParam,
			})
			b.typePositionIdent = false
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// declContext set (class/interface/struct/record/enum/namespace name follows).
	if b.declContext != "" {
		kind := b.declContext
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		// Namespace: remember the decl index + start byte so the
		// ';' handler can open a file-scoped namespace scope if we
		// reach ';' before we see '{'. Dotted names `namespace A.B;`
		// consume intermediate idents; we intercept them below (in
		// the `prevByte == '.'` branch) to extend the namespace's
		// Signature rather than emitting property_access refs.
		if kind == scope.KindNamespace {
			b.pendingNamespaceDeclIdx = len(b.res.Decls) - 1
			b.pendingNamespaceStartByte = startByte
			b.namespacePathDeclIdx = len(b.res.Decls) - 1
			b.namespacePathBuf = append(b.namespacePathBuf[:0], word...)
			// Stamp initial single-component FQN on the decl.
			// Overwritten if more dotted components follow.
			b.res.Decls[b.namespacePathDeclIdx].Signature = string(b.namespacePathBuf)
		}
		// Record / struct primary constructor: `record Foo(...)` — the
		// next `(` begins a field list.
		if kind == scope.KindClass {
			if b.peekNonWSByte() == '(' {
				b.recordPrimaryCtor = true
				b.recordOwnerDeclIdx = len(b.res.Decls) - 1
			}
		}
		// Track the class/interface so the upcoming `: Base, IA, IB`
		// inheritance clause appends supertype names onto its decl.
		// Cleared when the body `{` opens.
		if kind == scope.KindClass || kind == scope.KindInterface {
			b.pendingClassDeclIdx = len(b.res.Decls) - 1
		}
		b.declContext = ""
		b.genericParamsExpected = true
		b.prevByte = 'i'
		return
	}

	scopeK := b.currentScopeKind()

	// Class / interface / struct / record body.
	if scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface {
		nextCh := b.peekNonWSByte()

		if nextCh == '(' || nextCh == '<' {
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
		if nextCh == '{' || nextCh == '=' {
			// Property: `public int X { get; set; }` or `public int X => _x;`
			// or `public int X { get; } = 0;`. The next '{' (if any) is
			// an opaque property body that we treat as a plain block scope.
			if b.typePositionIdent {
				b.emitDecl(name, scope.KindField, mkSpan(startByte, endByte))
				b.typePositionIdent = false
				b.localVarDeclKind = scope.KindField
				b.prevByte = 'i'
				return
			}
			b.emitRef(name, mkSpan(startByte, endByte))
			b.typePositionIdent = true
			b.prevByte = 'i'
			return
		}
		if b.typePositionIdent {
			b.emitDecl(name, scope.KindField, mkSpan(startByte, endByte))
			b.typePositionIdent = false
			b.localVarDeclKind = scope.KindField
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, mkSpan(startByte, endByte))
		b.typePositionIdent = true
		b.prevByte = 'i'
		return
	}

	// Method body / function scope / foreach or for header.
	if scopeK == scope.ScopeFunction || b.inForHeader || b.inForeachHeader {
		if b.inForeachHeader {
			if name == "in" {
				b.typePositionIdent = false
				b.prevByte = 'k'
				return
			}
			if b.typePositionIdent {
				b.emitDecl(name, scope.KindVar, mkSpan(startByte, endByte))
				b.typePositionIdent = false
				b.prevByte = 'i'
				return
			}
			b.emitRef(name, mkSpan(startByte, endByte))
			b.typePositionIdent = true
			b.prevByte = 'i'
			return
		}
		if b.typePositionIdent {
			nextCh := b.peekNonWSByte()
			switch nextCh {
			case '=', ';', ',', ':':
				b.emitDecl(name, scope.KindVar, mkSpan(startByte, endByte))
				b.typePositionIdent = false
				b.localVarDeclKind = scope.KindVar
				b.prevByte = 'i'
				return
			case '(':
				b.typePositionIdent = false
				b.emitRef(name, mkSpan(startByte, endByte))
				b.prevByte = 'i'
				return
			case '[':
				b.emitRef(name, mkSpan(startByte, endByte))
				b.prevByte = 'i'
				return
			default:
				b.emitDecl(name, scope.KindVar, mkSpan(startByte, endByte))
				b.typePositionIdent = false
				b.localVarDeclKind = scope.KindVar
				b.prevByte = 'i'
				return
			}
		}
		if wasStmtStart {
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
			b.emitRef(name, mkSpan(startByte, endByte))
			b.prevByte = 'i'
			return
		}
	}

	// Bare-ident lambda `x => body`: only apply when a fat arrow
	// follows directly.
	if !b.inParamList && !b.inGenericParams && b.declContext == "" &&
		b.prevByte != '.' && b.peekFatArrow() {
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
	// Supertype name capture: while inside the inheritance clause
	// at depth 0 (outside `<...>` generic args), record the latest
	// ident. Qualified names `Foo.Bar.Base` reduce to the leaf via
	// "last ident wins".
	if b.inSuperTypes && b.supertypeAngleDepth == 0 {
		b.classBaseLastIdent = name
	}
	b.prevByte = 'i'
}

// flushSuperTypeArg appends the captured base/interface ident to
// classSuperTypes and resets the per-section state. Called on `,`
// inside the inheritance clause.
func (b *builder) flushSuperTypeArg() {
	if b.pendingClassDeclIdx < 0 || b.classBaseLastIdent == "" {
		b.classBaseLastIdent = ""
		return
	}
	b.classSuperTypes[b.pendingClassDeclIdx] = append(
		b.classSuperTypes[b.pendingClassDeclIdx], b.classBaseLastIdent)
	b.classBaseLastIdent = ""
}

// flushSuperTypeSection finalises the inheritance window and clears
// inSuperTypes. Called on body `{` and on the `where` constraint
// keyword that terminates the base list.
func (b *builder) flushSuperTypeSection() {
	b.flushSuperTypeArg()
	b.inSuperTypes = false
	b.supertypeAngleDepth = 0
}

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	b.prevByte = '{'
	b.stmtStart = true

	kind := scope.ScopeBlock
	if b.pendingScope != nil {
		kind = *b.pendingScope
		b.pendingScope = nil
		b.genericParamsExpected = false
	}
	// Class / interface body opens — flush the pending supertype
	// section onto Decl.SuperTypes and clear the trackers so
	// nothing leaks into the body.
	if b.inSuperTypes {
		b.flushSuperTypeSection()
	}
	if kind == scope.ScopeClass || kind == scope.ScopeInterface {
		b.pendingClassDeclIdx = -1
	}
	// Any pending namespace decl becomes a block namespace scope;
	// drop the pending marker so it isn't also opened on the next
	// ';', and stop extending the namespace's Signature.
	if kind == scope.ScopeNamespace {
		b.pendingNamespaceDeclIdx = -1
		b.namespacePathDeclIdx = -1
	}
	b.openScope(kind, uint32(b.s.Pos-1))
	// Flush generics into the newly opened scope.
	if kind == scope.ScopeClass || kind == scope.ScopeInterface ||
		kind == scope.ScopeFunction || kind == scope.ScopeNamespace {
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
	// Flush record primary-ctor fields into the class body scope.
	if kind == scope.ScopeClass && len(b.pendingRecordFields) > 0 {
		for _, f := range b.pendingRecordFields {
			b.emitDecl(f.name, scope.KindField, f.span)
		}
		b.pendingRecordFields = nil
		b.recordOwnerDeclIdx = -1
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

// peekFatArrow reports whether the next non-ws token is '=>'. Does not
// advance the scanner.
func (b *builder) peekFatArrow() bool {
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
	return b.s.Peek() == '=' && b.s.PeekAt(1) == '>'
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
	// File-scope decls hash with canonicalPath when set, so
	// cross-file references through imports/includes bind to
	// matching DeclIDs. Nested-scope decls keep the file path.
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

	// Exported flag. Phase-1 approximation: a top-level class /
	// interface / enum preceded by `public` is externally
	// referenceable. The resolver uses this to decide which decls
	// in a namespace are eligible for cross-file import rewrites.
	// Only top-level (file scope or directly-in-namespace) counts;
	// nested types like Outer.Inner are addressed via property
	// access, not imports.
	if b.pendingPublic {
		switch kind {
		case scope.KindClass, scope.KindInterface, scope.KindEnum:
			sk := b.currentScopeKind()
			if sk == scope.ScopeFile || sk == scope.ScopeNamespace {
				b.res.Decls[idx].Exported = true
			}
		}
	}
	// Clear pendingPublic once a non-namespace decl consumes it so
	// it doesn't leak. KindNamespace is preserved (defensive — the
	// normal pattern is modifiers preceding an inner decl, not the
	// namespace itself).
	if kind != scope.KindNamespace {
		b.pendingPublic = false
	}

	switch kind {
	case scope.KindClass, scope.KindInterface, scope.KindEnum,
		scope.KindMethod, scope.KindFunction, scope.KindNamespace:
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

// tryResolveThisField attempts to resolve `this.name` or `base.name`
// at `span` against the nearest enclosing class / interface / struct /
// record NSField decls. Mirrors the TS pattern.
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
// back to signature-position generics then unresolved.
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
			if builtins.CSharp.Has(r.Name) {
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

func hashBuiltinDecl(name string) scope.DeclID {
	h := sha256.New()
	h.Write([]byte("<builtin:csharp>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
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
