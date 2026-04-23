// Package ts is the TypeScript/JavaScript scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single file.
// Handles file / function / block / class / namespace scopes and
// var/let/const/function/class/interface/type/import/param declarations.
// Identifiers not in declaration position are emitted as Refs and
// resolved via scope-chain walk to the innermost matching Decl.
//
// v1 limitations (to be relaxed):
//   - Type-vs-value namespace split (v2): interfaces and type aliases
//     emit into NSType only; classes and enums dual-emit (NSValue +
//     NSType) so `new Foo()` and `let f: Foo` resolve to the same ID
//     via within-file merge. Refs are tagged NSType when the builder
//     detects a type annotation (`:` in param/var/return position) or
//     a generic `<...>` list. Ref tagging is heuristic — keyword-
//     triggered contexts (`extends`, `as`, `satisfies`, `keyof`) are
//     NOT yet tracked, so refs in those positions stay NSValue and
//     rely on resolveRefs' NSValue→NSType fallback to find types.
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
	"strings"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a TypeScript/JavaScript source buffer.
// file is the canonical file path used to stamp Decl.File and Ref.File;
// pass the same path the caller will use when querying.
// Parse extracts a scope.Result from a TS/JS source buffer.
// File-scope decls hash with the file path.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path for
// file-scope DeclID hashing. When canonicalPath is non-empty,
// file-scope decls hash with it instead of the file path — so an
// exported `function foo` in a/b.ts and `import { foo } from '../a/b'`
// in c.ts bind to the same DeclID.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:             file,
		canonicalPath:    canonicalPath,
		res:              &scope.Result{File: file},
		s:                lexkit.New(src),
		pendingOwnerDecl: -1,
	}
	// Enable JSX parsing for .jsx/.tsx. Plain .ts/.js never has JSX;
	// in those, `<` means generic param or less-than, never element.
	if strings.HasSuffix(file, ".tsx") || strings.HasSuffix(file, ".jsx") {
		b.jsxEnabled = true
	}
	b.openScope(scope.ScopeFile, 0)
	b.regexOK = true
	b.stmtStart = true
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	scope.MergeDuplicateDecls(b.res)
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
	// ownerDeclIdx is the index in res.Decls of the decl that owns this
	// scope. closeTopScope patches FullSpan.EndByte on that decl. -1 if
	// the scope was not introduced by a decl (block scope, etc.).
	ownerDeclIdx int
}

type builder struct {
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner



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

	// prevIdentIsThis is true when the most recently scanned identifier
	// was the keyword 'this'. Used in combination with prevByte == '.'
	// to resolve 'this.X' property accesses against the enclosing class
	// or interface's NSField decls. Cleared by any non-'this' ident.
	prevIdentIsThis bool

	// jsxEnabled is true for .tsx/.jsx files: '<' in expression position
	// may start a JSX element. False for plain .ts/.js where '<' is
	// always generic-param or less-than.
	jsxEnabled bool

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

	// typePosition is true when the next ident refers to a type. Entered
	// on `:` in param/var/return annotations and while inside a generic
	// `<...>` list. Cleared on `=`, `;`, `{` (value body), `=>`, `,` at
	// param-list top level, and `)` closing an annotation context. Refs
	// emitted while true are tagged NSType; the resolveRefs fallback
	// still catches mistagged cases.
	typePosition bool

	// pendingFullStart captures the byte position of the most recent
	// declaration keyword (function, class, interface, type, enum,
	// namespace, const, let, var). emitDecl uses it as FullSpan.StartByte.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last scope-owning
	// decl. Consumed by the next openScope; closeTopScope patches
	// FullSpan.EndByte. -1 when none.
	pendingOwnerDecl int

	// pendingExport is true when the current statement is prefixed with
	// `export` (`export class Foo`, `export function bar`, `export const x`,
	// `export interface I`, `export type T = ...`, `export default ...`, etc.).
	// Consumed by emitDecl to set Decl.Exported = true on the emitted decl,
	// then cleared. Also cleared on statement boundaries ('}', ';', file end)
	// so a stray `export` keyword doesn't contaminate later decls.
	pendingExport bool

	// arrowExprStack tracks open expression-body arrow function scopes.
	// Each entry records the parenVarStack depth at the moment the arrow
	// opened, so we know which terminator (`,`, `;`, `)`, `]`, `}`, or
	// a statement-start decl keyword) should close it. Block-body arrows
	// `(x) => { ... }` use the regular '{' → '}' scope machinery and do
	// NOT appear on this stack.
	arrowExprStack []arrowExprEntry
}

// arrowExprEntry records the ambient state at the moment an expression-
// body arrow function scope was pushed, so the close logic can match
// the correct terminator.
type arrowExprEntry struct {
	// parenDepth is len(parenVarStack) at the time the arrow opened.
	// The arrow's expression body closes when we see a terminator at
	// this same paren/bracket depth (i.e., the ambient paren context
	// the arrow was written inside).
	parenDepth int
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
		case c == ':':
			// Type annotation entry. Trigger in three contexts:
			//   - param annotation: `(x: T)` — inParamList && paramDepth 1
			//   - var annotation: `let x: T` — declContext is var/let/const
			//   - return-type annotation: `function f(): T` — prevByte is ')'
			// Object-literal keys (`{ x: 1 }`) and ternary arms hit this
			// case too but don't match any of the above, so they stay value.
			b.s.Pos++
			if (b.inParamList && b.paramDepth == 1) ||
				b.varDeclKind == scope.KindVar || b.varDeclKind == scope.KindLet || b.varDeclKind == scope.KindConst ||
				b.prevByte == ')' {
				b.typePosition = true
			}
			b.prevByte = ':'
			b.regexOK = true
		case c == ';':
			// Close any expression-body arrow whose scope terminates at
			// this statement boundary before processing the ';' itself.
			b.closeArrowExprIfTerminating(uint32(b.s.Pos))
			b.s.Pos++
			b.stmtStart = true
			b.regexOK = true
			b.declContext = ""
			b.varDeclKind = ""
			b.typePosition = false
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
		case c == '=' && b.s.PeekAt(1) != '=' && b.s.PeekAt(1) != '>':
			// Plain `=`: initializer for a var decl, class-field default,
			// or assignment. Exits a type annotation (RHS is value).
			// Exception: type-alias RHS `type X = RHS` — keep type context.
			b.s.Pos++
			b.prevByte = '='
			b.regexOK = true
			if b.declContext == scope.KindType {
				b.typePosition = true
			} else {
				b.typePosition = false
			}
		case c == '=' && b.s.PeekAt(1) == '>':
			arrowStart := uint32(b.s.Pos)
			b.s.Advance(2)
			save := b.s.Pos
			saveLine := b.s.Line
			b.skipWS()
			if !b.s.EOF() && b.s.Peek() == '{' {
				// Block-body arrow `(x) => { ... }`: defer scope push to
				// the '{' handler via pendingScope, which also flushes
				// pendingParams into the new function scope.
				k := scope.ScopeFunction
				b.pendingScope = &k
				b.typePosition = false
			} else {
				b.s.Pos = save
				b.s.Line = saveLine
				b.typePosition = false
				// Expression-body arrow `(x) => x + 1`: open a function
				// scope now so params bind inside and body refs resolve
				// through it. Closed on the next terminator (`,`, `;`,
				// `)`, `]`, `}`, or a stmt-start decl keyword) at the
				// matching paren/bracket depth.
				b.openScope(scope.ScopeFunction, arrowStart)
				b.arrowExprStack = append(b.arrowExprStack, arrowExprEntry{
					parenDepth: len(b.parenVarStack),
				})
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
			}
			b.regexOK = true
			b.prevByte = '>'
		case c == '(':
			b.s.Pos++
			b.regexOK = true
			b.prevByte = '('
			b.parenVarStack = append(b.parenVarStack, b.varDeclKind)
			b.varDeclKind = ""
			// Entering a `(`: param names are value-position. Annotation
			// re-entry happens via the `:` handler.
			b.typePosition = false
			if b.paramListPending {
				b.paramListPending = false
				// Seeing '(' confirms we're in a param list, not a generic
				// decl. The '<' for generics comes before '('; if we got
				// here without it, there's no generic decl — clear the flag.
				b.genericParamsExpected = false
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
			// Expression-body arrows opened inside the enclosing paren
			// context end at this ')'. Close before popping parenVarStack
			// so the arrow's entry depth still matches.
			b.closeArrowExprIfTerminating(uint32(b.s.Pos))
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
			// Expression-body arrows separated from siblings by ','
			// (e.g. `[x => x, y => y]`) end at this comma.
			b.closeArrowExprIfTerminating(uint32(b.s.Pos))
			b.s.Pos++
			b.regexOK = true
			b.prevByte = ','
			// In a param list at top depth, the next section starts with a
			// param name (value position). Inside generics, commas separate
			// type params and values stay in type context (handled by
			// inGenericParams in emitRef).
			if b.inParamList && b.paramDepth == 1 {
				b.typePosition = false
			}
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
			// type/method decl name.
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
				b.s.Pos++
				b.regexOK = true
				b.prevByte = '<'
				continue
			}
			// JSX element start: in .tsx/.jsx files, '<' in expression
			// position followed by a letter, '/', or '>' starts a JSX
			// element. Consume the whole element (with embedded {...}
			// recursively parsed as JS expressions).
			if b.jsxEnabled && looksLikeJSXStart(b) {
				b.handleJSXElement()
				continue
			}
			// Otherwise: less-than or type-position angle (we don't decode).
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
			// Expression-body arrows inside an array/bracket end at ']'.
			b.closeArrowExprIfTerminating(uint32(b.s.Pos))
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

	// Expression-body arrow without a trailing `;` terminates when a
	// new statement-level decl keyword appears (ASI-style). Close here
	// so the new decl lands in the enclosing scope, not in the arrow.
	if wasStmtStart && len(b.arrowExprStack) > 0 {
		switch name {
		case "const", "let", "var", "function", "class", "interface",
			"type", "enum", "namespace", "module", "import", "export",
			"return", "if", "for", "while", "do", "switch", "throw",
			"try", "break", "continue":
			b.closeArrowExprIfTerminating(startByte)
		}
	}

	// Keywords that change parser state.
	switch name {
	case "function":
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "class":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "interface":
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "type":
		if wasStmtStart || b.prevByte == ';' || b.prevByte == '{' || b.prevByte == '}' {
			b.declContext = scope.KindType
			b.pendingFullStart = startByte + 1
		}
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeBlock
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "namespace", "module":
		b.declContext = scope.KindNamespace
		k := scope.ScopeNamespace
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "const":
		b.declContext = scope.KindConst
		b.varDeclKind = scope.KindConst
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "let":
		b.declContext = scope.KindLet
		b.varDeclKind = scope.KindLet
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "var":
		b.declContext = scope.KindVar
		b.varDeclKind = scope.KindVar
		b.pendingFullStart = startByte + 1
		b.regexOK = false
		b.prevByte = 'd'
		return
	case "import":
		b.handleImport()
		return
	case "export":
		// Module-level decl prefix: remember that the next emitted decl
		// is exported, but don't alter the stmt-start state so a following
		// decl keyword (type, class, function, etc.) still sees a statement
		// boundary and activates. Consumed by emitDecl; cleared on emit.
		b.pendingExport = true
		b.stmtStart = wasStmtStart
		b.prevByte = 'k'
		return
	case "default", "declare":
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
		// Track 'this' so the next property access (this.X) can resolve
		// against the enclosing class's field/method decls.
		b.prevIdentIsThis = name == "this"
		b.prevByte = 'k'
		return
	}

	if b.prevByte == '.' {
		// Property access `x.Name`: normally we can't resolve Name via
		// scope chain (we don't know the receiver type), but if the
		// receiver is `this` we can bind against the enclosing class's
		// NSField decls directly.
		if b.prevIdentIsThis {
			b.prevIdentIsThis = false
			if b.tryResolveThisField(name, mkSpan(startByte, endByte)) {
				b.regexOK = false
				b.prevByte = 'i'
				return
			}
		}
		// Otherwise: emit as a probable ref with Reason=property_access
		// so refs-to queries on method/field decls can pick it up by
		// name-matching across files. Imprecise — matches any same-named
		// method on any object — but surfaces the references users
		// actually want.
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	// Any non-'this' identifier past this point clears the this marker
	// so a later 'foo.bar' doesn't mis-resolve against enclosing fields.
	b.prevIdentIsThis = false

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

	// Bare-ident arrow param: `x => body`. If the next non-ws/comment is
	// '=>' and we're not in a context that would otherwise consume this
	// ident (param list, destructuring, generics, declContext), stash it
	// as a pending param instead of emitting a ref; the '=>' handler will
	// open the arrow scope and flush it as a param decl.
	if !b.inParamList && !b.inDestructuring && !b.inGenericParams &&
		b.declContext == "" && b.prevByte != '.' && b.peekArrowAfterIdent() {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindParam,
		})
		b.regexOK = false
		b.prevByte = 'i'
		return
	}

	b.emitRef(name, mkSpan(startByte, endByte))
	b.regexOK = false
	b.prevByte = 'i'
}

// peekArrowAfterIdent reports whether the bytes following the current
// scanner position (which has already consumed an identifier) are
// whitespace/comments then '=>'. Used to detect bare-ident arrows like
// `x => body`. Does not mutate scanner position.
func (b *builder) peekArrowAfterIdent() bool {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() {
		b.s.Pos = save
		b.s.Line = saveLine
	}()
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
		return c == '=' && b.s.PeekAt(1) == '>'
	}
	return false
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
	// Track decl indexes we emit here + their "original name" so we can
	// back-fill Signature = "<modulePath>\x00<originalName>" after the
	// 'from <path>' string is scanned. The import graph (store/imports.go)
	// consumes this field to resolve cross-file refs.
	type pendingImport struct {
		declIdx  int
		origName string
	}
	var pending []pendingImport
	recordImport := func(origName string) {
		if len(b.res.Decls) == 0 {
			return
		}
		pending = append(pending, pendingImport{
			declIdx:  len(b.res.Decls) - 1,
			origName: origName,
		})
	}

	// Default import: `import foo from '...'`
	if lexkit.IsDefaultIdentStart(b.s.Peek()) {
		word := b.s.ScanIdentTable(&identStart, &identCont)
		if len(word) > 0 {
			start := uint32(b.s.Pos - len(word))
			end := uint32(b.s.Pos)
			b.emitDecl(string(word), scope.KindImport, mkSpan(start, end))
			recordImport("default")
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
				recordImport("*")
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
					recordImport(imported)
				}
			} else {
				b.emitDecl(imported, scope.KindImport, mkSpan(importedStart, importedEnd))
				recordImport(imported)
			}
			b.skipWS()
			if b.s.Peek() == ',' {
				b.s.Pos++
			}
		}
	}
	// Scan for `from '<path>'` (skip whitespace, optional "from" word,
	// then string literal). If any piece is missing, leave pending decls
	// without Signature — the resolver treats them as unresolved/external.
	b.skipWS()
	if b.peekIdentWord("from") {
		b.s.Advance(4)
		b.skipWS()
	}
	modulePath := b.scanImportString()
	if modulePath != "" {
		for _, pi := range pending {
			if pi.declIdx < 0 || pi.declIdx >= len(b.res.Decls) {
				continue
			}
			b.res.Decls[pi.declIdx].Signature = modulePath + "\x00" + pi.origName
		}
	}
}

// scanImportString reads a single or double-quoted string literal at the
// current position and returns its unescaped contents. Returns "" if the
// current byte is not a quote or the string is malformed. The scanner is
// advanced past the closing quote on success.
func (b *builder) scanImportString() string {
	q := b.s.Peek()
	if q != '\'' && q != '"' {
		return ""
	}
	b.s.Pos++
	start := b.s.Pos
	// Module specifiers in TS/JS don't contain escape sequences we care
	// about (no newlines inside them). Scan byte-wise until matching quote.
	src := b.s.Src
	for b.s.Pos < len(src) {
		c := src[b.s.Pos]
		if c == q {
			path := string(src[start:b.s.Pos])
			b.s.Pos++
			return path
		}
		if c == '\\' && b.s.Pos+1 < len(src) {
			b.s.Pos += 2
			continue
		}
		if c == '\n' {
			return ""
		}
		b.s.Pos++
	}
	return ""
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
		// Opening a named body (function/class/interface/namespace) means
		// we're past the decl-name-to-'<' window. Clear genericParamsExpected
		// ONLY in that case — generic constraints like `<T extends { k }>`
		// also contain '{' but those are type-position, and we haven't
		// consumed the '<' yet; clearing there would break them.
		b.genericParamsExpected = false
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
	// Expression-body arrow whose body nests inside `{...}` ends at
	// this '}'. Close before advancing so the scope ends before the
	// brace that closes the enclosing block.
	b.closeArrowExprIfTerminating(uint32(b.s.Pos))
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
	owner := b.pendingOwnerDecl
	b.pendingOwnerDecl = -1
	b.stack.Push(lexkit.Scope[scopeEntry]{
		Data: scopeEntry{
			kind:             kind,
			id:               id,
			savedVarDeclKind: b.varDeclKind,
			ownerDeclIdx:     owner,
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
	if o := e.Data.ownerDeclIdx; o >= 0 && o < len(b.res.Decls) {
		if b.res.Decls[o].FullSpan.EndByte < endByte {
			b.res.Decls[o].FullSpan.EndByte = endByte
		}
	}
	b.varDeclKind = e.Data.savedVarDeclKind
}

func (b *builder) closeScopesToDepth(depth int) {
	endByte := uint32(len(b.s.Src))
	for b.stack.Depth() > depth {
		b.closeTopScope(endByte)
	}
}

// closeArrowExprIfTerminating closes any expression-body arrow function
// scope(s) whose body is terminating at the current position. Called
// before processing terminator tokens (`,`, `;`, `)`, `]`, `}`, and at
// newline/stmt-start decl boundaries). An arrow expr scope terminates
// when the current parenVarStack depth has returned to (or gone below)
// its entry depth and it is on top of the scope stack. endByte is the
// byte position to stamp as the scope's end.
func (b *builder) closeArrowExprIfTerminating(endByte uint32) {
	for len(b.arrowExprStack) > 0 {
		top := b.arrowExprStack[len(b.arrowExprStack)-1]
		if len(b.parenVarStack) > top.parenDepth {
			// Inside a nested paren/bracket; not ready to close.
			return
		}
		// Only close if the arrow scope is actually on top of the stack.
		// If a block or other scope opened after the arrow (e.g. an
		// object-literal `{}` inside the body), the caller (handleCloseBrace)
		// will close those first.
		if top2 := b.stack.Top(); top2 == nil || top2.Data.kind != scope.ScopeFunction {
			return
		}
		b.closeTopScope(endByte)
		b.arrowExprStack = b.arrowExprStack[:len(b.arrowExprStack)-1]
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
	// Consume pendingExport once per top-level emission. Import decls,
	// params, and fields inside a scope owner should never be exported
	// via this path — the `export` prefix only applies at the statement
	// that follows it, and pendingExport is cleared here even if the
	// emitted decl isn't traditionally exportable (defensive).
	exported := false
	if b.pendingExport {
		switch kind {
		case scope.KindFunction, scope.KindClass, scope.KindInterface,
			scope.KindType, scope.KindEnum, scope.KindNamespace,
			scope.KindConst, scope.KindLet, scope.KindVar:
			exported = true
		}
		b.pendingExport = false
	}

	primaryNs := b.namespaceFor(kind)
	idx := b.appendDecl(name, kind, span, primaryNs)
	if exported {
		b.res.Decls[idx].Exported = true
	}

	// Dual-namespace emission: TS classes and enums bind in BOTH the
	// value namespace (for `new Foo()`, `Foo.X`) and the type namespace
	// (for `let x: Foo`). Emit a shadow NSType decl; within-file merge
	// (scope.MergeDuplicateDecls) unifies their DeclIDs so refs-to from
	// either position returns the same target.
	if kind == scope.KindClass || kind == scope.KindEnum {
		shadowIdx := b.appendDecl(name, kind, span, scope.NSType)
		if exported {
			b.res.Decls[shadowIdx].Exported = true
		}
	}

	switch kind {
	case scope.KindFunction, scope.KindMethod, scope.KindClass,
		scope.KindInterface, scope.KindEnum, scope.KindNamespace,
		scope.KindType:
		b.pendingOwnerDecl = idx
	}
	b.pendingFullStart = 0
}

// namespaceFor returns the primary namespace for a declaration kind.
// Type-only kinds (interface, type alias) go in NSType; class/interface
// members go in NSField when inside a class/interface body; everything
// else is NSValue. Classes and enums additionally emit a shadow NSType
// decl (see emitDecl).
func (b *builder) namespaceFor(kind scope.DeclKind) scope.Namespace {
	switch kind {
	case scope.KindInterface, scope.KindType:
		return scope.NSType
	case scope.KindField, scope.KindMethod:
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			return scope.NSField
		}
	}
	return scope.NSValue
}

// appendDecl writes a single Decl to b.res.Decls with the given
// namespace and returns its index. FullSpan covers [decl keyword → end
// of body]: scope-owning decls get FullSpan.EndByte patched when the
// body's closing brace closes the scope; class methods have no
// preceding keyword but DO own a scope, so they use the identifier
// position as FullSpan.Start.
//
// pendingFullStart uses a +1 offset to reserve 0 as "unset" (byte 0 is
// a valid declaration-keyword position when a file begins with e.g.
// `function x() {}`).
func (b *builder) appendDecl(name string, kind scope.DeclKind, span scope.Span, ns scope.Namespace) int {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	// File-scope decls hash with canonicalPath so importers bind to
	// the same DeclID. Nested-scope decls keep the file path — scope
	// IDs are file-local and cross-file collisions aren't possible.
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
	return idx
}

func (b *builder) emitRef(name string, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	ns := scope.NSValue
	if b.typePosition || b.inGenericParams {
		ns = scope.NSType
	}
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: ns,
		Scope:     scopeID,
	})
}

// emitPropertyRef records a ref from a property-access position
// (`x.Name` — we're at Name, following `.`). Binding is BindProbable
// with Reason="property_access": name-only, no decl link. Consumers
// (refs-to, rename) match these by name against field/method decls.
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

// tryResolveThisField attempts to resolve `this.name` at `span` against
// the nearest enclosing class or interface's NSField decls. Returns true
// if it found a match and emitted a resolved ref; false otherwise, in
// which case the caller should fall back to emitPropertyRef.
//
// The resolution is: walk the scope stack outward, find the nearest
// ScopeClass or ScopeInterface, then scan res.Decls for a decl whose
// Name matches, Namespace is NSField, and Scope equals that class's
// scope ID. Works across arrow functions nested inside methods because
// arrow `this` binds lexically to the enclosing method's receiver, and
// the class scope is still on the stack.
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
		// Pre-bound refs skip the scope walk:
		//  - property_access: probable, name-only, intentionally unresolved.
		//  - this_dot_field: resolved directly against the enclosing class.
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "this_dot_field" {
			continue
		}
		cur := r.Scope
		resolved := false
		// Try the ref's own namespace first. On miss, fall back to the
		// other value/type namespace — position detection at ref-emission
		// time is imprecise, so a value-tagged ref to an interface (which
		// lives only in NSType) should still resolve.
		alt := altNamespace(r.Namespace)
		namespaces := []scope.Namespace{r.Namespace}
		if alt != r.Namespace {
			namespaces = append(namespaces, alt)
		}
		for _, ns := range namespaces {
			cur = r.Scope
			for {
				if d, ok := byKey[key{scope: cur, name: r.Name, ns: ns}]; ok {
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
					if d, ok := byKey[key{scope: 0, name: r.Name, ns: ns}]; ok {
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
			if resolved {
				break
			}
		}
		if !resolved {
			// Signature-position generics: `function foo<T>(x: T)` — the
			// ref to T in the param list is emitted at file scope but the
			// T decl lives in the function scope (opened later at '{').
			// Pass 2: for an unresolved ref, look for a KindType decl
			// whose source position precedes the ref AND whose enclosing
			// scope's end-byte follows the ref. Bind with a distinct
			// reason so consumers can tell it apart. Namespace match is
			// relaxed here: type-param decls are NSType but annotation
			// refs are emitted as NSValue until Stage 2 ref-position
			// tagging lands.
			for j := range b.res.Decls {
				d := &b.res.Decls[j]
				if d.Kind != scope.KindType || d.Name != r.Name {
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

// looksLikeJSXStart reports whether the '<' we're currently sitting at
// (not yet consumed) plausibly starts a JSX element, given:
//   1. the character immediately after '<' is a letter, '/', or '>'
//   2. the prevByte context is one where an expression is expected
// Called only when jsxEnabled and NOT inGenericParams/expected.
func looksLikeJSXStart(b *builder) bool {
	next := b.s.PeekAt(1)
	// Must be ident-start, '/', or '>' right after '<'.
	if !(identStart[next] || next == '/' || next == '>') {
		return false
	}
	// And the preceding context must be expression-position.
	switch b.prevByte {
	case '=', '(', '[', '{', ',', ';', ':', '?', '!', '|', '&',
		'~', '+', '-', '*', '/', '^', '%', '>', 0:
		return true
	case 'k': // previous keyword (return, yield, throw, etc.)
		return true
	}
	// Statement boundary with no prev byte counts too.
	return b.stmtStart
}

// handleJSXElement consumes a full JSX element (or fragment) starting
// at '<'. Emits component-name refs for capitalized tags. Recurses on
// nested elements and calls back into the main token handling inside
// '{...}' embedded expressions so scope resolution still applies there.
func (b *builder) handleJSXElement() {
	b.s.Pos++ // consume '<'
	// Fragment: <> ... </>
	if b.s.Peek() == '>' {
		b.s.Pos++
		b.scanJSXChildren()
		b.prevByte = '>'
		b.regexOK = false
		return
	}
	// Closing tag: </Name> — not a start. Consume to matching '>'.
	if b.s.Peek() == '/' {
		for !b.s.EOF() && b.s.Peek() != '>' {
			b.s.Pos++
		}
		if !b.s.EOF() {
			b.s.Pos++
		}
		b.prevByte = '>'
		b.regexOK = false
		return
	}
	// Opening tag name (possibly dotted: <Foo.Bar>).
	if identStart[b.s.Peek()] {
		startByte := uint32(b.s.Pos)
		word := b.s.ScanIdentTable(&identStart, &identCont)
		endByte := uint32(b.s.Pos)
		// Capitalized tag = component reference.
		if len(word) > 0 {
			c0 := word[0]
			if c0 >= 'A' && c0 <= 'Z' {
				b.emitRef(string(word), mkSpan(startByte, endByte))
			}
		}
		// Skip dotted members (Foo.Bar.Baz) — these are property accesses.
		for !b.s.EOF() && b.s.Peek() == '.' {
			b.s.Pos++
			b.s.ScanIdentTable(&identStart, &identCont)
		}
	}
	// Attributes.
	b.scanJSXAttributes()
	// Self-close or open?
	if b.s.Peek() == '/' && b.s.PeekAt(1) == '>' {
		b.s.Advance(2)
		b.prevByte = '>'
		b.regexOK = false
		return
	}
	if b.s.Peek() == '>' {
		b.s.Pos++
		b.scanJSXChildren()
	}
	b.prevByte = '>'
	b.regexOK = false
}

// scanJSXAttributes consumes attributes up to '>' or '/>'.
func (b *builder) scanJSXAttributes() {
	for !b.s.EOF() {
		b.skipJSXWS()
		c := b.s.Peek()
		if c == '>' || c == '/' || c == 0 {
			return
		}
		// `{...spread}` attribute.
		if c == '{' {
			b.s.Pos++
			b.scanJSXEmbedded()
			continue
		}
		// Attribute name.
		if identStart[c] {
			b.s.ScanIdentTable(&identStart, &identCont)
			// Skip namespaced (aria-label:foo, xlink:href) — colon or dash.
			for !b.s.EOF() && (b.s.Peek() == ':' || b.s.Peek() == '-') {
				b.s.Pos++
				if identStart[b.s.Peek()] {
					b.s.ScanIdentTable(&identStart, &identCont)
				}
			}
			b.skipJSXWS()
			if b.s.Peek() == '=' {
				b.s.Pos++
				b.skipJSXWS()
				switch b.s.Peek() {
				case '"':
					b.s.ScanSimpleString('"')
				case '\'':
					b.s.ScanSimpleString('\'')
				case '{':
					b.s.Pos++
					b.scanJSXEmbedded()
				}
			}
			continue
		}
		b.s.Pos++ // unknown: advance one byte
	}
}

// scanJSXChildren reads child content until the matching closing tag.
func (b *builder) scanJSXChildren() {
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '<' {
			// </Name> closing tag or nested <Name>.
			if b.s.PeekAt(1) == '/' {
				// Consume through '>'.
				for !b.s.EOF() && b.s.Peek() != '>' {
					b.s.Pos++
				}
				if !b.s.EOF() {
					b.s.Pos++
				}
				return
			}
			b.handleJSXElement()
			continue
		}
		if c == '{' {
			b.s.Pos++
			b.scanJSXEmbedded()
			continue
		}
		if c == '\n' {
			b.s.Next()
			continue
		}
		b.s.Pos++
	}
}

// scanJSXEmbedded processes a '{...}' expression inside JSX. We've
// already consumed the opening '{'. Idents inside get routed through
// handleIdent so scope resolution still applies; nested JSX is also
// handled (e.g. `{items.map(x => <Item key={x} />)}`).
func (b *builder) scanJSXEmbedded() {
	depth := 1
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		switch {
		case c == '{':
			depth++
			b.s.Pos++
		case c == '}':
			depth--
			b.s.Pos++
			if depth == 0 {
				return
			}
		case c == '<':
			if b.jsxEnabled && looksLikeJSXStart(b) {
				b.handleJSXElement()
			} else {
				b.s.Pos++
				b.prevByte = '<'
			}
		case c == '"':
			b.s.ScanSimpleString('"')
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.prevByte = '\''
		case c == '`':
			b.s.ScanInterpolatedString('`', "${", skipTemplateExpr)
			b.prevByte = '`'
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case lexkit.IsDefaultIdentStart(c) || c == '$':
			word := b.s.ScanIdentTable(&identStart, &identCont)
			b.handleIdent(word)
		case c == '\n':
			b.s.Next()
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

// skipJSXWS skips whitespace and line/block comments inside JSX.
func (b *builder) skipJSXWS() {
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

// altNamespace returns the opposite value/type namespace. For NSField
// and other non-value/type namespaces it returns the input unchanged —
// fields and params never fall back.
func altNamespace(ns scope.Namespace) scope.Namespace {
	switch ns {
	case scope.NSValue:
		return scope.NSType
	case scope.NSType:
		return scope.NSValue
	}
	return ns
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
