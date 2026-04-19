// Package swift is the Swift scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single file.
// Handles file / function / block / class / namespace scopes and
// decls for class/struct/enum/protocol/extension/func/init/deinit/
// subscript/var/let/typealias/import/param bindings. Identifiers not
// in declaration position are emitted as Refs and resolved via
// scope-chain walk to the innermost matching Decl.
//
// v1 scope (see package README for full list):
//   - Property wrappers (`@State var x`) — attributes are skipped but
//     not parsed; the decl itself is still emitted.
//   - Variadic params `T...`, opaque types `some Protocol`, macros,
//     result builders, and regex literals `/.../` are NOT handled.
//   - Argument labels at call sites (`foo(first: 1)`) are NOT emitted
//     as refs — an ident immediately followed by ':' inside `(...)` is
//     treated as a label, not a binding reference.
//   - Closures that omit the `in` keyword (implicit `$0`) push a scope
//     but do not emit named params.
//   - self.X / Self.X binds to the enclosing class/struct/enum's
//     field and method decls (NSField) via tryResolveSelfField.
//   - Conditional bindings `if let NAME = expr`, `while let NAME = expr`
//     emit NAME scoped to the block body (KindLet, or KindVar for
//     `if var` / `while var`). `guard let NAME = expr else { ... }`
//     emits NAME into the enclosing scope (guard unwraps for the rest
//     of the surrounding block). Pattern-match bindings inside switch
//     `case .foo(let x)` are emitted as KindVar but `if case` / `guard
//     case` / `while case` pattern-match bindings are NOT handled.
package swift

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Swift source buffer. file is the
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

// scopeEntry carries per-scope state saved/restored across push/pop.
type scopeEntry struct {
	kind scope.ScopeKind
	id   scope.ScopeID
	// ownerDeclIdx is the index in res.Decls of the decl that owns this
	// scope (function, class, struct, enum, protocol, extension). -1 if
	// none. On scope close, FullSpan.EndByte is patched to endByte.
	ownerDeclIdx int
	// structNeedsName: at the top depth of a type body, the first ident
	// per statement/section names a field or method.
	savedStructNeedsName bool
	savedStructDepth     int
	// parenDepth snapshot: closures push a function scope mid-expression,
	// so we need to be able to close them when returning to the enclosing
	// call paren depth on ',' or ')'.
	savedParenDepth int
}

// pendingParam stashes a parameter or generic type param until the body
// scope opens, then flushes as a decl inside that scope.
type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	stmtStart bool

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push. Set by class/struct/enum/protocol/extension/func/
	// init/deinit/subscript and by property accessor blocks.
	pendingScope *scope.ScopeKind

	// declContext: next ident is a decl of this kind. Cleared on emit
	// unless varDeclKind keeps the statement alive through commas.
	declContext scope.DeclKind

	// varDeclKind: the enclosing var/let kind for multi-name decls,
	// e.g. `var a = 1, b = 2`. Cleared at statement end.
	varDeclKind scope.DeclKind

	// pendingFullStart is the byte position of the most recent decl
	// keyword (class, struct, enum, protocol, extension, func, init,
	// deinit, subscript, var, let, typealias, import). emitDecl uses
	// it as FullSpan.StartByte so full span covers keyword → closing
	// brace for scope-owning decls.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last emitted
	// decl that owns an upcoming scope. Consumed by the next openScope.
	pendingOwnerDecl int

	// paramListPending: after a func / init / subscript name and any
	// `<...>` generics, the next '(' starts a param list.
	paramListPending      bool
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool
	// paramLabelSeen tracks whether we've already seen the external
	// label token in the current param section (Swift `first x: Int`
	// emits `x` as the internal param name; `first` is skipped).
	paramLabelSeen bool

	// genericParamsPending: after `class N`, `struct N`, `enum N`,
	// `protocol N`, `func N`, `typealias N`, the next '<' is generics.
	genericParamsPending    bool
	inGenericParams         bool
	genericDepth            int
	genericSectionNeedsName bool

	pendingParams []pendingParam

	// import-handling.
	inImport bool

	// Extension target: when we see `extension`, the next ident is the
	// target type ident. We stash it as the extension's class scope
	// name so the scope is associated with the target type (the
	// extension-as-reopening pattern, mirroring Ruby open-class).
	inExtensionTarget     bool
	extensionTargetPending string
	extensionTargetSpan    scope.Span

	// parenVarStack: save/restore varDeclKind across () and [] pairs.
	parenVarStack []scope.DeclKind

	// structNeedsName: at the top depth of a class/struct/enum/protocol
	// body, the first ident in each section names a field or method.
	// For extensions, the same rule applies.
	structNeedsName bool
	structDepth     int

	// prevIdentIsSelf: last ident scanned was `self` or `Self`. Used to
	// resolve `self.X` against enclosing class/struct/enum's NSField
	// decls.
	prevIdentIsSelf bool

	// controlBlockExpected: set by if/else/while/for/do/switch/guard/
	// repeat. The next '{' is a block, not a computed-property accessor.
	controlBlockExpected bool

	// bindingKindInControl: for `if let x =` and `guard let x = ... else`
	// and `while let x =` — the ident after `let`/`var` is a KindVar
	// that binds in the enclosing or body scope.
	bindingKindInControl scope.DeclKind

	// caseBindingExpected: inside a switch, after `case ` we might see
	// `.foo(let x)` — the ident after `let`/`var` inside the pattern is
	// a KindVar. We conservatively scope it to the current scope (the
	// case arm block or the switch body block).
	caseBindingExpected bool

	// controlBindingMode tracks which conditional-binding construct we
	// are in the head of: "if", "guard", or "while". Set by the
	// keyword and cleared when the corresponding '{' opens the body.
	// For "if" and "while", bindings defer into the body scope via
	// pendingControlBindings. For "guard", bindings emit immediately
	// into the enclosing scope (Swift's guard unwraps for the rest of
	// the surrounding block).
	controlBindingMode string

	// pendingControlBindings stashes if/while let-bindings until the
	// body scope opens, then flushes as decls inside that scope.
	pendingControlBindings []pendingParam

	// closureStack: each entry is the scope stack depth at which this
	// closure scope was pushed, so ',' / ')' / ']' can close it when the
	// surrounding call/bracket context returns to that depth.
	closureStack []closureEntry

	// inClosureHead: true while we're in the head of a `{ params in
	// body }` closure (between '{' and `in`). Idents in this region are
	// collected as param names either directly (bare-ident params like
	// `{ a, b in ... }`) or via the standard inParamList flow for
	// parenthesized typed params `{ (a: Int, b: Int) in ... }`.
	// When we see the `in` keyword, we flush pendingParams into the
	// already-opened closure scope.
	inClosureHead         bool
	closureParamNeedsName bool

	// prevByte tracks the most recent non-whitespace, non-comment byte.
	// Used for regex-vs-operator heuristics (v1 skips regex anyway) and
	// composite-literal detection.
	prevByte byte
}

// closureEntry tracks an open closure scope (opened by a '{' where a
// closure is expected — either trailing closure after a call or an
// expression-position '{'). parenDepth is len(parenVarStack) at push,
// used to decide when the surrounding call's ')' closes the closure.
type closureEntry struct {
	scopeID    scope.ScopeID
	parenDepth int
	braceDepth int // depth of '{' stack at push time
}

func (b *builder) run() {
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.s.Next()
			b.onStatementBoundary()
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			// Swift string literals: support triple-quoted and interpolations
			// via `\(...)`. For v1, skip simple quoted strings byte-by-byte;
			// interpolations' identifiers inside are lost but this keeps the
			// scanner stable.
			b.scanSwiftString()
			b.stmtStart = false
			b.prevByte = '"'
		case c == '@':
			// Attribute like `@objc`, `@MainActor`, `@State`, `@testable`.
			// Consume `@` and the following identifier + optional `(...)`
			// argument list without emitting anything (attributes are
			// skipped per v1 scope).
			b.skipAttribute()
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == ';':
			b.s.Pos++
			b.onStatementBoundary()
			b.prevByte = ';'
		case c == '(':
			b.s.Pos++
			b.parenVarStack = append(b.parenVarStack, b.varDeclKind)
			b.varDeclKind = ""
			b.prevByte = '('
			if b.paramListPending {
				b.paramListPending = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
				b.paramLabelSeen = false
			} else if b.inParamList {
				b.paramDepth++
			} else if b.inClosureHead && b.closureParamNeedsName {
				// Parenthesized closure param list: `{ (a: Int, b: Int) in ... }`.
				// Turn off bare-ident collection and use the standard param-list
				// flow; the `in` keyword will flush pendingParams as decls.
				b.closureParamNeedsName = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
				b.paramLabelSeen = false
			}
		case c == ')':
			// Before consuming, close any trailing-closure scopes whose
			// paren depth matches (i.e., they were trailing closures in
			// this call's arg list and the call is closing).
			b.closeClosuresAtParenDepth(len(b.parenVarStack))
			b.s.Pos++
			if n := len(b.parenVarStack); n > 0 {
				b.varDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			b.prevByte = ')'
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
					b.paramLabelSeen = false
				}
			}
		case c == '[':
			b.s.Pos++
			b.parenVarStack = append(b.parenVarStack, b.varDeclKind)
			b.varDeclKind = ""
			b.prevByte = '['
			if b.inParamList {
				b.paramDepth++
			}
			if b.inGenericParams {
				b.genericDepth++
			}
		case c == ']':
			b.s.Pos++
			if n := len(b.parenVarStack); n > 0 {
				b.varDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			b.prevByte = ']'
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
					b.paramLabelSeen = false
				}
			}
			if b.inGenericParams {
				b.genericDepth--
			}
		case c == '<':
			if b.genericParamsPending {
				b.genericParamsPending = false
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
		case c == ',':
			// Close any closure scopes whose paren depth equals the
			// current paren depth — they were closure args and this
			// comma separates them from the next arg.
			b.closeClosuresAtParenDepth(len(b.parenVarStack))
			b.s.Pos++
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
				b.paramLabelSeen = false
			}
			if b.inGenericParams && b.genericDepth == 1 {
				b.genericSectionNeedsName = true
			}
			// In a bare-ident closure head `{ a, b in ... }`, a comma at
			// top level re-enables the next param name.
			if b.inClosureHead && !b.inParamList {
				b.closureParamNeedsName = true
			}
			if b.varDeclKind != "" && !b.inParamList && !b.inGenericParams {
				b.declContext = b.varDeclKind
			} else {
				b.declContext = ""
			}
			// Struct-like multi-name: `var x, y: Int` — re-enable name.
			sk := b.currentScopeKind()
			if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
				b.structNeedsName = true
			}
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == ':':
			// In a param list at top depth, ':' ends the param-name/label
			// section and starts the type annotation.
			b.s.Pos++
			b.prevByte = ':'
			if b.inParamList && b.paramDepth == 1 {
				// Type annotation follows — subsequent idents become refs.
				b.paramSectionNeedsName = false
				b.paramLabelSeen = false
			}
		case lexkit.IsDefaultIdentStart(c) || c == '$':
			word := b.s.ScanIdentTable(&identStart, &identCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' && cc != 'x' && cc != 'e' && cc != 'o' && cc != 'b' {
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

func (b *builder) onStatementBoundary() {
	b.stmtStart = true
	b.declContext = ""
	b.varDeclKind = ""
	sk := b.currentScopeKind()
	if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
		b.structNeedsName = true
	}
	// Close any import statement that didn't get a terminator (Swift
	// imports end at newline).
	if b.inImport {
		b.inImport = false
	}
}

// scanSwiftString consumes a Swift string literal starting at '"'. It
// handles triple-quoted `"""..."""` by delegating to the simple scanner
// twice; interpolations `\(...)` inside are skipped byte-by-byte up to
// the matching ')'.
func (b *builder) scanSwiftString() {
	if b.s.Peek() != '"' {
		return
	}
	// Triple-quoted string.
	if b.s.PeekAt(1) == '"' && b.s.PeekAt(2) == '"' {
		b.s.Advance(3)
		for !b.s.EOF() {
			if b.s.Peek() == '"' && b.s.PeekAt(1) == '"' && b.s.PeekAt(2) == '"' {
				b.s.Advance(3)
				return
			}
			if b.s.Peek() == '\\' && b.s.PeekAt(1) == '(' {
				b.s.Advance(2)
				b.skipBalancedParen()
				continue
			}
			b.s.Next()
		}
		return
	}
	// Single-line string.
	b.s.Pos++ // opening quote
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '\\' {
			if b.s.PeekAt(1) == '(' {
				b.s.Advance(2)
				b.skipBalancedParen()
				continue
			}
			// Other escape: skip the next char.
			b.s.Advance(2)
			continue
		}
		if c == '"' {
			b.s.Pos++
			return
		}
		if c == '\n' {
			// Unterminated — stop at newline.
			return
		}
		b.s.Next()
	}
}

// skipBalancedParen consumes bytes until the matching ')' at depth 0.
// Used to skip string interpolation expressions.
func (b *builder) skipBalancedParen() {
	depth := 1
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		switch c {
		case '(':
			depth++
			b.s.Pos++
		case ')':
			depth--
			b.s.Pos++
		case '"':
			b.scanSwiftString()
		case '/':
			if b.s.PeekAt(1) == '/' {
				b.s.SkipLineComment()
			} else if b.s.PeekAt(1) == '*' {
				b.s.Advance(2)
				b.s.SkipBlockComment("*/")
			} else {
				b.s.Pos++
			}
		default:
			b.s.Next()
		}
	}
}

// skipAttribute consumes a single attribute (`@name`, `@name(args)`,
// `@testable`). Leaves position at the byte after the attribute.
func (b *builder) skipAttribute() {
	if b.s.Peek() != '@' {
		return
	}
	b.s.Pos++ // '@'
	// Skip the attribute identifier (may include dots like @UIKit.Main).
	for !b.s.EOF() {
		c := b.s.Peek()
		if !lexkit.IsDefaultIdentStart(c) && !lexkit.IsASCIIDigit(c) && c != '.' {
			break
		}
		b.s.Pos++
	}
	// Optional `(...)` argument list.
	save := b.s.Pos
	saveLine := b.s.Line
	// Skip whitespace.
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' {
			b.s.Pos++
			continue
		}
		break
	}
	if !b.s.EOF() && b.s.Peek() == '(' {
		b.s.Pos++
		b.skipBalancedParen()
		return
	}
	b.s.Pos = save
	b.s.Line = saveLine
}

func (b *builder) handleIdent(word []byte) {
	if len(word) == 0 {
		return
	}
	startByte := uint32(b.s.Pos - len(word))
	endByte := uint32(b.s.Pos)
	name := string(word)
	_ = b.stmtStart // reserved for future use (e.g., per-statement ASI)
	b.stmtStart = false

	// Keywords that change parser state.
	switch name {
	case "class":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "struct":
		b.declContext = scope.KindClass // reuse class scope kind for struct
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "protocol":
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "extension":
		// Next ident is the target type. Emit no decl for the extension
		// itself — extensions reopen a type and their members are added
		// into a ScopeClass bound to the target type name.
		b.inExtensionTarget = true
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "func":
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "init":
		// Emit `init` as KindMethod at current (class/struct/enum) scope.
		scopeK := b.currentScopeKind()
		if scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface {
			b.emitDecl("init", scope.KindMethod, mkSpan(startByte, endByte))
			b.paramListPending = true
			b.genericParamsPending = true
			k := scope.ScopeFunction
			b.pendingScope = &k
			b.pendingFullStart = startByte + 1
		} else {
			// Bare ref fallback (rare).
			b.emitRef(name, mkSpan(startByte, endByte))
		}
		b.prevByte = 'i'
		return
	case "deinit":
		scopeK := b.currentScopeKind()
		if scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface {
			b.emitDecl("deinit", scope.KindMethod, mkSpan(startByte, endByte))
			k := scope.ScopeFunction
			b.pendingScope = &k
			b.pendingFullStart = startByte + 1
		} else {
			b.emitRef(name, mkSpan(startByte, endByte))
		}
		b.prevByte = 'i'
		return
	case "subscript":
		scopeK := b.currentScopeKind()
		if scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface {
			b.emitDecl("subscript", scope.KindMethod, mkSpan(startByte, endByte))
			b.paramListPending = true
			b.genericParamsPending = true
			k := scope.ScopeFunction
			b.pendingScope = &k
			b.pendingFullStart = startByte + 1
		} else {
			b.emitRef(name, mkSpan(startByte, endByte))
		}
		b.prevByte = 'i'
		return
	case "var":
		// In control-flow binding position (`if var`, `guard var`,
		// `while var`, or `case .foo(var x)`), bind as KindVar. Else,
		// treat as a property/local var.
		if b.controlBindingMode != "" {
			b.bindingKindInControl = scope.KindVar
			b.declContext = scope.KindVar
			b.pendingFullStart = startByte + 1
		} else if b.bindingKindInControl != "" {
			// switch-case pattern binding.
			b.declContext = scope.KindVar
		} else {
			b.declContext = scope.KindVar
			b.varDeclKind = scope.KindVar
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "let":
		if b.controlBindingMode != "" {
			// `if let` / `guard let` / `while let` — bind as KindLet.
			b.bindingKindInControl = scope.KindLet
			b.declContext = scope.KindLet
			b.pendingFullStart = startByte + 1
		} else if b.bindingKindInControl != "" {
			// switch-case pattern binding: `case .foo(let x)`.
			b.declContext = scope.KindVar
		} else {
			b.declContext = scope.KindLet
			b.varDeclKind = scope.KindLet
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "typealias":
		b.declContext = scope.KindType
		b.pendingFullStart = startByte + 1
		b.genericParamsPending = true
		b.prevByte = 'k'
		return
	case "import":
		b.inImport = true
		b.declContext = scope.KindImport
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "case":
		// Inside an enum body at top depth, `case Red, Green` declares
		// enum cases as KindConst. Inside a switch body, `case .foo(let x)`
		// starts a match arm pattern — bindings inside are KindVar.
		scopeK := b.currentScopeKind()
		if (scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface) && b.structDepth == 0 {
			// enum-case section.
			b.declContext = scope.KindConst
			b.varDeclKind = scope.KindConst
			b.pendingFullStart = startByte + 1
		} else {
			// In switch body: pattern-binding context.
			b.caseBindingExpected = true
			b.bindingKindInControl = scope.KindVar
			b.controlBlockExpected = true
		}
		b.prevByte = 'k'
		return
	case "if", "else", "while", "for", "do", "switch", "guard", "repeat":
		b.controlBlockExpected = true
		// `if let X =` / `guard let X =` / `while let X =` — the next
		// `let` or `var` creates a new binding. For if/while, it's
		// scoped to the body (deferred via pendingControlBindings). For
		// guard, it's scoped to the enclosing scope (emitted immediately).
		if name == "if" || name == "guard" || name == "while" {
			b.bindingKindInControl = scope.KindVar
			b.controlBindingMode = name
			b.pendingControlBindings = nil
		} else if name == "else" {
			// `else` after an if-block or guard's else-clause starts a
			// block, but any in-flight control-binding state is no longer
			// relevant (guard's bindings were already emitted; the else
			// block can't introduce new conditional bindings).
			b.controlBindingMode = ""
		}
		b.prevByte = 'k'
		return
	case "in":
		// In a closure head, `in` terminates the param list. Flush
		// pendingParams into the current (already-pushed) closure scope.
		if b.inClosureHead {
			for _, p := range b.pendingParams {
				pk := p.kind
				if pk == "" {
					pk = scope.KindParam
				}
				b.emitDecl(p.name, pk, p.span)
			}
			b.pendingParams = nil
			b.inClosureHead = false
			b.closureParamNeedsName = false
		}
		b.prevByte = 'k'
		return
	case "return", "throw", "try", "throws", "rethrows", "async", "await",
		"break", "continue", "fallthrough", "defer", "where",
		"true", "false", "nil", "as", "is", "inout", "some", "any",
		"public", "private", "internal", "fileprivate", "open",
		"static", "final", "lazy", "weak", "unowned", "mutating",
		"nonmutating", "override", "required", "convenience",
		"dynamic", "optional", "indirect", "associatedtype",
		"operator", "precedencegroup", "prefix", "postfix", "infix",
		"Any", "Type", "Protocol":
		// Modifiers and simple keywords. `associatedtype` behaves like
		// typealias for v1 (treat its name as a type decl on the next
		// ident), but we conservatively skip for v1.
		b.prevByte = 'k'
		return
	case "self", "Self":
		b.prevIdentIsSelf = true
		b.prevByte = 'k'
		return
	}

	// Property access after '.'.
	if b.prevByte == '.' {
		if b.prevIdentIsSelf {
			b.prevIdentIsSelf = false
			if b.tryResolveSelfField(name, mkSpan(startByte, endByte)) {
				b.prevByte = 'i'
				return
			}
		}
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}
	b.prevIdentIsSelf = false

	// Extension target ident: bind the class scope we just pre-queued
	// to this name. We do NOT emit a decl for the extension target —
	// the target type's decl is defined elsewhere (or in another file);
	// refs to it resolve via the scope chain.
	if b.inExtensionTarget {
		b.inExtensionTarget = false
		b.extensionTargetPending = name
		b.extensionTargetSpan = mkSpan(startByte, endByte)
		// Emit the target as a ref so refs-to-type queries pick it up.
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Bare-ident closure params: `{ a, b in ... }`. When inClosureHead
	// is true and we're NOT already in a parenthesized param list, the
	// first ident of each comma-separated section is a param name.
	// Typed parenthesized params `{ (a: Int, b: Int) in ... }` go
	// through the standard inParamList path instead.
	if b.inClosureHead && !b.inParamList && b.closureParamNeedsName {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindParam,
		})
		b.closureParamNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Inside a param list section: handle Swift's label+name syntax.
	// `func foo(first x: Int, _ y: Int)` — external label then internal
	// param name. The first ident in the section is either:
	//   (a) the label, if the next non-ws ident also looks like an ident
	//       and isn't followed by ':' (which would mean it's a simple
	//       param name); OR
	//   (b) the only name, used as both label and param name.
	// Heuristic: if the next non-ws byte is also an ident-start and the
	// char after that ident is NOT ':' (i.e., another ident follows, so
	// this is `label name: Type`), treat this as a label and skip emit.
	// Otherwise, emit as the param name. Also skip literal `_` label.
	if b.inParamList && b.paramDepth == 1 && b.paramSectionNeedsName {
		if name == "_" {
			// Wildcard label: next ident is the real param name.
			b.paramLabelSeen = true
			b.prevByte = 'i'
			return
		}
		if !b.paramLabelSeen && b.peekIsLabelThenName() {
			// Two idents in a row: first is label, skip.
			b.paramLabelSeen = true
			b.prevByte = 'i'
			return
		}
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindParam,
		})
		b.paramSectionNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Inside generics `<T: P, U: Q>`: first ident per section is a type.
	if b.inGenericParams && b.genericDepth == 1 && b.genericSectionNeedsName {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindType,
		})
		b.genericSectionNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Argument label at a call site: an ident followed by ':' inside a
	// function-call `(...)` (i.e., we're NOT in a param declaration list
	// and we ARE inside a paren-nested context). Skip emission — Swift
	// call arg labels are not refs.
	if !b.inParamList && !b.inGenericParams && len(b.parenVarStack) > 0 &&
		b.declContext == "" && b.peekNonWSByte() == ':' {
		b.prevByte = 'i'
		return
	}

	if b.declContext != "" {
		kind := b.declContext
		// For `if let NAME` / `if var NAME` / `while let NAME` /
		// `while var NAME`, defer emission until the body '{' opens so
		// the decl is scoped to the block. For `guard let NAME`, emit
		// immediately — guard binds into the ENCLOSING scope.
		if (b.controlBindingMode == "if" || b.controlBindingMode == "while") &&
			(kind == scope.KindLet || kind == scope.KindVar) &&
			b.bindingKindInControl != "" {
			b.pendingControlBindings = append(b.pendingControlBindings, pendingParam{
				name: name,
				span: mkSpan(startByte, endByte),
				kind: kind,
			})
			b.declContext = ""
			b.bindingKindInControl = ""
			b.prevByte = 'i'
			return
		}
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		switch kind {
		case scope.KindFunction, scope.KindMethod:
			b.paramListPending = true
			b.genericParamsPending = true
		case scope.KindClass, scope.KindInterface, scope.KindType, scope.KindEnum:
			b.genericParamsPending = true
		}
		// If this was a control-flow binding (if/guard/while let x), clear.
		b.bindingKindInControl = ""
		b.prevByte = 'i'
		return
	}

	// Struct/interface/class/enum/extension body: a bare ident is either
	// a member (field or method) or a type-ref after modifier. In Swift,
	// declarations always start with a keyword (var/let/func/init/...),
	// so a bare ident at top depth here is almost always a type ref
	// (e.g., a protocol conformance list after the type header).
	// We only emit fields/methods via declContext, so fall through.

	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

// peekIsLabelThenName reports whether the bytes following the current
// scanner position look like `<ws>name:` — i.e., there's another ident
// and then a colon, meaning the ident we just consumed was an external
// label. For `_ x: Int`, the caller handles `_` separately.
func (b *builder) peekIsLabelThenName() bool {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() { b.s.Pos = save; b.s.Line = saveLine }()
	// Skip whitespace and comments.
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' {
			b.s.Pos++
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
	if b.s.EOF() {
		return false
	}
	c := b.s.Peek()
	if !lexkit.IsDefaultIdentStart(c) && c != '_' && c != '$' {
		return false
	}
	// Skip the ident.
	b.s.Pos++
	for !b.s.EOF() {
		c := b.s.Peek()
		if !lexkit.IsDefaultIdentStart(c) && !lexkit.IsASCIIDigit(c) {
			break
		}
		b.s.Pos++
	}
	// Skip whitespace.
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' {
			b.s.Pos++
			continue
		}
		break
	}
	return !b.s.EOF() && b.s.Peek() == ':'
}

// peekNonWSByte returns the next non-whitespace, non-comment byte
// without advancing.
func (b *builder) peekNonWSByte() byte {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() { b.s.Pos = save; b.s.Line = saveLine }()
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
		return c
	}
	return 0
}

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	b.stmtStart = true
	b.prevByte = '{'

	// Explicit scope push via pendingScope (class/struct/enum/protocol/
	// extension/func/init/deinit/subscript body).
	if b.pendingScope != nil {
		kind := *b.pendingScope
		b.pendingScope = nil
		b.openScope(kind, uint32(b.s.Pos-1))
		// Flush type-params and value-params into the new scope.
		if kind == scope.ScopeFunction || kind == scope.ScopeClass || kind == scope.ScopeInterface {
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
		b.controlBlockExpected = false
		return
	}

	// Control-flow block (`if cond {`, `while cond {`, etc.).
	if b.controlBlockExpected {
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		b.bindingKindInControl = ""
		b.caseBindingExpected = false
		// Flush any `if let` / `while let` bindings into this block
		// scope. `guard let` bindings were already emitted into the
		// enclosing scope so pendingControlBindings is empty here.
		if b.controlBindingMode == "if" || b.controlBindingMode == "while" {
			for _, p := range b.pendingControlBindings {
				b.emitDecl(p.name, p.kind, p.span)
			}
		}
		b.pendingControlBindings = nil
		b.controlBindingMode = ""
		return
	}

	// Closure: any other '{' is a closure literal. Swift closures have
	// the form `{ params in body }` or `{ body }` (implicit $0). Push a
	// ScopeFunction and mark this as a closure so the matching '}' or a
	// terminating ')' / ',' / ']' can close it.
	b.openScope(scope.ScopeFunction, uint32(b.s.Pos-1))
	b.closureStack = append(b.closureStack, closureEntry{
		scopeID:    b.currentScope(),
		parenDepth: len(b.parenVarStack),
		braceDepth: b.stack.Depth(),
	})
	b.inClosureHead = true
	b.closureParamNeedsName = true
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.prevByte = '}'
	// If the top of the stack is a closure scope, pop it and its entry.
	if len(b.closureStack) > 0 {
		top := b.closureStack[len(b.closureStack)-1]
		if top.scopeID == b.currentScope() {
			b.closureStack = b.closureStack[:len(b.closureStack)-1]
			// If this closure never saw `in`, its pending params are
			// unused — discard.
			if b.inClosureHead {
				b.inClosureHead = false
				b.closureParamNeedsName = false
				b.pendingParams = nil
			}
		}
	}
	b.closeTopScope(uint32(b.s.Pos))
	b.stmtStart = true
}

// closeClosuresAtParenDepth closes any open closure scopes whose entry
// parenDepth equals the given depth. Called on ')' / ',' / ']' to close
// trailing closures and argument-list closures.
func (b *builder) closeClosuresAtParenDepth(depth int) {
	for len(b.closureStack) > 0 {
		top := b.closureStack[len(b.closureStack)-1]
		if top.parenDepth != depth {
			break
		}
		if top.scopeID != b.currentScope() {
			break
		}
		b.closureStack = b.closureStack[:len(b.closureStack)-1]
		if b.inClosureHead {
			b.inClosureHead = false
			b.closureParamNeedsName = false
			b.pendingParams = nil
		}
		b.closeTopScope(uint32(b.s.Pos))
	}
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
			kind:                 kind,
			id:                   id,
			ownerDeclIdx:         owner,
			savedStructNeedsName: b.structNeedsName,
			savedStructDepth:     b.structDepth,
			savedParenDepth:      len(b.parenVarStack),
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
	// Fresh scope: reset struct-body state.
	if kind == scope.ScopeClass || kind == scope.ScopeInterface {
		b.structNeedsName = true
		b.structDepth = 0
	} else {
		b.structNeedsName = false
		b.structDepth = 0
	}
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
	b.structNeedsName = e.Data.savedStructNeedsName
	b.structDepth = e.Data.savedStructDepth
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

func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	ns := scope.NSValue
	// Members of a class/struct/enum/protocol/extension body go in NSField
	// so they don't shadow same-name top-level decls during scope-chain
	// resolution. Refs via property access (obj.x) go through
	// emitPropertyRef.
	if kind == scope.KindField || kind == scope.KindMethod {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			ns = scope.NSField
		}
	}
	// Enum cases: keep them in NSField so they don't shadow top-level
	// decls; enum-case refs at call sites look like `.red` (property
	// access) and bind via property_access name matching.
	if kind == scope.KindConst {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			ns = scope.NSField
		}
	}
	// Var/let declared inside a type body are fields (NSField). Promote
	// KindVar/KindLet to KindField when we're at top depth of a class/
	// struct/enum/protocol/extension.
	if (kind == scope.KindVar || kind == scope.KindLet) && b.structDepth == 0 {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			kind = scope.KindField
			ns = scope.NSField
		}
	}
	// A `func` inside a type body is a method (NSField). Promote
	// KindFunction to KindMethod when we're at top depth of a class/
	// struct/enum/protocol/extension.
	if kind == scope.KindFunction && b.structDepth == 0 {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			kind = scope.KindMethod
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
	case scope.KindFunction, scope.KindMethod, scope.KindClass,
		scope.KindInterface, scope.KindEnum, scope.KindType:
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

// tryResolveSelfField attempts to resolve `self.name` / `Self.name` at
// span against the nearest enclosing class/struct/enum/protocol's
// NSField decls. Returns true if a match was found and a resolved ref
// was emitted.
func (b *builder) tryResolveSelfField(name string, span scope.Span) bool {
	entries := b.stack.Entries()
	var typeScope scope.ScopeID
	for i := len(entries) - 1; i >= 0; i-- {
		k := entries[i].Data.kind
		if k == scope.ScopeClass || k == scope.ScopeInterface {
			typeScope = entries[i].Data.id
			break
		}
	}
	if typeScope == 0 {
		return false
	}
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		if d.Scope != typeScope || d.Namespace != scope.NSField || d.Name != name {
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
				Reason: "self_dot_field",
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
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "self_dot_field" {
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
			// Signature-position generics: `func foo<T>(x: T)` — T ref
			// in the param list is emitted at the enclosing scope but
			// the T decl lives in the function scope (opened later at
			// '{'). For an unresolved ref, look for a KindType decl
			// whose source position precedes the ref and whose scope
			// encloses the ref byte range.
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
			if builtins.Swift.Has(r.Name) {
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

var identStart [256]bool
var identCont [256]bool

func init() {
	for c := 0; c < 256; c++ {
		cb := byte(c)
		if lexkit.IsDefaultIdentStart(cb) {
			identStart[c] = true
			identCont[c] = true
		}
		if lexkit.IsASCIIDigit(cb) {
			identCont[c] = true
		}
	}
	identStart['$'] = true // Swift allows $0, $1 in closures
	identCont['$'] = true
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
	h.Write([]byte("<builtin:swift>"))
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
