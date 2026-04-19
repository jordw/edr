// Package rust is the Rust scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single file.
// Handles file / function / block / struct / enum / trait / impl / mod
// scopes and fn / struct / enum / trait / impl / mod / const / static /
// type / let / param / type-param / field / import / macro_rules decls.
// Identifiers not in declaration position are emitted as Refs and
// resolved via scope-chain walk to the innermost matching Decl.
//
// Rust specifics vs Go:
//   - Path separator is `::`, not `.`. After `::` the next ident is a
//     property-access-style probable ref (treated identically to how Go
//     handles `pkg.X`). Method/field access via `.` also yields probable
//     refs. `self.X` inside an impl method resolves to the impl's target
//     type's NSField decls (same pattern as TS `this.X`).
//   - Lifetimes: `'a` is syntactically `'` + ident. Disambiguate from
//     char literals `'x'` by checking whether the char after the ident
//     is `'`. Lifetimes are emitted as KindType decls when declared in
//     generic param lists; lifetime refs are skipped.
//   - Attributes `#[derive(...)]` and `#![...]` are skipped entirely as
//     opaque balanced `[...]` regions — they do not produce decls or refs.
//   - Macro invocations `foo!(...)` and `macro_rules! foo { ... }` are
//     handled specially: `!` following an ident flags that ident as a
//     macro usage (its previous ref stays as a normal ref, not property
//     access); the macro body is scanned like any other balanced
//     delimiter but without introducing a lexical scope.
//   - Raw strings `r"..."`, `r#"..."#`, byte strings `b"..."`, `br#"..."#`
//     are recognized and skipped.
//
// v1 limitations:
//   - Complex patterns (tuple, slice, struct destructuring) only emit
//     the first binder. Subsequent binders are collected as refs.
//   - Limited trait resolution: `self.X` inside `impl Trait for T { }`
//     can reach trait-declared methods (including default impls) when
//     the trait is defined in the same file. Cross-file trait lookup
//     and external trait-bound dispatch are out of scope for v1.
//   - No lifetime refs; only lifetime decls in generic param lists.
//   - No procedural-macro expansion; macro bodies are opaque.
//   - No type inference: `let x = expr` emits x as KindVar, no type.
//   - Visibility modifiers (`pub`, `pub(crate)`) are parsed through.
//   - Composite struct literals `Point { x: 1 }` skip the key ident
//     (same concession as Go's composite-literal heuristic).
package rust

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Rust source buffer.
func Parse(file string, src []byte) *scope.Result {
	b := &builder{
		file:                 file,
		res:                  &scope.Result{File: file},
		s:                    lexkit.New(src),
		pendingOwnerDecl:     -1,
		pendingImplTargetIdx: -1,
		implForRefIdx:        -1,
	}
	b.openScope(scope.ScopeFile, 0)
	b.stmtStart = true
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	return b.res
}

type scopeEntry struct {
	kind                 scope.ScopeKind
	id                   scope.ScopeID
	savedStructNeedsName bool
	savedStructDepth     int
	// ownerDeclIdx is the index in res.Decls of the decl that owns this
	// scope. On close, that decl's FullSpan.EndByte is patched to the
	// closing brace position.
	ownerDeclIdx int
	// implTargetDeclIdx is the index in res.Decls of the struct/enum
	// decl this impl scope targets, or -1 if not applicable. Used by
	// self.X resolution.
	implTargetDeclIdx int
	// implTraitName is the simple trait name when this scope is an
	// `impl Trait for Type { }` body, or empty otherwise. Used by
	// self.X resolution to reach trait-declared methods (including
	// trait default impls not overridden in this impl block).
	implTraitName string
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	stmtStart bool

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push. Set by keywords (fn, struct, enum, trait, impl, mod).
	pendingScope *scope.ScopeKind
	// pendingScopeIsImpl is true when the pending scope came from `impl`.
	// Used to trigger impl-target resolution right before opening.
	pendingScopeIsImpl bool

	// declContext: next ident is a decl of this kind. Cleared after emit.
	declContext scope.DeclKind

	// pendingFullStart captures the byte position of the most recent
	// declaration keyword (fn, struct, enum, trait, impl, mod, const,
	// static, type, let, macro_rules). emitDecl uses it as
	// FullSpan.StartByte so the full span covers keyword → closing brace
	// for scope-owning decls.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last emitted
	// decl that owns an upcoming scope. Consumed by the next openScope.
	pendingOwnerDecl int

	// pendingImplTargetIdx is the index in res.Decls of the struct/enum
	// decl that an upcoming impl scope targets, or -1. Consumed by
	// openScope.
	pendingImplTargetIdx int

	// pendingImplTraitName is the simple trait name for an upcoming
	// `impl Trait for Type { }` body, or empty. Consumed by openScope.
	pendingImplTraitName string

	// implRefStartIdx is the index into res.Refs at which we started
	// collecting refs emitted between `impl` and the opening `{`. Used
	// to find the last ref that looks like the target type name.
	implRefStartIdx int

	// implForRefIdx is the index into res.Refs at the moment the `for`
	// keyword was seen inside an impl header (splitting `Trait` refs
	// from `Type` refs in `impl Trait for Type`). -1 when not in an
	// impl header or when the impl has no `for` clause.
	implForRefIdx int

	// paramListPending: after `fn name` (with optional generics), the
	// next `(` starts params.
	paramListPending      bool
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool

	// genericParamsPending: after `fn name`, `struct name`, `enum name`,
	// `trait name`, `impl`, `type name`, the next `<` is generics.
	genericParamsPending    bool
	inGenericParams         bool
	genericDepth            int
	genericSectionNeedsName bool

	pendingParams []pendingParam

	// inUse: `use` statement accumulates import decls until `;`.
	inUse bool
	// inUseGroup: inside `use foo::{a, b};` the `{...}` is a group, not a scope.
	inUseGroup bool
	// usePendingBind: the most recent ident in the current path segment.
	// Flushed as a KindImport decl on `;`, `,`, `}`, or `as`-alias.
	usePendingBind *pendingParam

	// inLetPattern: `let [mut] PAT = ...`. First ident on LHS is the binder.
	inLetPattern bool

	// matchArmPendingBind: match-arm pattern candidate ident(s). Flushed
	// as KindVar decls on `=>`. While inMatchArmPattern is true, lower-
	// case simple idents inside the match body are collected as potential
	// binders; it flips to false on `=>` (RHS is an expression body) and
	// back to true on `,` inside the match block (next arm starts).
	matchArmPendingBind []pendingParam
	inMatchBody         bool
	inMatchArmPattern   bool
	matchBodyDepth      int

	// structBody: inside a struct { X: T, Y: T } or enum variant with
	// named fields, the first ident in each comma-separated section is a
	// field decl; subsequent idents are type refs.
	structNeedsName bool
	structDepth     int

	// prevIdentIsSelf: last ident scanned was the keyword `self`. Used
	// to resolve `self.X` against the enclosing impl's target type.
	prevIdentIsSelf bool

	// compositeLitDepth: nested struct-literal `{}` depth. Used to skip
	// emitting idents that appear as keys inside `T { key: value }`.
	compositeLitDepth int

	// controlBlockExpected: set by if/else/while/for/loop/match/unsafe/move
	// keywords. The next `{` is a block body, not a struct literal.
	controlBlockExpected bool
	// matchBlockExpected: set by `match` keyword. The next `{` opens a
	// match body — inside, we enable match-arm pattern collection.
	matchBlockExpected bool

	// lastIdentEnd tracks the end-byte of the most recent ident; used to
	// detect `ident!` (macro invocation) reliably.
	lastIdentEnd uint32

	// macroCallPending: the previous ident was `foo` and we've just seen
	// `!`. The next delimiter `(`/`[`/`{` opens a macro body that should
	// NOT introduce a new lexical scope.
	macroCallPending bool

	// macroDelimStack: stack of macro-body delimiters we're currently inside.
	// While non-empty, `{` does not push a scope and `}` does not close one.
	macroDelimStack []byte

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
			b.skipNestedBlockComment()
		case c == '"':
			b.scanString()
			b.stmtStart = false
			b.prevByte = '"'
		case c == 'b' && (b.s.PeekAt(1) == '"' || (b.s.PeekAt(1) == 'r' && (b.s.PeekAt(2) == '"' || b.s.PeekAt(2) == '#'))):
			// Byte string `b"..."` or raw byte string `br"..."` / `br#"..."#`.
			// Guard: `b` must not be part of a longer ident (prev token
			// should not be an ident-cont byte).
			if !isIdentContByte(b.prevByte) {
				b.s.Pos++ // consume 'b'
				if b.s.Peek() == 'r' {
					b.s.Pos++
					b.scanRawString()
				} else {
					b.scanString()
				}
				b.stmtStart = false
				b.prevByte = '"'
			} else {
				word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				b.handleIdent(word)
			}
		case c == 'r' && (b.s.PeekAt(1) == '"' || b.s.PeekAt(1) == '#'):
			// Raw string: r"..." or r#"..."#.
			if !isIdentContByte(b.prevByte) {
				b.s.Pos++ // consume 'r'
				b.scanRawString()
				b.stmtStart = false
				b.prevByte = '"'
			} else {
				word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				b.handleIdent(word)
			}
		case c == '\'':
			b.scanCharOrLifetime()
		case c == '#':
			// Attribute: #[...] or #![...]. Skip balanced [...].
			b.s.Pos++
			if b.s.Peek() == '!' {
				b.s.Pos++
			}
			if b.s.Peek() == '[' {
				b.skipBalancedBracket()
			}
			b.prevByte = ']'
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == ';':
			b.s.Pos++
			b.flushUsePendingBind()
			b.onStatementBoundary()
			b.prevByte = ';'
		case c == '(':
			b.s.Pos++
			b.prevByte = '('
			if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
				b.structDepth++
			}
			if b.paramListPending {
				b.paramListPending = false
				b.genericParamsPending = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.inParamList {
				b.paramDepth++
			}
			if b.macroCallPending || len(b.macroDelimStack) > 0 {
				b.macroCallPending = false
				b.macroDelimStack = append(b.macroDelimStack, '(')
			}
		case c == ')':
			b.s.Pos++
			b.prevByte = ')'
			if sk := b.currentScopeKind(); (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth > 0 {
				b.structDepth--
			}
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
				}
			}
			if n := len(b.macroDelimStack); n > 0 && b.macroDelimStack[n-1] == '(' {
				b.macroDelimStack = b.macroDelimStack[:n-1]
			}
		case c == '[':
			b.s.Pos++
			b.prevByte = '['
			if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
				b.structDepth++
			}
			if b.macroCallPending || len(b.macroDelimStack) > 0 {
				b.macroCallPending = false
				b.macroDelimStack = append(b.macroDelimStack, '[')
			}
		case c == ']':
			b.s.Pos++
			b.prevByte = ']'
			if sk := b.currentScopeKind(); (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth > 0 {
				b.structDepth--
			}
			if n := len(b.macroDelimStack); n > 0 && b.macroDelimStack[n-1] == '[' {
				b.macroDelimStack = b.macroDelimStack[:n-1]
			}
		case c == '<':
			b.s.Pos++
			b.prevByte = '<'
			if b.genericParamsPending {
				b.genericParamsPending = false
				b.inGenericParams = true
				b.genericDepth = 1
				b.genericSectionNeedsName = true
			} else if b.inGenericParams {
				b.genericDepth++
			}
			// Track angle-bracket nesting as struct-body depth so that
			// commas inside `Vec<String, usize>` at field-type position
			// do not re-enable structNeedsName.
			if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
				b.structDepth++
			}
		case c == '>':
			b.s.Pos++
			b.prevByte = '>'
			if b.inGenericParams {
				b.genericDepth--
				if b.genericDepth == 0 {
					b.inGenericParams = false
					b.genericSectionNeedsName = false
				}
			}
			if sk := b.currentScopeKind(); (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth > 0 {
				b.structDepth--
			}
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
			} else if b.inGenericParams && b.genericDepth == 1 {
				b.genericSectionNeedsName = true
			}
			sk := b.currentScopeKind()
			if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
				b.structNeedsName = true
			}
			if b.inUse {
				b.flushUsePendingBind()
			}
			// In a match body at arm-delimiter depth, the `,` ends an arm
			// expression and the next arm begins with another pattern.
			if b.inMatchBody && b.matchBodyDepth == 1 {
				b.inMatchArmPattern = true
			}
		case c == ':' && b.s.PeekAt(1) == ':':
			// `::` path separator. Emit the next ident as a property-access
			// probable ref.
			b.s.Advance(2)
			b.prevByte = '.' // re-use '.' marker to trigger property-access path
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == '=' && b.s.PeekAt(1) == '>':
			// Match-arm `PAT => body`. Flush pending binders as decls.
			b.s.Advance(2)
			b.prevByte = '>'
			if len(b.matchArmPendingBind) > 0 {
				for _, p := range b.matchArmPendingBind {
					b.emitDecl(p.name, scope.KindVar, p.span)
				}
				b.matchArmPendingBind = nil
			}
			if b.inMatchBody {
				b.inMatchArmPattern = false
			}
		case c == '=':
			b.s.Pos++
			b.prevByte = '='
			b.inLetPattern = false
		case c == '!':
			b.s.Pos++
			b.prevByte = '!'
			// Macro invocation: only if `!` immediately follows an ident
			// (no whitespace). lastIdentEnd==b.s.Pos-1 means the `!` was
			// glued to the ident.
			if b.lastIdentEnd > 0 && b.lastIdentEnd == uint32(b.s.Pos-1) {
				b.macroCallPending = true
			}
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				p := b.s.Peek()
				if lexkit.IsASCIIDigit(p) || p == '.' || p == '_' ||
					p == 'x' || p == 'e' || p == 'b' || p == 'o' ||
					(p >= 'A' && p <= 'F') || (p >= 'a' && p <= 'f') {
					b.s.Pos++
					continue
				}
				break
			}
			b.stmtStart = false
			b.prevByte = '0'
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

func isIdentContByte(c byte) bool {
	return lexkit.DefaultIdentCont[c]
}

// skipNestedBlockComment handles Rust's nested /* /* */ */ comments.
func (b *builder) skipNestedBlockComment() {
	depth := 1
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		if c == '/' && b.s.PeekAt(1) == '*' {
			b.s.Advance(2)
			depth++
			continue
		}
		if c == '*' && b.s.PeekAt(1) == '/' {
			b.s.Advance(2)
			depth--
			continue
		}
		b.s.Next()
	}
}

// scanString consumes a standard `"..."` with escape sequences.
func (b *builder) scanString() {
	b.s.Pos++ // opening quote
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '\\' {
			b.s.Advance(2)
			continue
		}
		if c == '"' {
			b.s.Pos++
			return
		}
		b.s.Next()
	}
}

// scanRawString consumes a raw string `r#...#"..."#...#`. Called with
// the scanner positioned at the first `#` or `"` after the `r` (or `br`).
func (b *builder) scanRawString() {
	hashes := 0
	for b.s.Peek() == '#' {
		hashes++
		b.s.Pos++
	}
	if b.s.Peek() != '"' {
		return // malformed
	}
	b.s.Pos++ // opening quote
	for !b.s.EOF() {
		if b.s.Peek() == '"' {
			save := b.s.Pos
			b.s.Pos++
			ok := true
			for i := 0; i < hashes; i++ {
				if b.s.Peek() != '#' {
					ok = false
					break
				}
				b.s.Pos++
			}
			if ok {
				return
			}
			b.s.Pos = save + 1
			continue
		}
		b.s.Next()
	}
}

// scanCharOrLifetime disambiguates char literals from lifetime labels.
// - `'c'`, `'\n'`, `'\xFF'`: char literal.
// - `'a`: lifetime (no closing `'`).
func (b *builder) scanCharOrLifetime() {
	startPos := b.s.Pos
	b.s.Pos++ // consume opening '
	// Escaped char literal: always consume until closing '.
	if b.s.Peek() == '\\' {
		b.s.Advance(2)
		for !b.s.EOF() && b.s.Peek() != '\'' {
			b.s.Next()
		}
		if !b.s.EOF() {
			b.s.Pos++
		}
		b.stmtStart = false
		b.prevByte = '\''
		return
	}
	// Empty `''` (invalid but defensive): consume and return.
	if b.s.Peek() == '\'' {
		b.s.Pos++
		b.stmtStart = false
		b.prevByte = '\''
		return
	}
	// Read potential ident run.
	wordStart := b.s.Pos
	if !b.s.EOF() && lexkit.DefaultIdentStart[b.s.Peek()] {
		for !b.s.EOF() && lexkit.DefaultIdentCont[b.s.Peek()] {
			b.s.Pos++
		}
	} else if !b.s.EOF() {
		// Single non-ident char like '1' or '('. Treat as char literal.
		b.s.Next()
	}
	// If a closing `'` follows, this is a char literal.
	if b.s.Peek() == '\'' {
		b.s.Pos++
		b.stmtStart = false
		b.prevByte = '\''
		return
	}
	// Lifetime. Emit as KindType decl if we're in a generic-param name slot.
	// Include the leading `'` in the name so it is recognizable as a lifetime
	// and does not collide with same-name type params.
	if b.inGenericParams && b.genericDepth == 1 && b.genericSectionNeedsName {
		if b.s.Pos > wordStart {
			name := string(b.s.Src[startPos:b.s.Pos])
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: name,
				span: mkSpan(uint32(startPos), uint32(b.s.Pos)),
				kind: scope.KindType,
			})
			b.genericSectionNeedsName = false
		}
	}
	b.stmtStart = false
	b.prevByte = 'i'
}

// skipBalancedBracket consumes a `[...]` block balanced across `[` and `]`.
// Scanner is positioned at the opening `[`.
func (b *builder) skipBalancedBracket() {
	if b.s.Peek() != '[' {
		return
	}
	b.s.Pos++
	depth := 1
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		switch c {
		case '[':
			depth++
			b.s.Pos++
		case ']':
			depth--
			b.s.Pos++
		case '"':
			b.scanString()
		case '\'':
			b.scanCharOrLifetime()
		case '/':
			if b.s.PeekAt(1) == '/' {
				b.s.SkipLineComment()
			} else if b.s.PeekAt(1) == '*' {
				b.s.Advance(2)
				b.skipNestedBlockComment()
			} else {
				b.s.Pos++
			}
		default:
			b.s.Next()
		}
	}
}

func (b *builder) onStatementBoundary() {
	b.stmtStart = true
	b.declContext = ""
	sk := b.currentScopeKind()
	if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
		b.structNeedsName = true
	}
	if b.prevByte == ';' {
		b.inUse = false
		b.inUseGroup = false
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
	b.lastIdentEnd = endByte
	wasQualifiedAccess := b.prevByte == '.'

	// Keywords.
	switch name {
	case "fn":
		// Inside an impl or trait body, `fn` declares a method; elsewhere
		// it declares a free function. Method decls get NSField namespace
		// so they don't shadow same-name top-level decls on scope walks.
		sk := b.currentScopeKind()
		if sk == scope.ScopeClass || sk == scope.ScopeInterface {
			b.declContext = scope.KindMethod
		} else {
			b.declContext = scope.KindFunction
		}
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingScopeIsImpl = false
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "struct":
		b.declContext = scope.KindType
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingScopeIsImpl = false
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingScopeIsImpl = false
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "trait":
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		b.pendingScopeIsImpl = false
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "impl":
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingScopeIsImpl = true
		b.pendingFullStart = startByte + 1
		b.genericParamsPending = true
		b.implRefStartIdx = len(b.res.Refs)
		b.implForRefIdx = -1
		b.prevByte = 'k'
		return
	case "mod":
		b.declContext = scope.KindNamespace
		k := scope.ScopeNamespace
		b.pendingScope = &k
		b.pendingScopeIsImpl = false
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "const":
		b.declContext = scope.KindConst
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "static":
		b.declContext = scope.KindVar
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "type":
		if wasStmtStart || b.prevByte == ';' || b.prevByte == '{' {
			b.declContext = scope.KindType
			b.genericParamsPending = true
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "let":
		b.declContext = scope.KindVar
		b.pendingFullStart = startByte + 1
		b.inLetPattern = true
		b.prevByte = 'k'
		return
	case "mut", "ref":
		// Modifiers — next ident is still the binder.
		b.prevByte = 'k'
		return
	case "use":
		b.inUse = true
		b.declContext = scope.KindImport
		b.prevByte = 'k'
		return
	case "macro_rules":
		// `macro_rules! name { ... }`. Suppress treating the following
		// `!` as marking the `macro_rules` ident as a macro call (which
		// we haven't emitted since it's a keyword anyway).
		b.declContext = scope.KindFunction
		b.pendingFullStart = startByte + 1
		b.lastIdentEnd = 0
		b.prevByte = 'k'
		return
	case "as":
		// In `use foo as bar;` the next ident replaces the binding, so
		// discard the current pending-bind (`foo`) and let the alias
		// ident overwrite it. In `x as T` cast expressions, `as` is a
		// no-op here — the following ident emits as a normal ref.
		if b.inUse {
			b.usePendingBind = nil
		}
		b.prevByte = 'k'
		return
	case "if", "else", "while", "for", "loop", "unsafe", "move", "async":
		// Inside an impl header (between `impl` and `{`), `for` splits
		// the trait name from the target type. Record the split point so
		// resolveImplTarget can pick out the trait separately.
		if name == "for" && b.pendingScopeIsImpl && b.implRefStartIdx >= 0 {
			b.implForRefIdx = len(b.res.Refs)
			b.prevByte = 'k'
			return
		}
		b.controlBlockExpected = true
		b.prevByte = 'k'
		return
	case "match":
		b.controlBlockExpected = true
		b.matchBlockExpected = true
		b.prevByte = 'k'
		return
	case "return", "break", "continue", "in", "where", "dyn", "pub", "crate",
		"super", "Self", "true", "false", "await", "yield", "box", "extern":
		b.prevIdentIsSelf = false
		b.prevByte = 'k'
		return
	case "self":
		if b.inParamList && b.paramDepth == 1 && b.paramSectionNeedsName {
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: "self",
				span: mkSpan(startByte, endByte),
				kind: scope.KindParam,
			})
			b.paramSectionNeedsName = false
		}
		b.prevIdentIsSelf = true
		b.prevByte = 'k'
		return
	}

	// Inside a use statement, path segments (including after `::`) are
	// import-candidate binders; handle before the generic property-access
	// branch so `use foo::Bar;` captures `Bar` as the binder.
	if b.inUse {
		b.usePendingBind = &pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
		}
		b.prevByte = 'i'
		return
	}

	// Property/path access: `.X` or `::X`.
	if wasQualifiedAccess {
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

	// Composite-literal key: `T { key: value }` — skip ident followed by `:`
	// but not `::`.
	if b.compositeLitDepth > 0 && b.peekColonNotColonColon() {
		b.prevByte = 'i'
		return
	}

	// Generic type params — first ident per section.
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

	// Param list: first ident per section.
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

	// Declaration context: fn/struct/enum/trait/mod/type/let/const/static/
	// macro_rules/use name.
	if b.declContext != "" {
		kind := b.declContext
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		switch kind {
		case scope.KindFunction, scope.KindMethod:
			b.paramListPending = true
			b.genericParamsPending = true
		case scope.KindType, scope.KindEnum, scope.KindInterface:
			b.genericParamsPending = true
		}
		// If we emitted a method inside a trait/impl body, clear the
		// struct-body needs-name flag so subsequent idents on the same
		// line (return type, etc.) are not treated as field names.
		if kind == scope.KindMethod || kind == scope.KindFunction {
			b.structNeedsName = false
		}
		b.prevByte = 'i'
		return
	}

	// Struct/enum/trait body: field/variant/method decl at top depth.
	sk := b.currentScopeKind()
	if (sk == scope.ScopeClass || sk == scope.ScopeInterface) && b.structDepth == 0 {
		if b.structNeedsName {
			kind := scope.KindField
			if sk == scope.ScopeInterface {
				next := b.peekNonWSByte()
				if next == '(' || next == '<' {
					kind = scope.KindMethod
					b.paramListPending = true
					b.genericParamsPending = true
				}
			}
			b.emitDecl(name, kind, mkSpan(startByte, endByte))
			b.structNeedsName = false
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Match-arm pattern candidate: any lowercase simple ident in the
	// pattern portion of a match arm (before the `=>`) is a potential
	// binder. We collect all such candidates and flush as KindVar decls
	// on `=>`. Uppercase-start idents (variant constructors like `Some`,
	// `None`, path constructors) fall through as regular refs.
	if b.inMatchBody && b.inMatchArmPattern && isLowerStart(name) {
		b.matchArmPendingBind = append(b.matchArmPendingBind, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
		})
		b.prevByte = 'i'
		return
	}

	// Default: reference.
	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

func isLowerStart(s string) bool {
	if len(s) == 0 {
		return false
	}
	c := s[0]
	return (c >= 'a' && c <= 'z') || c == '_'
}

func (b *builder) peekColonNotColonColon() bool {
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
			b.skipNestedBlockComment()
			continue
		}
		if c == ':' && b.s.PeekAt(1) != ':' {
			return true
		}
		return false
	}
	return false
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
			b.skipNestedBlockComment()
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

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	prev := b.prevByte
	b.stmtStart = true
	b.prevByte = '{'

	// Macro body — do NOT push a scope.
	if b.macroCallPending || len(b.macroDelimStack) > 0 {
		b.macroCallPending = false
		b.macroDelimStack = append(b.macroDelimStack, '{')
		return
	}

	// `use foo::{a, b};` — group braces are not a scope.
	if b.inUse {
		b.inUseGroup = true
		return
	}

	// Pending scope push (fn/struct/enum/trait/impl/mod body).
	if b.pendingScope != nil {
		kind := *b.pendingScope
		isImpl := b.pendingScopeIsImpl
		b.pendingScope = nil
		b.pendingScopeIsImpl = false
		b.controlBlockExpected = false
		b.matchBlockExpected = false

		if isImpl {
			// Resolve impl target: find the last ref emitted between the
			// `impl` keyword and this `{`, then match its name against
			// top-level struct/enum decls.
			b.resolveImplTarget()
		}

		b.openScope(kind, uint32(b.s.Pos-1))
		// Flush pending params into the new scope.
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

	// match body.
	if b.matchBlockExpected {
		b.matchBlockExpected = false
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		b.inMatchBody = true
		b.inMatchArmPattern = true
		b.matchBodyDepth = 1
		return
	}

	// Control-flow block.
	if b.controlBlockExpected {
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		if b.inMatchBody {
			b.matchBodyDepth++
		}
		return
	}

	// Composite literal: `T { ... }`, `Foo::Bar { ... }`, `Vec<T> { ... }`.
	// Does NOT introduce a scope.
	if prev == 'i' || prev == '>' || prev == ']' {
		b.compositeLitDepth++
		return
	}

	// Default: bare block (e.g., `{ let x = 1; x }`).
	b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
	if b.inMatchBody {
		b.matchBodyDepth++
	}
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.prevByte = '}'
	b.stmtStart = true

	// Macro body close.
	if n := len(b.macroDelimStack); n > 0 && b.macroDelimStack[n-1] == '{' {
		b.macroDelimStack = b.macroDelimStack[:n-1]
		return
	}

	if b.inUseGroup {
		b.flushUsePendingBind()
		b.inUseGroup = false
		return
	}

	if b.compositeLitDepth > 0 {
		b.compositeLitDepth--
		return
	}

	if len(b.matchArmPendingBind) > 0 {
		for _, p := range b.matchArmPendingBind {
			b.emitRef(p.name, p.span)
		}
		b.matchArmPendingBind = nil
	}

	if b.inMatchBody {
		b.matchBodyDepth--
		if b.matchBodyDepth <= 0 {
			b.inMatchBody = false
			b.matchBodyDepth = 0
		}
	}

	b.closeTopScope(uint32(b.s.Pos))
}

// resolveImplTarget walks refs emitted since the `impl` keyword, finds
// the last one that could be the target type name (most recent type
// ident before `{` or `for`), and matches it against file-scope decls
// to record pendingImplTargetIdx. `impl Trait for Type` emits both
// `Trait` and `Type` as refs in that order; the second is the target.
func (b *builder) resolveImplTarget() {
	if b.implRefStartIdx < 0 || b.implRefStartIdx > len(b.res.Refs) {
		return
	}
	// When `for` was seen in the impl header, refs before it are the
	// trait path (last one = simple trait name), refs after are the
	// target type path. Otherwise the single path is the target type.
	traitSliceEnd := len(b.res.Refs)
	targetSliceStart := b.implRefStartIdx
	if b.implForRefIdx >= b.implRefStartIdx && b.implForRefIdx <= len(b.res.Refs) {
		traitSliceEnd = b.implForRefIdx
		targetSliceStart = b.implForRefIdx
	} else {
		// No `for`: no trait.
		traitSliceEnd = b.implRefStartIdx
	}
	// Simple trait name: last ref before `for`.
	var trait string
	if traitSliceEnd > b.implRefStartIdx {
		traitSlice := b.res.Refs[b.implRefStartIdx:traitSliceEnd]
		for i := len(traitSlice) - 1; i >= 0; i-- {
			n := traitSlice[i].Name
			if n == "" {
				continue
			}
			trait = n
			break
		}
	}
	// Simple target type name: last ref after `for` (or last ref overall
	// if no `for`).
	var target string
	targetSlice := b.res.Refs[targetSliceStart:]
	for i := len(targetSlice) - 1; i >= 0; i-- {
		n := targetSlice[i].Name
		if n == "" {
			continue
		}
		target = n
		break
	}
	b.implRefStartIdx = -1
	b.implForRefIdx = -1
	b.pendingImplTraitName = trait
	if target == "" {
		return
	}
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		if d.Scope != 1 {
			continue
		}
		if d.Namespace != scope.NSType {
			continue
		}
		if d.Name == target {
			b.pendingImplTargetIdx = i
			return
		}
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
	implTarget := b.pendingImplTargetIdx
	b.pendingImplTargetIdx = -1
	implTrait := b.pendingImplTraitName
	b.pendingImplTraitName = ""
	b.stack.Push(lexkit.Scope[scopeEntry]{
		Data: scopeEntry{
			kind:                 kind,
			id:                   id,
			savedStructNeedsName: b.structNeedsName,
			savedStructDepth:     b.structDepth,
			ownerDeclIdx:         owner,
			implTargetDeclIdx:    implTarget,
			implTraitName:        implTrait,
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
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
	switch kind {
	case scope.KindType, scope.KindEnum, scope.KindInterface, scope.KindClass:
		ns = scope.NSType
	case scope.KindNamespace:
		ns = scope.NSNamespace
	case scope.KindField, scope.KindMethod:
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
	case scope.KindFunction, scope.KindMethod, scope.KindType,
		scope.KindEnum, scope.KindInterface, scope.KindClass,
		scope.KindNamespace:
		b.pendingOwnerDecl = idx
	}
	b.pendingFullStart = 0
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

// flushUsePendingBind emits the current use-segment's pending binder as
// a KindImport decl and clears it.
func (b *builder) flushUsePendingBind() {
	if b.usePendingBind == nil {
		return
	}
	p := *b.usePendingBind
	b.usePendingBind = nil
	b.emitDecl(p.name, scope.KindImport, p.span)
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

// tryResolveSelfField attempts to resolve `self.X` against the enclosing
// impl's target type's NSField decls. Returns true if a binding was made.
// Mirrors TS tryResolveThisField.
func (b *builder) tryResolveSelfField(name string, span scope.Span) bool {
	entries := b.stack.Entries()
	// Find the innermost enclosing impl (ScopeClass with implTargetDeclIdx)
	// or trait body (ScopeInterface). This is the "method container" whose
	// own members we try first.
	var container scope.ScopeID
	containerIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i].Data
		if e.kind == scope.ScopeClass && e.implTargetDeclIdx >= 0 {
			container = e.id
			containerIdx = i
			break
		}
		if e.kind == scope.ScopeInterface {
			container = e.id
			containerIdx = i
			break
		}
	}
	emit := func(d *scope.Decl, reason string) bool {
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
				Reason: reason,
			},
		})
		return true
	}
	// 1) Methods (or fields, for interfaces) defined directly in the
	// enclosing impl/trait body.
	if container != 0 {
		for i := range b.res.Decls {
			d := &b.res.Decls[i]
			if d.Scope != container || d.Namespace != scope.NSField || d.Name != name {
				continue
			}
			return emit(d, "self_dot_field")
		}
	}
	// 2) Fields of the impl target struct/enum (existing behavior).
	if containerIdx >= 0 && entries[containerIdx].Data.implTargetDeclIdx >= 0 {
		targetIdx := entries[containerIdx].Data.implTargetDeclIdx
		if targetIdx < len(b.res.Decls) {
			targetDecl := &b.res.Decls[targetIdx]
			// The target struct/enum's body scope is a ScopeClass whose span
			// starts at or after the target decl's Span.EndByte and is
			// contained within targetDecl.FullSpan.
			var targetScope scope.ScopeID
			for i := range b.res.Scopes {
				sc := &b.res.Scopes[i]
				if sc.Kind != scope.ScopeClass {
					continue
				}
				if sc.Span.StartByte < targetDecl.Span.EndByte {
					continue
				}
				if targetDecl.FullSpan.EndByte > 0 && sc.Span.StartByte >= targetDecl.FullSpan.EndByte {
					continue
				}
				targetScope = sc.ID
				break
			}
			if targetScope != 0 {
				for i := range b.res.Decls {
					d := &b.res.Decls[i]
					if d.Scope != targetScope || d.Namespace != scope.NSField || d.Name != name {
						continue
					}
					return emit(d, "self_dot_field")
				}
			}
		}
	}
	// 3) Trait-declared methods: if this is an `impl Trait for Type` body,
	// find the trait decl (same file) and look inside its body for an
	// NSField method by this name. Same-file only; cross-file trait
	// lookup is out of scope for v1.
	if containerIdx >= 0 {
		traitName := entries[containerIdx].Data.implTraitName
		if traitName != "" {
			var traitScope scope.ScopeID
			for i := range b.res.Decls {
				d := &b.res.Decls[i]
				if d.Scope != 1 || d.Kind != scope.KindInterface || d.Name != traitName {
					continue
				}
				// Find the trait's body scope: first ScopeInterface whose
				// span starts at/after the trait decl's ident span end and
				// is contained within its FullSpan.
				for j := range b.res.Scopes {
					sc := &b.res.Scopes[j]
					if sc.Kind != scope.ScopeInterface {
						continue
					}
					if sc.Span.StartByte < d.Span.EndByte {
						continue
					}
					if d.FullSpan.EndByte > 0 && sc.Span.StartByte >= d.FullSpan.EndByte {
						continue
					}
					traitScope = sc.ID
					break
				}
				break
			}
			if traitScope != 0 {
				for i := range b.res.Decls {
					d := &b.res.Decls[i]
					if d.Scope != traitScope || d.Namespace != scope.NSField || d.Name != name {
						continue
					}
					return emit(d, "trait_method")
				}
			}
		}
	}
	return false
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
	tryNS := func(r *scope.Ref, ns scope.Namespace) bool {
		cur := r.Scope
		for {
			if d, ok := byKey[key{scope: cur, name: r.Name, ns: ns}]; ok {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   d.ID,
					Reason: "direct_scope",
				}
				return true
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
					return true
				}
				break
			}
			if cur == 0 {
				break
			}
			cur = p
		}
		return false
	}
	for i := range b.res.Refs {
		r := &b.res.Refs[i]
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "self_dot_field" || r.Binding.Reason == "trait_method" {
			continue
		}
		if tryNS(r, r.Namespace) {
			continue
		}
		// Rust doesn't clearly split type/value namespaces at use sites —
		// `Foo` in `Foo::new()` is a type, but in `let x = Foo;` it's a
		// value. Try the other namespace as a fallback.
		var other scope.Namespace
		if r.Namespace == scope.NSValue {
			other = scope.NSType
		} else {
			other = scope.NSValue
		}
		if tryNS(r, other) {
			continue
		}
		// Signature-position generic refs: find a KindType decl that
		// precedes the ref and whose owning scope encloses the ref's span.
		resolved := false
		for j := range b.res.Decls {
			d := &b.res.Decls[j]
			if d.Kind != scope.KindType || d.Name != r.Name {
				continue
			}
			if d.Namespace != scope.NSType {
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
		if resolved {
			continue
		}
		if builtins.Rust.Has(r.Name) {
			r.Binding = scope.RefBinding{
				Kind:   scope.BindResolved,
				Decl:   hashBuiltinDecl(r.Name),
				Reason: "builtin",
			}
			continue
		}
		r.Binding = scope.RefBinding{
			Kind:   scope.BindUnresolved,
			Reason: "missing_import",
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
	h.Write([]byte("<builtin:rust>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
