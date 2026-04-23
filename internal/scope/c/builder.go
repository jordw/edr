// Package c is the C scope + binding extractor.
//
// Built on lexkit tokens; produces scope.Result for a single file.
// Serves both .c and .h files — the dispatcher routes both here.
// Handles file / function / block / struct / union / for scopes and
// var / const / func / type / import / param / field decls.
// Identifiers not in declaration position are emitted as Refs and
// resolved via scope-chain walk to the innermost matching Decl.
//
// Compared to Go, C is harder because:
//   - No `func` keyword: functions are `<types> name(params) { ... }`
//     and must be distinguished from variable decls and calls by
//     peeking past `)` for `{` (def) or `;` (decl).
//   - Declarator syntax is recursive (pointers, arrays, function
//     pointers) — we parse through it conservatively.
//   - Preprocessor directives begin with `#` and end at the newline
//     (unless line-continued with `\`). They are treated as
//     statement-level tokens; only `#define` and `#include` emit decls.
//
// v1 limitations (intentional deferrals; documented as a package
// comment since we do not add new DeclKinds to scope.types):
//   - `#define FOO value` emits FOO as KindConst (no KindMacro exists
//     in scope.types; KindConst is the closest match for both object-
//     and function-like macros).
//   - Full C preprocessor semantics (conditional compilation, macro
//     expansion, macro-argument substitution) are NOT modeled. Code
//     inside `#ifdef/#endif` is still parsed.
//   - Typedef chains do not affect later parses (we emit the alias as
//     KindType but do not track that "FooT" means "struct Foo").
//   - Old K&R-style function declarations are NOT supported.
//   - GCC extensions (`__attribute__`, statement expressions `({...})`),
//     C11 _Generic, and C11 anonymous struct/union members are parsed
//     through but their special semantics are ignored.
//   - Function-pointer typedefs/variables: the "name" inside
//     `(*name)(args)` is not extracted; we rely on surrounding idents
//     instead (so `typedef int (*Fn)(int);` currently emits Fn as a
//     ref rather than a decl — a known gap).
//   - Composite literals `(struct T){ .x = 1 }` are parsed as blocks;
//     field-designator idents inside `{}` may be misidentified as refs.
package c

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a C source buffer. Works for both
// .c and .h files; the dispatcher routes both here.
// Parse extracts a scope.Result from a C source buffer. File-scope
// decls hash with the file path.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path used to
// hash exported file-scope decls. When canonicalPath is non-empty,
// every exported file-scope decl hashes with it instead of the file
// path — so `int compute(int)` in foo.c and `int compute(int);` in
// foo.h share a DeclID when both files map to the same canonical
// path (convention: dir + basename-without-extension). Static decls
// and nested-scope decls keep the file path.
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

type scopeEntry struct {
	kind                 scope.ScopeKind
	id                   scope.ScopeID
	savedStructNeedsName bool
	savedStructDepth     int
	savedInTypedef       bool
	// ownerDeclIdx is the index in res.Decls of the decl that owns this
	// scope (e.g., for a function scope, the func decl). -1 if none.
	// On scope close, FullSpan.EndByte for that decl is patched.
	ownerDeclIdx int
}

type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind
}

// lastIdent is a small record of the most recent identifier we saw
// at the top level of a statement. Used to decide "is this a function
// name or a call?" when the next token is `(`.
type lastIdent struct {
	name  string
	span  scope.Span
	// stmtStart records whether this ident began at statement start.
	// Only stmt-start idents can be function names / var decl idents;
	// anything else is a type token or type ref.
	atStmtStart bool
}

type builder struct {
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	// stmtStart is true at the beginning of a statement (after ';', '{',
	// '}', or a newline that was a statement boundary). Used to distinguish
	// declarations from expressions.
	stmtStart bool

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push. Set by `struct Name {`, `union Name {`, `enum Name {`,
	// function-definition recognition, and control-flow keywords.
	pendingScope *scope.ScopeKind

	// pendingFullStart is the byte where the in-progress declaration
	// started (statement start or declaration keyword). emitDecl uses
	// it as FullSpan.StartByte. Zero means "unset"; we store start+1
	// so byte 0 stays unambiguous.
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last emitted
	// decl that owns an upcoming scope. Consumed by the next openScope
	// call so closeTopScope can patch FullSpan.EndByte.
	pendingOwnerDecl int

	// At statement start, we collect idents as "candidates" until we
	// see a delimiter that tells us what kind of declaration/expression
	// this is:
	//   - `(` after idents: last ident is a function (decl or def), and
	//     the rest before it are return-type tokens.
	//   - `;`, `=`, `[`, `,` after idents: last ident is a variable,
	//     and the rest are type tokens.
	//   - `{` after a `struct/union/enum Name` keyword sequence: open
	//     a type body.
	stmtIdents []lastIdent

	// typeKeywordPending: after `struct`, `union`, or `enum`, the next
	// ident is a type name (and if followed by `{`, a type body opens).
	// Cleared once we see `{`, `;`, or emit the decl.
	typeKeywordPending scope.ScopeKind // ScopeClass / "union" / ScopeBlock-ish sentinel
	typeKeywordIsEnum  bool
	typeKeywordSeen    bool

	// inTypedef: we're inside a `typedef ...;` statement. The last ident
	// before `;` (or before the next `,` at statement level) is a type
	// alias.
	inTypedef bool

	// paramListPending / inParamList: set when we think we're entering
	// a function parameter list. The `(` right after a function name
	// starts params.
	paramListPending bool
	inParamList      bool
	paramDepth       int
	// paramSectionNeedsName and paramSectionIdents collect idents in a
	// param "section" (between `,` or `(` and the next `,` or `)`).
	// The last ident in a section that's followed by `)` or `,` is the
	// parameter name; earlier idents are type tokens.
	paramSectionIdents []lastIdent

	// After a param list closes, this captures whether the next token
	// is `{` (function definition) or `;` (declaration).
	funcAwaitingBody bool
	// funcNameIdx is the index in pendingParams of the associated
	// function name (its span) so we can emit properly when we see {.
	funcDef pendingFuncDef

	// pendingParams collects params for a pending function/method.
	pendingParams []pendingParam

	// structNeedsName: at top depth of a struct/union body, the first
	// ident in each section is a field name; subsequent idents are
	// type refs. structDepth tracks nested brackets inside the body.
	structNeedsName bool
	structDepth     int

	// forHeaderPending: after `for` keyword. The next `(` opens a "for
	// scope"; we push ScopeFor there so any decls go into it. The
	// scope closes at the matching `{` (C for-init scope extends over
	// the body).
	forHeaderPending bool
	inForHeader      bool
	forHeaderDepth   int
	// forBodyPending: once we've seen the `)` closing a for-header and
	// a ScopeFor is on the stack, the next `{` opens the for body as a
	// child block — which we handle by just pushing ScopeBlock inside
	// the ScopeFor. On the matching close of that body we close both.
	forBodyPending bool

	// sawTypeKeyword is set when a primitive type keyword (int, char,
	// void, ...) or a qualifier (const, static, ...) was observed at
	// the current statement. Combined with one trailing ident + `(`,
	// this signals a function declaration (vs a call expression). Or
	// with one trailing ident + `;`/`=`, a variable declaration (vs
	// a bare expression statement). Cleared on statement boundary.
	sawTypeKeyword bool

	// sawStatic is set when the `static` storage-class specifier
	// appears in the current statement. In C a file-scope decl has
	// external linkage unless marked static; this flag lets emitDecl
	// / emitDeclAtFileScope stamp Decl.Exported correctly.
	sawStatic bool

	// controlBlockExpected is true after seeing if/while/do/switch/else.
	// Tells handleOpenBrace to push a plain block scope (not a
	// composite literal or a function body).
	controlBlockExpected bool

	// Brace-level tracking for enum bodies (they don't push a scope —
	// enum constants leak to file scope — but we need to know we're
	// inside one so ident handling emits KindConst at file scope).
	enumBodyDepth int

	// prevByte is the last non-whitespace, non-comment byte we've
	// classified. Used for context-sensitive decisions like "is this
	// `{` a function body, control block, or composite literal?".
	prevByte byte
}

type pendingFuncDef struct {
	name     string
	nameSpan scope.Span
	// parenStart is the byte of the '(' that opens the param list;
	// used to set FullSpan for a decl (no body) correctly.
	parenStart uint32
	valid      bool
}

func (b *builder) run() {
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\\' && b.s.PeekAt(1) == '\n':
			// Line continuation: treat "\\n" as whitespace (no stmt boundary).
			b.s.Advance(2)
		case c == '\n':
			b.s.Next()
			// A bare newline in C is whitespace, not a statement boundary.
			// Statement boundaries are `;` and `{`/`}`.
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"':
			b.s.ScanSimpleString('"')
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.prevByte = '\''
		case c == '#':
			b.handlePreprocessor()
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == ';':
			b.handleSemicolon()
		case c == '(':
			b.handleOpenParen()
		case c == ')':
			b.handleCloseParen()
		case c == '[':
			b.s.Pos++
			b.prevByte = '['
			if b.isInsideStructBody() {
				b.structDepth++
			}
		case c == ']':
			b.s.Pos++
			b.prevByte = ']'
			if b.isInsideStructBody() && b.structDepth > 0 {
				b.structDepth--
			}
		case c == ',':
			b.handleComma()
		case c == '=':
			b.s.Pos++
			b.prevByte = '='
			b.handleDeclIfPending()
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == '-' && b.s.PeekAt(1) == '>':
			b.s.Advance(2)
			b.prevByte = '>'
		case lexkit.DefaultIdentStart[c]:
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() && (lexkit.IsASCIIDigit(b.s.Peek()) || b.s.Peek() == '.' || b.s.Peek() == '_' ||
				b.s.Peek() == 'x' || b.s.Peek() == 'X' || b.s.Peek() == 'e' || b.s.Peek() == 'E' ||
				b.s.Peek() == 'L' || b.s.Peek() == 'l' || b.s.Peek() == 'U' || b.s.Peek() == 'u' ||
				b.s.Peek() == 'f' || b.s.Peek() == 'F') {
				b.s.Pos++
			}
			b.prevByte = '0'
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

// handlePreprocessor consumes a `#...` directive up to the end of the
// logical line (respecting line continuations). Emits decls for
// `#define` and `#include`.
func (b *builder) handlePreprocessor() {
	start := uint32(b.s.Pos)
	b.s.Pos++ // consume '#'
	// Skip spaces/tabs after #.
	b.s.SkipSpaces()
	// Read directive keyword.
	wordStart := b.s.Pos
	for !b.s.EOF() {
		c := b.s.Peek()
		if lexkit.DefaultIdentCont[c] {
			b.s.Pos++
		} else {
			break
		}
	}
	directive := string(b.s.Src[wordStart:b.s.Pos])
	_ = start

	switch directive {
	case "define":
		b.s.SkipSpaces()
		// Name is the next identifier.
		nStart := b.s.Pos
		for !b.s.EOF() {
			c := b.s.Peek()
			if (nStart == b.s.Pos && lexkit.DefaultIdentStart[c]) || (nStart != b.s.Pos && lexkit.DefaultIdentCont[c]) {
				b.s.Pos++
			} else {
				break
			}
		}
		if b.s.Pos > nStart {
			name := string(b.s.Src[nStart:b.s.Pos])
			span := mkSpan(uint32(nStart), uint32(b.s.Pos))
			b.pendingFullStart = start + 1
			// Emit at file scope regardless of current scope (macros
			// are file-global) — but for simplicity emit at current
			// scope; nearly all #defines are at file scope anyway.
			b.emitDeclAtFileScope(name, scope.KindConst, span)
		}
	case "include":
		b.s.SkipSpaces()
		c := b.s.Peek()
		if c == '"' || c == '<' {
			closeCh := byte('"')
			quoteStyle := `"`
			if c == '<' {
				closeCh = '>'
				quoteStyle = "<>"
			}
			b.s.Pos++ // consume opener
			pathStart := b.s.Pos
			for !b.s.EOF() {
				ch := b.s.Peek()
				if ch == closeCh || ch == '\n' {
					break
				}
				b.s.Pos++
			}
			if b.s.Pos > pathStart {
				name := string(b.s.Src[pathStart:b.s.Pos])
				span := mkSpan(uint32(pathStart), uint32(b.s.Pos))
				b.pendingFullStart = start + 1
				idx := b.emitDeclAtFileScope(name, scope.KindImport, span)
				// Stamp Signature = "<includedPath>\x00<quoteStyle>"
				// so the import-graph resolver can distinguish quoted
				// (local) from angle-bracket (system) includes. C's
				// convention differs from other languages (which use
				// "<modulePath>\x00<origName>") because `#include`
				// has no per-name "original name".
				if idx >= 0 && idx < len(b.res.Decls) {
					b.res.Decls[idx].Signature = name + "\x00" + quoteStyle
				}
			}
			// Skip the closing quote/angle bracket.
			if !b.s.EOF() && b.s.Peek() == closeCh {
				b.s.Pos++
			}
		}
	}

	// Consume to end of logical line (handling line continuations).
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '\\' && b.s.PeekAt(1) == '\n' {
			b.s.Advance(2)
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
		if c == '"' {
			b.s.ScanSimpleString('"')
			continue
		}
		if c == '\'' {
			b.s.ScanSimpleString('\'')
			continue
		}
		if c == '\n' {
			// End of directive; do NOT consume the newline (let run()
			// handle it so line counting stays uniform).
			break
		}
		b.s.Pos++
	}
	// After a preprocessor line, we're at statement start.
	b.stmtStart = true
	b.resetStmt()
	b.prevByte = 'd'
}

// resetStmt clears per-statement accumulators.
func (b *builder) resetStmt() {
	b.stmtIdents = nil
	b.typeKeywordPending = ""
	b.typeKeywordIsEnum = false
	b.typeKeywordSeen = false
	b.inTypedef = false
	b.paramListPending = false
	b.funcAwaitingBody = false
	b.funcDef = pendingFuncDef{}
	b.pendingFullStart = 0
	b.sawTypeKeyword = false
	b.sawStatic = false
}

func (b *builder) handleSemicolon() {
	b.s.Pos++
	b.prevByte = ';'

	// Inside a struct/union body at top depth: we've accumulated ident
	// candidates on a "field line" — last ident is the field name.
	if b.isInsideStructBody() && b.structDepth == 0 {
		b.flushStructField()
	} else if b.inParamList {
		// Shouldn't normally happen, but defensive: stray `;`.
	} else {
		// Top-level of a block/function: flush any pending var decls.
		b.flushStmtAsVarDecls()
	}

	// Handle function declaration (no body) — if we were awaiting `{`
	// and got `;` instead, emit the function as a decl-only.
	if b.funcAwaitingBody && b.funcDef.valid {
		// Extend FullSpan end to include the `;`.
		b.emitFunctionDecl(b.funcDef, uint32(b.s.Pos))
	}

	// Enum values live in file scope; a `;` at the end of an enum
	// type decl closes the enum statement.

	b.stmtStart = true
	b.resetStmt()
	// Don't clear structNeedsName — the next line is a fresh field.
	b.structNeedsName = true
}

func (b *builder) handleComma() {
	b.s.Pos++
	b.prevByte = ','

	// In an enum body: all idents are enum constants. Flush the last
	// ident as a KindConst at file scope.
	if b.enumBodyDepth > 0 {
		b.flushEnumConstant()
		return
	}

	// In param list top depth: flush the current section as a param.
	if b.inParamList && b.paramDepth == 1 {
		b.flushParamSection()
		return
	}

	// In a struct body at top depth with multi-name field: `int x, y;`
	if b.isInsideStructBody() && b.structDepth == 0 {
		b.flushStructField()
		b.structNeedsName = true
		return
	}

	// Outside: in a typedef or a plain var decl statement, comma
	// separates names. Flush the last ident as a decl (var or type
	// alias in typedef).
	if b.stmtStart || len(b.stmtIdents) > 0 {
		b.flushStmtAsVarDecls()
	}
}

func (b *builder) handleOpenParen() {
	b.s.Pos++
	b.prevByte = '('

	// `for (` — open a for scope, then parse the header as a single
	// statement whose decls belong to the for scope.
	if b.forHeaderPending {
		b.forHeaderPending = false
		b.openScope(scope.ScopeFor, uint32(b.s.Pos-1))
		b.inForHeader = true
		b.forHeaderDepth = 1
		b.stmtStart = true
		b.resetStmt()
		return
	}
	if b.inForHeader {
		b.forHeaderDepth++
		return
	}

	if b.paramListPending {
		b.paramListPending = false
		b.inParamList = true
		b.paramDepth = 1
		b.paramSectionIdents = nil
		return
	}

	if b.inParamList {
		b.paramDepth++
		return
	}

	// Otherwise this is either a call expression (ident immediately
	// before) or a grouping. If the PRECEDING tokens include at least
	// one ident (the "function name") and we're at statement start,
	// this may be a function declaration: `int foo(...)`.
	//
	// Heuristic: if stmtIdents is non-empty and we are at the outermost
	// scope of a function or file (not inside expressions), treat the
	// last ident as a potential function name.
	if b.couldBeFunctionHeader() {
		last := b.stmtIdents[len(b.stmtIdents)-1]
		// Drop the last ident from stmtIdents; everything before it is
		// type/return-type tokens. Emit those as refs (they may be type
		// refs).
		for _, id := range b.stmtIdents[:len(b.stmtIdents)-1] {
			b.emitRef(id.name, id.span)
		}
		b.funcDef = pendingFuncDef{
			name:       last.name,
			nameSpan:   last.span,
			parenStart: uint32(b.s.Pos - 1),
			valid:      true,
		}
		b.stmtIdents = nil
		b.inParamList = true
		b.paramDepth = 1
		b.paramSectionIdents = nil
		return
	}
	// Otherwise: a call or grouped expression. If the last ident is a
	// callee, emit it as a ref.
	if len(b.stmtIdents) > 0 {
		last := b.stmtIdents[len(b.stmtIdents)-1]
		b.emitRef(last.name, last.span)
		b.stmtIdents = b.stmtIdents[:len(b.stmtIdents)-1]
	}
	// Remaining stmtIdents stay; we might be mid-expression.
}

func (b *builder) handleCloseParen() {
	b.s.Pos++
	b.prevByte = ')'

	if b.inForHeader {
		b.forHeaderDepth--
		if b.forHeaderDepth == 0 {
			b.inForHeader = false
			// Flush any remaining idents in the for-header as refs.
			for _, id := range b.stmtIdents {
				b.emitRef(id.name, id.span)
			}
			b.stmtIdents = nil
			// Next `{` is the for body — push as block, close both on `}`.
			b.forBodyPending = true
		}
		return
	}

	if b.inParamList {
		b.paramDepth--
		if b.paramDepth == 0 {
			b.inParamList = false
			// Flush the final param section.
			b.flushParamSection()
			// If we had a function name staged, we're now waiting to
			// see `{` (def) or `;` (decl).
			if b.funcDef.valid {
				b.funcAwaitingBody = true
			}
		}
		return
	}
}

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	b.prevByte = '{'

	// Function definition body.
	if b.funcAwaitingBody && b.funcDef.valid {
		b.funcAwaitingBody = false
		// Emit the function decl now so params live in the function
		// scope (child of file). Then open the function scope.
		idx := b.emitDecl(b.funcDef.name, scope.KindFunction, b.funcDef.nameSpan)
		b.pendingOwnerDecl = idx
		b.openScope(scope.ScopeFunction, uint32(b.s.Pos-1))
		// Emit pending params into the function scope.
		for _, p := range b.pendingParams {
			b.emitDecl(p.name, scope.KindParam, p.span)
		}
		b.pendingParams = nil
		b.funcDef = pendingFuncDef{}
		b.stmtStart = true
		b.resetStmt()
		return
	}

	// `struct Name {`, `union Name {`, or anonymous `struct {`.
	if b.typeKeywordSeen {
		sk := b.typeKeywordPending
		isEnum := b.typeKeywordIsEnum
		b.typeKeywordPending = ""
		b.typeKeywordIsEnum = false
		b.typeKeywordSeen = false

		// If we have a pending type NAME in stmtIdents, it's the last
		// ident we saw. Emit as KindType and associate the scope.
		// Idents before it are... unusual; emit as refs.
		var ownerIdx int = -1
		if len(b.stmtIdents) > 0 {
			typeName := b.stmtIdents[len(b.stmtIdents)-1]
			for _, id := range b.stmtIdents[:len(b.stmtIdents)-1] {
				b.emitRef(id.name, id.span)
			}
			ownerIdx = b.emitDecl(typeName.name, scope.KindType, typeName.span)
			b.stmtIdents = nil
		}
		if ownerIdx >= 0 {
			b.pendingOwnerDecl = ownerIdx
		}

		if isEnum {
			// Enum body: no new scope (enum constants leak to file scope).
			// Track depth to recognize the closing brace.
			b.enumBodyDepth = 1
			b.stmtStart = true
			return
		}
		// Struct/union body.
		b.openScope(sk, uint32(b.s.Pos-1))
		b.structNeedsName = true
		b.structDepth = 0
		b.stmtStart = true
		return
	}

	// For body: we're in a ScopeFor, and the `{` opens the actual body.
	if b.forBodyPending {
		b.forBodyPending = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		b.stmtStart = true
		b.resetStmt()
		return
	}

	// Control-flow block (`if (...) { }`, `while (...) { }`, etc.), or
	// plain block at statement position.
	if b.controlBlockExpected {
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		b.stmtStart = true
		b.resetStmt()
		return
	}

	// Plain `{` at statement position: push a block scope.
	// (`{` after a value context, like a composite literal, we simply
	// treat as a block too. The impact is: field-designator idents like
	// `.x` are already skipped by `.` handling. Values in initializers
	// become refs, which is acceptable for v1.)
	b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
	b.stmtStart = true
	b.resetStmt()
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.prevByte = '}'

	// Closing an enum body.
	if b.enumBodyDepth > 0 {
		b.enumBodyDepth--
		if b.enumBodyDepth == 0 {
			// Flush last pending enum constant (if any, and no trailing comma).
			b.flushEnumConstant()
			// After the enum body, if there are idents after `}` (e.g.
			// `enum Color { R } C;`), they'd be variable decls. But
			// we've already closed the "type decl" context; those are
			// handled by the regular flow via the next `;`.
			// Patch FullSpan for the enum type decl (if any pendingOwner).
			if b.pendingOwnerDecl >= 0 && b.pendingOwnerDecl < len(b.res.Decls) {
				if b.res.Decls[b.pendingOwnerDecl].FullSpan.EndByte < uint32(b.s.Pos) {
					b.res.Decls[b.pendingOwnerDecl].FullSpan.EndByte = uint32(b.s.Pos)
				}
				b.pendingOwnerDecl = -1
			}
		}
		b.stmtStart = true
		return
	}

	// Struct body: emit any pending last-field before closing.
	if b.isInsideStructBody() && b.structDepth == 0 {
		b.flushStructField()
	}
	b.closeTopScope(uint32(b.s.Pos))

	// If the just-closed scope was a ScopeBlock inside a ScopeFor, also
	// close the for scope.
	if top := b.stack.Top(); top != nil && top.Data.kind == scope.ScopeFor {
		b.closeTopScope(uint32(b.s.Pos))
	}

	// After a struct/union type body, a trailing identifier (typedef
	// name or variable) may appear. The surrounding logic handles it
	// via stmtIdents and the next `;`.
	b.stmtStart = true
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

	// Property access after `.` or `->`: emit as probable ref.
	if b.prevByte == '.' || b.prevByte == '>' {
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// Keywords that affect structure.
	switch name {
	case "typedef":
		b.inTypedef = true
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "struct":
		b.typeKeywordSeen = true
		b.typeKeywordPending = scope.ScopeClass
		b.typeKeywordIsEnum = false
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "union":
		b.typeKeywordSeen = true
		b.typeKeywordPending = scope.ScopeClass // reuse class scope kind for union
		b.typeKeywordIsEnum = false
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "enum":
		b.typeKeywordSeen = true
		b.typeKeywordPending = scope.ScopeBlock // not actually used as a scope
		b.typeKeywordIsEnum = true
		if b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		return
	case "for":
		b.forHeaderPending = true
		b.prevByte = 'k'
		return
	case "if", "while", "switch", "do", "else":
		b.controlBlockExpected = true
		b.prevByte = 'k'
		return
	case "return", "break", "continue", "goto", "case", "default", "sizeof":
		b.prevByte = 'k'
		return
	case "void", "char", "short", "int", "long", "float", "double", "signed", "unsigned",
		"const", "volatile", "static", "extern", "register", "auto", "inline",
		"restrict", "_Atomic", "_Thread_local", "_Bool", "_Complex", "_Imaginary", "_Noreturn":
		// Type qualifiers / specifiers. Don't emit as refs (they're
		// language built-ins), but record that we saw one so the next
		// ident can be recognized as a declaration name even when no
		// user-defined type token precedes it.
		b.sawTypeKeyword = true
		if name == "static" {
			// Track `static` separately: at file scope it marks a decl
			// as internal linkage (not visible to includers).
			b.sawStatic = true
		}
		if wasStmtStart && b.pendingFullStart == 0 {
			b.pendingFullStart = startByte + 1
		}
		b.prevByte = 'k'
		if wasStmtStart {
			b.stmtStart = true // stay at statement start for follow-up ident
		}
		return
	}

	span := mkSpan(startByte, endByte)
	id := lastIdent{name: name, span: span, atStmtStart: wasStmtStart}
	if wasStmtStart && b.pendingFullStart == 0 {
		b.pendingFullStart = startByte + 1
	}

	// In enum body: each ident at top is an enum constant. Keep only
	// the most recent one pending — it'll be flushed on `,`, `}`, or
	// `=` (which starts a value expression for that constant).
	if b.enumBodyDepth > 0 {
		// Flush any previously-staged constant (if `,` was already seen
		// we'd have cleared it). Stage this one.
		// Simplest: always emit on ident; re-emission is avoided by
		// the fact that value expressions don't produce new stmt-start
		// idents at enum top.
		// For enum, we stage the LAST ident seen at top; on `,`/`}` the
		// staged one is emitted. Use stmtIdents for storage.
		b.stmtIdents = append(b.stmtIdents, id)
		b.prevByte = 'i'
		return
	}

	// In a param section: collect for later flush.
	if b.inParamList && b.paramDepth == 1 {
		b.paramSectionIdents = append(b.paramSectionIdents, id)
		b.prevByte = 'i'
		return
	}

	// Struct body top depth: collect for "last ident of the line is the
	// field name". Delegate to stmtIdents and let `;`/`,` flush.
	if b.isInsideStructBody() && b.structDepth == 0 {
		b.stmtIdents = append(b.stmtIdents, id)
		b.prevByte = 'i'
		return
	}

	// Default: accumulate into stmtIdents. `(`, `;`, `=`, `,`, `[` will
	// decide what the trailing ident is.
	b.stmtIdents = append(b.stmtIdents, id)
	b.prevByte = 'i'
}

// handleDeclIfPending is called on `=`: if we have stmt idents, the
// last one is a variable being initialized. Flush it as a var decl; the
// earlier idents are type tokens/refs.
func (b *builder) handleDeclIfPending() {
	if len(b.stmtIdents) == 0 {
		return
	}
	if b.inParamList {
		// Default argument in a param: the last ident in the current
		// section is the param; anything after `=` is a value expr.
		return
	}
	if b.enumBodyDepth > 0 {
		// `enum Color { RED = 0 }` — RED is the constant; `=` begins
		// its value. Emit RED (the last ident) as KindConst at file.
		b.flushEnumConstant()
		return
	}
	// `int x = 5;` — emit x as var (or const/type depending on qualifier).
	b.flushStmtAsVarDecls()
}

// couldBeFunctionHeader reports whether the current stmtIdents
// accumulation, followed by `(`, plausibly represents a function header.
// A function header requires at least one ident (the name) and that we
// be at statement position (not mid-expression inside parens/brackets).
func (b *builder) couldBeFunctionHeader() bool {
	if len(b.stmtIdents) == 0 {
		return false
	}
	if b.inParamList || b.inForHeader {
		return false
	}
	// Need to be at file scope or block scope where decls are allowed.
	sk := b.currentScopeKind()
	if sk != scope.ScopeFile && sk != scope.ScopeBlock && sk != scope.ScopeFunction {
		return false
	}
	// Distinguish declaration from call:
	//   - `int foo(`   → 1 ident + sawTypeKeyword → declaration
	//   - `Foo bar(`   → 2 idents (both user types/names) → declaration
	//   - `foo(`       → 1 ident, no type keyword → call expression
	// The first ident must have appeared at statement start (contiguous
	// top-level idents that all started as the statement started).
	if !b.stmtIdents[0].atStmtStart {
		return false
	}
	if len(b.stmtIdents) >= 2 {
		return true
	}
	// len == 1: only a declaration if a type keyword preceded it.
	return b.sawTypeKeyword
}

// flushStmtAsVarDecls: on `;`, `=`, or `,` at statement level, the last
// ident in stmtIdents is a variable being declared. Earlier idents are
// type tokens (built-in keywords were skipped) or type refs.
func (b *builder) flushStmtAsVarDecls() {
	if len(b.stmtIdents) == 0 {
		return
	}
	// typedef: the last ident is a type alias.
	if b.inTypedef {
		last := b.stmtIdents[len(b.stmtIdents)-1]
		for _, id := range b.stmtIdents[:len(b.stmtIdents)-1] {
			b.emitRef(id.name, id.span)
		}
		b.emitDecl(last.name, scope.KindType, last.span)
		b.stmtIdents = nil
		return
	}
	// Regular var decl: last ident = var, others = type refs.
	// Skip if we have only one ident and no preceding type keyword
	// (e.g., bare expression `foo;` — treat foo as a ref).
	if len(b.stmtIdents) == 1 {
		id := b.stmtIdents[0]
		if b.sawTypeKeyword && id.atStmtStart {
			b.emitDecl(id.name, scope.KindVar, id.span)
		} else {
			b.emitRef(id.name, id.span)
		}
		b.stmtIdents = nil
		return
	}
	last := b.stmtIdents[len(b.stmtIdents)-1]
	for _, id := range b.stmtIdents[:len(b.stmtIdents)-1] {
		b.emitRef(id.name, id.span)
	}
	b.emitDecl(last.name, scope.KindVar, last.span)
	b.stmtIdents = nil
}

// flushParamSection: called on `,` or at end of param list. The last
// ident in paramSectionIdents is the param name; earlier ones are type
// refs. Primitive type keywords (int, void, ...) are tracked via
// sawTypeKeyword, which is set at the param-list level (not per
// section); we rely on the "2+ idents" heuristic for user types.
func (b *builder) flushParamSection() {
	ids := b.paramSectionIdents
	b.paramSectionIdents = nil
	if len(ids) == 0 {
		return
	}
	if len(ids) == 1 {
		// Single ident: ambiguous between
		//   (a) unnamed parameter of user type:    `int foo(MyType);`
		//   (b) named parameter of primitive type: `int foo(int x);` where
		//       "int" was consumed as a keyword and x is the single ident.
		// Distinguish via sawTypeKeyword (set by keyword handler earlier
		// in the param list). If a primitive keyword was seen, treat the
		// ident as the param name; otherwise, as a type ref.
		if b.sawTypeKeyword {
			b.pendingParams = append(b.pendingParams, pendingParam{
				name: ids[0].name,
				span: ids[0].span,
				kind: scope.KindParam,
			})
		} else {
			b.emitRef(ids[0].name, ids[0].span)
		}
		// Reset sawTypeKeyword per section so multi-param `(int a, MyT)`
		// treats the second section independently.
		b.sawTypeKeyword = false
		return
	}
	last := ids[len(ids)-1]
	for _, id := range ids[:len(ids)-1] {
		b.emitRef(id.name, id.span)
	}
	b.pendingParams = append(b.pendingParams, pendingParam{
		name: last.name,
		span: last.span,
		kind: scope.KindParam,
	})
	b.sawTypeKeyword = false
}

// flushStructField: last ident in stmtIdents is the field name;
// earlier idents are type refs. For primitive-typed fields like
// `int x;` the single ident is the field (identified by sawTypeKeyword
// having been set by the "int" handler).
func (b *builder) flushStructField() {
	if len(b.stmtIdents) == 0 {
		return
	}
	last := b.stmtIdents[len(b.stmtIdents)-1]
	for _, id := range b.stmtIdents[:len(b.stmtIdents)-1] {
		b.emitRef(id.name, id.span)
	}
	b.emitDecl(last.name, scope.KindField, last.span)
	b.stmtIdents = nil
}

// flushEnumConstant: in an enum body, the most recently staged ident
// is an enum constant. Emit it as KindConst at file scope.
func (b *builder) flushEnumConstant() {
	if len(b.stmtIdents) == 0 {
		return
	}
	// Take last.
	last := b.stmtIdents[len(b.stmtIdents)-1]
	// Anything before it (rare — value expression refs) — emit as refs.
	for _, id := range b.stmtIdents[:len(b.stmtIdents)-1] {
		b.emitRef(id.name, id.span)
	}
	b.emitDeclAtFileScope(last.name, scope.KindConst, last.span)
	b.stmtIdents = nil
}

// emitFunctionDecl emits a function declaration (no body) — called
// when we see `;` after a param list close. endByte is one past the `;`.
func (b *builder) emitFunctionDecl(fd pendingFuncDef, endByte uint32) {
	idx := b.emitDecl(fd.name, scope.KindFunction, fd.nameSpan)
	// Patch FullSpan end to include the `;`.
	if idx >= 0 && idx < len(b.res.Decls) {
		if b.res.Decls[idx].FullSpan.EndByte < endByte {
			b.res.Decls[idx].FullSpan.EndByte = endByte
		}
	}
	// Emit the pending params as refs at the current scope (they aren't
	// scope-bound to anything, since there's no function body).
	for _, p := range b.pendingParams {
		b.emitRef(p.name, p.span)
	}
	b.pendingParams = nil
	b.funcDef = pendingFuncDef{}
	b.funcAwaitingBody = false
}

// isInsideStructBody reports whether we're inside a struct/union body
// (ScopeClass with structDepth context).
func (b *builder) isInsideStructBody() bool {
	sk := b.currentScopeKind()
	return sk == scope.ScopeClass
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
			savedStructNeedsName: b.structNeedsName,
			savedStructDepth:     b.structDepth,
			savedInTypedef:       b.inTypedef,
			ownerDeclIdx:         owner,
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
	// A struct body inside a typedef inherits inTypedef=false (the
	// typedef keyword only applies once the struct body ends). But
	// after the scope closes, we restore the enclosing inTypedef so
	// the trailing ident (the type alias) flushes correctly.
	b.inTypedef = false
	if kind == scope.ScopeClass {
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
	b.inTypedef = e.Data.savedInTypedef
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

// emitDecl records a declaration at the current scope and returns the
// index in res.Decls.
func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span) int {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	ns := scope.NSValue
	if kind == scope.KindField {
		ns = scope.NSField
	} else if kind == scope.KindType {
		ns = scope.NSType
	}
	// Exported file-scope decls share a DeclID across .c and .h
	// siblings when both map to the same canonical path. Static
	// decls and nested-scope decls keep the file-based hash.
	hashPath := b.file
	exportedAtFile := kind != scope.KindImport && scopeID == scope.ScopeID(1) && !b.sawStatic
	if exportedAtFile && b.canonicalPath != "" {
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

	// Exported: file-scope decls default to external linkage in C;
	// `static` flips them to internal (not visible to includers).
	// KindImport (#include) is not a declaration in the export sense,
	// so never mark imports Exported — the resolver filters them.
	exported := false
	if kind != scope.KindImport && scopeID == scope.ScopeID(1) && !b.sawStatic {
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
	b.pendingFullStart = 0
	return idx
}

// emitDeclAtFileScope emits a decl targeted at the file scope (ScopeID 1).
// Used for enum constants (which leak out of the enum body in C) and
// for #define macros (which are file-global).
func (b *builder) emitDeclAtFileScope(name string, kind scope.DeclKind, span scope.Span) int {
	// File scope is always ScopeID 1 (opened first in Parse).
	fileScope := scope.ScopeID(1)
	locID := hashLoc(b.file, span, name)
	ns := scope.NSValue
	if kind == scope.KindType {
		ns = scope.NSType
	} else if kind == scope.KindImport {
		ns = scope.NSValue
	}
	// File-scope emits: macros + enum constants. These are exported
	// unless declared static — use the canonical path when available
	// so they join across .c/.h siblings.
	hashPath := b.file
	exportedAtFile := kind != scope.KindImport && !b.sawStatic
	if exportedAtFile && b.canonicalPath != "" {
		hashPath = b.canonicalPath
	}
	declID := hashDecl(hashPath, name, ns, fileScope)
	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}
	// Exported: file-scope decls (macros, enum constants, file-scope
	// vars/types) have external linkage unless declared `static`.
	// KindImport (#include) is never marked Exported — it's not a
	// declaration in the export sense; the resolver filters it.
	exported := false
	if kind != scope.KindImport && !b.sawStatic {
		exported = true
	}
	idx := len(b.res.Decls)
	b.res.Decls = append(b.res.Decls, scope.Decl{
		ID:        declID,
		LocID:     locID,
		Name:      name,
		Namespace: ns,
		Kind:      kind,
		Scope:     fileScope,
		File:      b.file,
		Span:      span,
		FullSpan:  fullSpan,
		Exported:  exported,
	})
	b.pendingFullStart = 0
	return idx
}

// emitPropertyRef records a property-access ref (after `.` or `->`).
// Binding is BindProbable, Reason="property_access".
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
	// Index ALL decls per key (not just the first): a C function body
	// may legally redeclare a name that shadows a file-scope decl
	// from its declaration point onward. Lookup picks the decl with
	// the latest Span.EndByte that is still <= ref.Span.StartByte,
	// so refs before the local decl bind to the outer decl and refs
	// after bind to the local.
	byKey := make(map[key][]*scope.Decl, len(b.res.Decls))
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		k := key{scope: d.Scope, name: d.Name, ns: d.Namespace}
		byKey[k] = append(byKey[k], d)
	}
	// Track scope kind so lookupLexical can apply the right rule.
	scopeKind := make(map[scope.ScopeID]scope.ScopeKind, len(b.res.Scopes))
	for _, s := range b.res.Scopes {
		scopeKind[s.ID] = s.Kind
	}
	// lookupLexical returns the decl with the latest Span.EndByte
	// that precedes the ref's StartByte.
	//
	// At block/function scope we require strict lexical ordering:
	// `int x = compute(5); int compute = 42;` must NOT bind the
	// call to the later local — the call must escalate to the
	// enclosing scope instead.
	//
	// At file scope we allow forward references (common C pattern:
	// `int main() { foo(); } int foo() {}`), so if no decl precedes
	// we return the first one anyway.
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
	for i := range b.res.Refs {
		r := &b.res.Refs[i]
		if r.Binding.Reason == "property_access" {
			continue
		}
		cur := r.Scope
		resolved := false
		for {
			curIsFile := scopeKind[cur] == scope.ScopeFile
			if d := lookupLexical(key{scope: cur, name: r.Name, ns: r.Namespace}, r.Span.StartByte, curIsFile); d != nil {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   d.ID,
					Reason: "direct_scope",
				}
				resolved = true
				break
			}
			if r.Namespace == scope.NSValue {
				if d := lookupLexical(key{scope: cur, name: r.Name, ns: scope.NSType}, r.Span.StartByte, curIsFile); d != nil {
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
			if builtins.C.Has(r.Name) {
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
	h.Write([]byte("<builtin:c>"))
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
