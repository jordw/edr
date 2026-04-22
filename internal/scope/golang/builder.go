// Package golang is the Go scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single file.
// Handles file / function / block / struct / interface scopes and
// var / const / func / type / import / param / receiver decls.
// Identifiers not in declaration position are emitted as Refs and
// resolved via scope-chain walk to the innermost matching Decl.
//
// Go is simpler than TS: no hoisting, no declaration merging, no arrow
// functions (func keyword only), no destructuring for vars. But it has:
// receivers, short var decl (:=), block forms (var (...)), multi-name
// assignment, generics (func F[T any]), implicit interface satisfaction
// (handled at the hierarchy layer, not here).
//
// v1 limitations:
//   - Composite literals: `Point{x: 1}` emits `x` as a field decl in a
//     fresh block (incorrect, but harmless for rename since x doesn't
//     resolve outward). A proper fix would distinguish comp-lit braces
//     from block-stmt braces.
//   - `go` and `defer` keywords are passthrough.
//   - Signature-position generic refs may not bind (same gap as TS).
package golang

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Go source buffer.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path used to hash
// file-scope DeclIDs. When canonicalPath is "" the behavior reduces to
// Parse (file-local DeclIDs). A non-empty canonicalPath makes file-
// scope DeclIDs identity-equal across files in the same logical
// package, which is how cross-file rename matches a caller's imported
// ref to the target decl.
//
// Nested-scope decls (function locals, block vars) always hash with
// the file path — they have no cross-file identity and two different
// files could have colliding nested scope IDs otherwise.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:              file,
		canonicalPath:     canonicalPath,
		res:               &scope.Result{File: file},
		s:                 lexkit.New(src),
		pendingOwnerDecl:  -1,
		lastImportDeclIdx: -1,
	}
	b.openScope(scope.ScopeFile, 0)
	b.stmtStart = true
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	return b.res
}

type scopeEntry struct {
	kind                  scope.ScopeKind
	id                    scope.ScopeID
	savedVarDeclKind      scope.DeclKind
	savedStructNeedsName  bool
	savedStructDepth      int
	// ownerDeclIdx is the index in res.Decls of the decl that owns this
	// scope (e.g., for a function scope, the func decl). -1 if none.
	// On scope close, FullSpan.EndByte for that decl is patched to the
	// closing brace position.
	ownerDeclIdx int
}

type builder struct {
	file          string
	canonicalPath string // "" ⇒ fall back to file for DeclID hashing
	res           *scope.Result
	s             lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	stmtStart bool

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push. Set by keywords (func, struct, interface) and by type
	// decls like `type Name struct {...}`.
	pendingScope *scope.ScopeKind

	// declContext: next ident is a decl of this kind. Cleared on emit
	// unless varDeclKind keeps the statement alive through commas.
	declContext scope.DeclKind

	// varDeclKind: the enclosing var/const kind for multi-name decls.
	// Cleared at statement end (\n, ;).
	varDeclKind scope.DeclKind

	// inBlockDecl: inside a `var (...)` / `const (...)` / `type (...)` /
	// `import (...)` block. Each line starts a fresh binder.
	inBlockDecl    bool
	blockDeclKind  scope.DeclKind
	blockDeclDepth int

	// Receiver-for-next-func: when we see `func (`, the next `(` is the
	// receiver list. After it, the ident is the method name.
	funcReceiverPending bool
	inFuncReceiver      bool

	// paramListPending / inParamList: same roles as TS. After a func name
	// (with optional generic `[T any]`), the `(` starts params.
	paramListPending      bool
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool

	// Type-param `[T any]` in Go uses [], not <>. When paramListPending
	// is true and we see `[`, that's a type-param list.
	typeParamsPending  bool
	inTypeParams       bool
	typeParamDepth     int
	typeParamNeedsName bool

	pendingParams []pendingParam

	// parenVarStack: save/restore varDeclKind across () and [] pairs.
	parenVarStack []scope.DeclKind

	// structNeedsName: at the top depth of a struct/interface body, the
	// first ident in each section (line or comma-separated) is a field or
	// method name; subsequent idents on the same section are type refs.
	// structDepth tracks nested {}/[]/() inside the struct body to avoid
	// applying the rule inside nested types like `X []map[string]int`.
	structNeedsName bool
	structDepth     int

	// shortVarCandidates: idents on the LHS of a possible `a, b := ...`.
	// If we hit `:=`, they become decls. Else, they're refs.
	shortVarCandidates []pendingParam
	inShortVarLHS      bool

	// pendingFullStart captures the byte position of the most recent
	// declaration keyword (func, type, var, const, struct, interface).
	// emitDecl uses it as FullSpan.StartByte so the full span covers
	// keyword → closing brace for scope-owning decls.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last emitted
	// decl that owns an upcoming scope. Consumed by the next openScope
	// call so closeTopScope can patch FullSpan.EndByte. -1 when none.
	pendingOwnerDecl int

	// controlBlockExpected is true after seeing if/for/switch/select/else;
	// tells handleOpenBrace that the upcoming `{` is a block (control-flow
	// body), not a composite literal. Cleared when `{` consumes it.
	controlBlockExpected bool

	// compositeLitDepth counts nested composite-literal `{}` — `T{...}`,
	// `[]T{...}`, `map[K]V{...}`. A composite literal does NOT introduce
	// a scope; incrementing the depth lets handleIdent skip ident-key
	// emission (the `field` in `T{field: value}`) so those idents do not
	// bind to same-named top-level decls.
	compositeLitDepth int

	// lastImportDeclIdx is the index in res.Decls of the most recently
	// emitted KindImport alias decl (e.g. `alias` in `alias "strings"`)
	// that is still awaiting its path string. When the upcoming import
	// path literal is consumed, its Signature is back-filled on that decl.
	// -1 when none pending. Cleared on statement boundaries.
	lastImportDeclIdx int

	prevByte byte
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
			b.onStatementBoundary()
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			start := b.s.Pos
			b.s.ScanSimpleString('"')
			if (b.declContext == scope.KindImport || b.lastImportDeclIdx >= 0) && b.s.Pos > start+1 {
				path := string(b.s.Src[start+1 : b.s.Pos-1])
				b.handleImportPath(path, uint32(start), uint32(b.s.Pos))
				b.declContext = ""
			}
			b.stmtStart = false
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.stmtStart = false
			b.prevByte = '\''
		case c == '`':
			// Go raw string — no escapes. Skip to matching backtick.
			start := b.s.Pos
			b.s.Pos++
			for !b.s.EOF() && b.s.Peek() != '`' {
				b.s.Next()
			}
			if !b.s.EOF() {
				b.s.Pos++
			}
			if (b.declContext == scope.KindImport || b.lastImportDeclIdx >= 0) && b.s.Pos > start+1 {
				path := string(b.s.Src[start+1 : b.s.Pos-1])
				b.handleImportPath(path, uint32(start), uint32(b.s.Pos))
				b.declContext = ""
			}
			b.stmtStart = false
			b.prevByte = '`'
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
			if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
				b.structDepth++
			}
			if b.funcReceiverPending {
				b.funcReceiverPending = false
				b.inFuncReceiver = true
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.paramListPending {
				b.paramListPending = false
				b.typeParamsPending = false // type params would come before, not after
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.inParamList {
				b.paramDepth++
			} else if b.inBlockDecl && b.blockDeclDepth == 0 {
				// `var (`, `const (`, `type (`, `import (` — the opening
				// paren is the block decl's boundary.
				b.blockDeclDepth = 1
				b.declContext = b.blockDeclKind
			} else if b.inBlockDecl {
				b.blockDeclDepth++
			}
		case c == ')':
			b.s.Pos++
			if n := len(b.parenVarStack); n > 0 {
				b.varDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			b.prevByte = ')'
			if sk := b.currentScopeKind(); (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth > 0 {
				b.structDepth--
			}
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
					if b.inFuncReceiver {
						b.inFuncReceiver = false
						// After receiver, the next ident is the method name.
						b.declContext = scope.KindMethod
					}
				}
			} else if b.inBlockDecl {
				b.blockDeclDepth--
				if b.blockDeclDepth == 0 {
					b.inBlockDecl = false
					b.blockDeclKind = ""
					b.declContext = ""
				}
			}
		case c == '[':
			b.s.Pos++
			b.parenVarStack = append(b.parenVarStack, b.varDeclKind)
			b.varDeclKind = ""
			b.prevByte = '['
			if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
				b.structDepth++
			}
			if b.typeParamsPending {
				b.typeParamsPending = false
				b.inTypeParams = true
				b.typeParamDepth = 1
				b.typeParamNeedsName = true
			} else if b.inTypeParams {
				b.typeParamDepth++
			}
		case c == ']':
			b.s.Pos++
			if n := len(b.parenVarStack); n > 0 {
				b.varDeclKind = b.parenVarStack[n-1]
				b.parenVarStack = b.parenVarStack[:n-1]
			}
			b.prevByte = ']'
			if sk := b.currentScopeKind(); (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth > 0 {
				b.structDepth--
			}
			if b.inTypeParams {
				b.typeParamDepth--
				if b.typeParamDepth == 0 {
					b.inTypeParams = false
					b.typeParamNeedsName = false
				}
			}
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
			} else if b.inTypeParams && b.typeParamDepth == 1 {
				b.typeParamNeedsName = true
			}
			if !b.inShortVarLHS && b.varDeclKind != "" && !b.inParamList && !b.inTypeParams {
				// multi-name var: `var a, b int`
				b.declContext = b.varDeclKind
			}
			// Struct multi-name field: `X, Y int` — a comma at struct top
			// depth re-enables needsName for the next name.
			sk := b.currentScopeKind()
			if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
				b.structNeedsName = true
			}
		case c == ':' && b.s.PeekAt(1) == '=':
			// `:=` short variable declaration. Preceding ident(s) are decls.
			b.s.Advance(2)
			b.prevByte = '='
			if b.inShortVarLHS {
				for _, p := range b.shortVarCandidates {
					b.emitDecl(p.name, scope.KindVar, p.span)
				}
				b.shortVarCandidates = nil
				b.inShortVarLHS = false
			}
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
			if b.inShortVarLHS {
				for _, p := range b.shortVarCandidates {
					b.emitRef(p.name, p.span)
				}
				b.shortVarCandidates = nil
				b.inShortVarLHS = false
			}
		case c == '=':
			b.s.Pos++
			b.prevByte = '='
			if b.inShortVarLHS {
				// `a = 1` is assignment, not decl.
				for _, p := range b.shortVarCandidates {
					b.emitRef(p.name, p.span)
				}
				b.shortVarCandidates = nil
				b.inShortVarLHS = false
			}
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() && (lexkit.IsASCIIDigit(b.s.Peek()) || b.s.Peek() == '.' || b.s.Peek() == '_' || b.s.Peek() == 'x' || b.s.Peek() == 'e') {
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

// onStatementBoundary fires on \n and ;. Ends var/const statements,
// flushes pending short-var LHS as refs (if not followed by `:=`).
// Inside a block decl (`var (... )`), each line is a fresh binder —
// re-activate declContext from blockDeclKind so the next ident is
// recognized as a decl. In struct/interface bodies, a newline begins
// a fresh field/method section — re-enable structNeedsName.
func (b *builder) onStatementBoundary() {
	b.stmtStart = true
	if b.inBlockDecl && b.blockDeclDepth > 0 {
		b.declContext = b.blockDeclKind
		b.varDeclKind = b.blockDeclKind
	} else {
		b.declContext = ""
		b.varDeclKind = ""
	}
	// Re-enable field-name at top depth of a struct/interface scope.
	sk := b.currentScopeKind()
	if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
		b.structNeedsName = true
	}
	if b.inShortVarLHS {
		for _, p := range b.shortVarCandidates {
			b.emitRef(p.name, p.span)
		}
		b.shortVarCandidates = nil
		b.inShortVarLHS = false
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

	switch name {
	case "package":
		// `package x` — x is a package decl; skip for v1 (no scope effect).
		b.prevByte = 'k'
		return
	case "import":
		b.declContext = scope.KindImport
		b.blockDeclKind = scope.KindImport
		b.inBlockDecl = true // may or may not open a block — `(` triggers
		b.prevByte = 'k'
		return
	case "func":
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.funcReceiverPending = true // next `(` might be receiver
		b.prevByte = 'k'
		return
	case "type":
		if wasStmtStart || b.prevByte == ';' || b.prevByte == '{' {
			b.declContext = scope.KindType
			b.blockDeclKind = scope.KindType
			b.inBlockDecl = true
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "var":
		b.declContext = scope.KindVar
		b.varDeclKind = scope.KindVar
		b.blockDeclKind = scope.KindVar
		b.inBlockDecl = true
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "const":
		b.declContext = scope.KindConst
		b.varDeclKind = scope.KindConst
		b.blockDeclKind = scope.KindConst
		b.inBlockDecl = true
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "struct":
		k := scope.ScopeClass // reuse Class kind for struct bodies
		b.pendingScope = &k
		b.prevByte = 'k'
		return
	case "interface":
		k := scope.ScopeInterface
		b.pendingScope = &k
		b.prevByte = 'k'
		return
	case "if", "for", "switch", "select", "else":
		// The `{` that follows this keyword is a block body, not a
		// composite literal — flag it so handleOpenBrace skips the
		// ident-preceded-composite-lit heuristic.
		b.controlBlockExpected = true
		b.prevByte = 'k'
		return
	case "return", "case", "break", "continue",
		"go", "defer", "goto", "range", "chan", "map",
		"true", "false", "nil", "iota":
		b.prevByte = 'k'
		return
	}

	// Property access after `.` (x.y): emit y as a probable ref with
	// Reason="property_access". Imprecise — we don't know the receiver
	// type — but lets refs-to discover cross-package method/field
	// references by name matching. Consumer filters by binding kind.
	if b.prevByte == '.' {
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Composite-literal field key: inside `T{field: value}` (or a map
	// literal `{key: value}`), an ident followed by `:` names a struct
	// field on T — NOT a reference to an outer-scope decl. Without type
	// info we cannot tell struct-literal from map-literal, so we skip
	// ident emission for both conservatively: struct keys must not bind
	// to same-name types; map keys-as-idents lose their ref (usually
	// keys are string literals anyway, so this is low cost).
	if b.compositeLitDepth > 0 && b.peekNonWSByte() == ':' {
		b.prevByte = 'i'
		return
	}

	// Inside a param list section, first ident is the param name.
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

	// Inside a type-params [T any, K comparable] — first ident per section.
	if b.inTypeParams && b.typeParamDepth == 1 && b.typeParamNeedsName {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
			kind: scope.KindType,
		})
		b.typeParamNeedsName = false
		b.prevByte = 'i'
		return
	}

	// Declaration context.
	if b.declContext != "" {
		kind := b.declContext
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		if kind == scope.KindFunction || kind == scope.KindMethod {
			b.paramListPending = true
			b.typeParamsPending = true
			// The function *name* just passed — any trailing '(' starts
			// params, not a receiver list. Clear the receiver-pending flag
			// so non-method functions don't mistake their params for a
			// receiver.
			b.funcReceiverPending = false
		} else if kind == scope.KindType {
			b.typeParamsPending = true
		}
		b.prevByte = 'i'
		return
	}

	// Struct/interface body: field or method declaration.
	// First ident per section at top depth = field (or method, if followed
	// by '(' in interface scope). Subsequent idents on the same line/section
	// are type refs. `,` re-enables needsName for multi-name fields.
	// Nested types (X []map[string]int) are handled by structDepth — idents
	// inside nested brackets go through the normal ref path.
	scopeK := b.currentScopeKind()
	if (scopeK == scope.ScopeClass || scopeK == scope.ScopeInterface) && b.structDepth == 0 {
		if b.structNeedsName {
			// Embedded type in interface: `io.Closer` — ident followed by
			// `.` is a qualified reference, not a method. Emit as ref.
			// Same in struct for embedded types, though struct embedding
			// is less common in this codebase.
			if b.peekNonWSByte() == '.' {
				b.emitRef(name, mkSpan(startByte, endByte))
				b.structNeedsName = false
				b.prevByte = 'i'
				return
			}
			// Method in interface: ident followed by `(` or `[` (generic).
			kind := scope.KindField
			if scopeK == scope.ScopeInterface {
				next := b.peekNonWSByte()
				if next == '(' || next == '[' {
					kind = scope.KindMethod
					// After a method decl, allow param list to open.
					b.paramListPending = true
					b.typeParamsPending = true
				}
			}
			b.emitDecl(name, kind, mkSpan(startByte, endByte))
			b.structNeedsName = false
			b.prevByte = 'i'
			return
		}
		// Subsequent ident on the same field/method line is a type ref.
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Short var LHS candidate: at statement start (or after `,` following
	// a prior candidate), an ident might be the LHS of `a, b := ...`.
	if wasStmtStart || (b.inShortVarLHS && b.prevByte == ',') {
		b.shortVarCandidates = append(b.shortVarCandidates, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
		})
		b.inShortVarLHS = true
		b.prevByte = 'i'
		return
	}

	// Otherwise: reference.
	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	prev := b.prevByte
	b.stmtStart = true
	b.prevByte = '{'

	// Explicit scope push (function/struct/interface body).
	if b.pendingScope != nil {
		kind := *b.pendingScope
		b.pendingScope = nil
		b.openScope(kind, uint32(b.s.Pos-1))
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
		return
	}

	// Control-flow block (`if cond {`, `for {`, `switch {`, etc.).
	if b.controlBlockExpected {
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		return
	}

	// Composite literal: `{` preceded by an ident (`T{...}`), a closing
	// bracket (`[]T{...}`, `map[K]V{...}`, `[N]T{...}`), or the close of
	// a struct/interface type decl (`struct{}{...}` — rare). Composite
	// literals do NOT introduce a scope; we track depth so handleIdent
	// can skip ident-key emission inside them.
	if prev == 'i' || prev == ']' || prev == '}' {
		b.compositeLitDepth++
		return
	}

	// Default: bare block at statement position.
	b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.prevByte = '}'
	if b.compositeLitDepth > 0 {
		b.compositeLitDepth--
	} else {
		b.closeTopScope(uint32(b.s.Pos))
	}
	b.stmtStart = true
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
			savedVarDeclKind:     b.varDeclKind,
			savedStructNeedsName: b.structNeedsName,
			savedStructDepth:     b.structDepth,
			ownerDeclIdx:         owner,
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
	b.varDeclKind = ""
	// Entering a new scope: reset struct-body state. A fresh struct or
	// interface scope starts with structNeedsName=true; any other scope
	// ignores the flag.
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
	// If this scope was owned by a decl (function, class, interface,
	// struct/type body), extend that decl's FullSpan to include the
	// closing brace. endByte is the byte position one past the '}'
	// (half-open range, matches Scope.Span convention).
	if o := e.Data.ownerDeclIdx; o >= 0 && o < len(b.res.Decls) {
		if b.res.Decls[o].FullSpan.EndByte < endByte {
			b.res.Decls[o].FullSpan.EndByte = endByte
		}
	}
	b.varDeclKind = e.Data.savedVarDeclKind
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

// peekNonWSByte returns the next non-whitespace, non-comment byte
// without advancing the scanner. Used for one-token lookahead (e.g.
// "is this ident a method or a field?" in an interface body).
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
	// Struct fields and interface methods live in the field namespace so
	// they do not shadow same-name top-level types/values during scope-
	// chain resolution. Refs to them via property access (obj.x) are
	// skipped at the tokenizer level, so they never come up as bare refs.
	if kind == scope.KindField || kind == scope.KindMethod {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			ns = scope.NSField
		}
	}
	// Canonical DeclID for file-scope decls — same identity across
	// every file in the same Go package, enabling cross-file rename
	// to match a caller's imported ref to the target decl by ID. For
	// nested scopes we keep the file to avoid collisions (scope IDs
	// are file-local).
	hashPath := b.file
	if scopeID == scope.ScopeID(1) && b.canonicalPath != "" {
		hashPath = b.canonicalPath
	}
	declID := hashDecl(hashPath, name, ns, scopeID)

	// FullSpan covers keyword → end of declaration. Scope-owning decls
	// (function, method, type, interface class/struct bodies) get
	// FullSpan.EndByte patched to the closing brace in closeTopScope.
	// Leaf decls (var/const/param/field/import) keep FullSpan = Span
	// since this pass does not track their statement end.
	//
	// pendingFullStart uses a +1 offset so 0 is unambiguously "unset"
	// (byte 0 is not actually reachable for Go decls — `package` comes
	// first — but keeping the convention uniform avoids surprise if
	// callers pipe in non-canonical fragments).
	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}

	// Go export rule: a top-level (file-scope) identifier whose first
	// rune is uppercase is visible to importers. The import-graph
	// resolver uses this flag to pick cross-file rewrite targets. Skip
	// KindImport (imports themselves aren't exported names) and anything
	// not at file scope (params, block locals, struct fields, interface
	// methods — these don't cross package boundaries via pkg.Name).
	exported := false
	if kind != scope.KindImport && b.currentScopeKind() == scope.ScopeFile && isExportedName(name) {
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

	// Track the most recent import alias decl so the upcoming path
	// string literal back-fills Signature on it.
	if kind == scope.KindImport {
		b.lastImportDeclIdx = idx
	}

	// Scope-owning decls: remember the decl index so the next openScope
	// can attach it and closeTopScope can patch FullSpan.EndByte.
	switch kind {
	case scope.KindFunction, scope.KindMethod, scope.KindType,
		scope.KindClass, scope.KindInterface:
		b.pendingOwnerDecl = idx
	}
	// Always clear pendingFullStart after the first consumer so a
	// later decl on a different statement does not pick up a stale
	// keyword position. Multi-ident block decls (var (...)) re-set it
	// at each line start via onStatementBoundary → blockDeclKind path.
	b.pendingFullStart = 0
}

// emitPropertyRef records a property-access ref (after `.`). Binding
// is BindProbable, Reason="property_access"; consumers match by name.
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
		if r.Binding.Reason == "property_access" {
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
			// Signature-position generics: for an unresolved ref, find a
			// KindType decl whose source position precedes the ref and
			// whose enclosing scope encloses the ref's byte range.
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
			if builtins.Go.Has(r.Name) {
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
	h.Write([]byte("<builtin:go>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}

// handleImportPath is called when the scanner consumes a string literal
// while declContext == KindImport. The literal is the Go import path
// (e.g. "net/http"). If an alias decl was just emitted (`alias "strings"`
// or `_ "pkg"`), we back-fill Signature onto that decl. Otherwise we
// synthesize a fresh KindImport decl named after the last path segment.
// Either way the Signature format matches the cross-language convention:
// "<importPath>\x00*" — Go imports bind the whole package, so the
// original-name slot is always "*" (see internal/scope/store/imports.go).
func (b *builder) handleImportPath(path string, startByte, endByte uint32) {
	sig := path + "\x00*"
	if b.lastImportDeclIdx >= 0 && b.lastImportDeclIdx < len(b.res.Decls) {
		d := &b.res.Decls[b.lastImportDeclIdx]
		if d.Kind == scope.KindImport {
			d.Signature = sig
			b.lastImportDeclIdx = -1
			return
		}
	}
	// No alias — emit a decl for the implicit binding name (last path
	// segment after the final '/'). Skip empty/ill-formed paths.
	name := lastPathSegment(path)
	if name == "" {
		return
	}
	span := scope.Span{StartByte: startByte, EndByte: endByte}
	b.emitDecl(name, scope.KindImport, span)
	// emitDecl stamped lastImportDeclIdx; back-fill Signature directly
	// so the reset on the next statement boundary doesn't lose it.
	if b.lastImportDeclIdx >= 0 && b.lastImportDeclIdx < len(b.res.Decls) {
		b.res.Decls[b.lastImportDeclIdx].Signature = sig
	}
	b.lastImportDeclIdx = -1
}

// lastPathSegment returns the substring after the final '/', or the
// whole input when there is no slash. Used to derive the default
// binding name for `import "net/http"` → "http".
func lastPathSegment(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// isExportedName reports whether a Go identifier is exported — i.e. its
// first rune is uppercase. Mirrors go/token.IsExported but avoids a
// dependency on that package.
func isExportedName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return false
	}
	return unicode.IsUpper(r)
}
