// Package zig is the Zig scope + binding extractor.
//
// Zig is brace-delimited with explicit decl keywords: `fn`, `const`,
// `var`. Visibility is `pub`. The file itself is a struct namespace.
// Container types are introduced via `const Name = struct/enum/union/
// error { ... }` — the body is both a type definition and a scope
// (we model it as ScopeClass so methods inside `struct { fn ... }`
// land in a class-like scope rather than a generic block).
//
// What this builder handles:
//   - `pub fn name(...)` / `fn name(...)`            → KindFunction
//   - Function inside `struct { fn name() }`         → KindMethod
//   - `pub const Name = struct/enum/union/error{}`   → KindClass / KindEnum / KindType
//   - `pub const x = expr`                           → KindConst
//   - `var x = expr`                                 → KindVar
//   - Function params (`name: T`)                    → KindParam
//   - Block-scope captures `if (cond) |x| { ... }`,
//     `while (cond) |x| { }`, `for (xs) |x, i| { }`  → KindLet
//   - Refs: bare idents, property access `.field`
//
// Skipped (first-pass):
//   - `comptime` / `inline` block tracking — opens a normal block.
//   - `usingnamespace X;` doesn't import names from X.
//   - `error { A, B }` enum-like field decls aren't tracked individually.
//   - Generic type params in `fn name(comptime T: type, ...)` flow
//     through as regular params.
package zig

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:          file,
		canonicalPath: canonicalPath,
		res:           &scope.Result{File: file},
		s:             lexkit.New(src),
	}
	b.openScope(scope.ScopeFile, 0)
	b.run()
	for b.stack.Depth() > 0 {
		b.closeTopScope(uint32(len(src)))
	}
	b.resolveRefs()
	return b.res
}

type builder struct {
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner
	stack         lexkit.ScopeStack[scopeEntry]

	// Pending decl state. Set by the keyword scanner (`fn`, `const`,
	// `var`); the next ident in decl position consumes it as the name.
	declPending     declKindPending
	declPub         bool // `pub` modifier seen for the current decl
	declStartByte   uint32
	declSeen        bool // we've seen the name already, awaiting `(` or `=`/end

	// Function-decl name + bookkeeping. Once we emit the decl we
	// capture its index so the body's open-brace patches FullSpan.
	funcName        string
	funcSpan        scope.Span
	funcDeclIdx     int

	// `pub const Name = struct/enum/union/error { ... }` — we mark the
	// upcoming `{` as a class-like scope rather than a plain block,
	// and the decl kind is upgraded to KindClass / KindEnum / KindType.
	containerKind   scope.DeclKind
	containerScope  scope.ScopeKind
	containerActive bool

	// Param-list parsing state. When inside a function header `(...)`,
	// we parse `name: type` pairs and emit KindParam decls into the
	// upcoming function scope.
	paramListDepth int
	inParamList    bool
	paramExpectName bool
	paramBuf        []pendingDecl

	// Pipe captures: `if (...) |x| { }`, `while (...) |x| { }`,
	// `for (xs) |x, i| { }`. Idents between the `|...|` get emitted as
	// KindLet decls in the upcoming block scope.
	pipeOpen        bool
	pipeBuf         []pendingDecl

	// Track `{` openers that should turn into Function or Class scopes.
	pendingFunc       bool
	pendingClassKind  scope.ScopeKind // if non-empty, opens with this Kind

	prevByte byte
}

type declKindPending int

const (
	declNone declKindPending = iota
	declFn
	declConst
	declVar
	declTest
)

type scopeEntry struct {
	kind scope.ScopeKind
	id   scope.ScopeID
}

type pendingDecl struct {
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
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '"':
			b.s.ScanSimpleString('"')
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.prevByte = '\''
		case c == '@':
			// Builtin call like @import("..."). Consume `@name(...)`
			// without emitting a ref — the name is reserved syntax.
			b.s.Pos++
			if !b.s.EOF() && lexkit.DefaultIdentStart[b.s.Peek()] {
				b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			}
			b.prevByte = ')'
		case c == '(':
			b.s.Pos++
			if b.pendingFunc && !b.inParamList {
				b.inParamList = true
				b.paramListDepth = 1
				b.paramExpectName = true
				b.paramBuf = b.paramBuf[:0]
			} else if b.inParamList {
				b.paramListDepth++
			}
			b.prevByte = '('
		case c == ')':
			b.s.Pos++
			if b.inParamList {
				b.paramListDepth--
				if b.paramListDepth == 0 {
					b.inParamList = false
					b.paramExpectName = false
				}
			}
			b.prevByte = ')'
		case c == '|':
			b.s.Pos++
			// `||` is logical-or; we only enter pipe-capture mode at
			// statement-positioned `|`. Heuristic: prevByte is `)` or
			// `=` (loop / assignment captures) — open if not already
			// in capture mode, else close.
			if b.s.Peek() == '|' {
				b.s.Pos++
				b.prevByte = '|'
				break
			}
			if b.pipeOpen {
				b.pipeOpen = false
			} else if b.prevByte == ')' || b.prevByte == '=' {
				b.pipeOpen = true
				b.pipeBuf = b.pipeBuf[:0]
			}
			b.prevByte = '|'
		case c == '{':
			b.s.Pos++
			b.handleOpenBrace()
			b.prevByte = '{'
		case c == '}':
			b.s.Pos++
			b.closeTopScope(uint32(b.s.Pos))
			b.prevByte = '}'
		case c == ',':
			b.s.Pos++
			if b.inParamList && b.paramListDepth == 1 {
				b.paramExpectName = true
			}
			b.prevByte = ','
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == ':':
			b.s.Pos++
			if b.inParamList {
				// `name: Type` — flip out of name-expect mode; the
				// type expression gets processed as refs naturally.
				b.paramExpectName = false
			}
			b.prevByte = ':'
		case c == ';':
			b.s.Pos++
			// End of statement — abandon any half-parsed decl that
			// didn't reach a body or initializer.
			b.declPending = declNone
			b.declPub = false
			b.declSeen = false
			b.containerActive = false
			b.containerKind = ""
			b.containerScope = ""
			b.pendingFunc = false
			b.pendingClassKind = ""
			b.prevByte = ';'
		case c == '=':
			b.s.Pos++
			// Skip `==`, `=>`.
			if !b.s.EOF() && (b.s.Peek() == '=' || b.s.Peek() == '>') {
				b.s.Pos++
			}
			b.prevByte = '='
		case lexkit.DefaultIdentStart[c]:
			startByte := uint32(b.s.Pos)
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			endByte := uint32(b.s.Pos)
			b.handleIdent(word, startByte, endByte)
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

func (b *builder) handleIdent(word []byte, startByte, endByte uint32) {
	name := string(word)
	span := scope.Span{StartByte: startByte, EndByte: endByte}

	// Property access: `obj.field`. Suppress when prev was a number-ish
	// `.0.1` field tuple syntax (rare; skip for now).
	if b.prevByte == '.' {
		b.emitPropertyRef(name, span)
		b.prevByte = 'i'
		return
	}

	// Pipe captures inside `if/while/for (...) |x[, i]| { ... }`.
	if b.pipeOpen {
		b.pipeBuf = append(b.pipeBuf, pendingDecl{name: name, span: span, kind: scope.KindLet})
		b.prevByte = 'i'
		return
	}

	// Param-list name slot.
	if b.inParamList && b.paramExpectName {
		// Skip `comptime`, `noalias`, `anytype` keyword positions.
		switch name {
		case "comptime", "noalias", "anytype", "anyerror":
			b.prevByte = 'k'
			return
		}
		b.paramBuf = append(b.paramBuf, pendingDecl{name: name, span: span, kind: scope.KindParam})
		b.paramExpectName = false
		b.prevByte = 'i'
		return
	}

	switch name {
	case "pub":
		b.declPub = true
		b.prevByte = 'k'
		return
	case "fn":
		b.declPending = declFn
		b.declStartByte = startByte
		b.declSeen = false
		b.prevByte = 'k'
		return
	case "const":
		b.declPending = declConst
		b.declStartByte = startByte
		b.declSeen = false
		b.prevByte = 'k'
		return
	case "var":
		b.declPending = declVar
		b.declStartByte = startByte
		b.declSeen = false
		b.prevByte = 'k'
		return
	case "test":
		b.declPending = declTest
		b.declStartByte = startByte
		b.declSeen = false
		b.prevByte = 'k'
		return
	case "struct":
		// `const Foo = struct { ... }` — RHS of a const decl.
		// containerActive is true here only if we're between `=` and `{`.
		if b.declPending == declConst && b.declSeen {
			b.containerActive = true
			b.containerKind = scope.KindClass
			b.containerScope = scope.ScopeClass
			b.pendingClassKind = scope.ScopeClass
		}
		b.prevByte = 'k'
		return
	case "enum":
		if b.declPending == declConst && b.declSeen {
			b.containerActive = true
			b.containerKind = scope.KindEnum
			b.containerScope = scope.ScopeClass
			b.pendingClassKind = scope.ScopeClass
		}
		b.prevByte = 'k'
		return
	case "union":
		if b.declPending == declConst && b.declSeen {
			b.containerActive = true
			b.containerKind = scope.KindType
			b.containerScope = scope.ScopeClass
			b.pendingClassKind = scope.ScopeClass
		}
		b.prevByte = 'k'
		return
	case "error":
		if b.declPending == declConst && b.declSeen {
			b.containerActive = true
			b.containerKind = scope.KindType
			b.containerScope = scope.ScopeClass
			b.pendingClassKind = scope.ScopeClass
		}
		b.prevByte = 'k'
		return
	case "if", "while", "for", "switch", "else", "defer", "errdefer",
		"return", "break", "continue", "and", "or", "orelse", "catch",
		"try", "comptime", "inline", "asm", "volatile", "noinline",
		"linksection", "callconv", "threadlocal", "extern", "export",
		"packed", "align", "allowzero", "noreturn", "true", "false",
		"null", "undefined", "unreachable":
		b.prevByte = 'k'
		return
	}

	// Decl name slot.
	if b.declPending != declNone && !b.declSeen {
		b.handleDeclName(name, span)
		b.prevByte = 'i'
		return
	}

	b.emitRef(name, span)
	b.prevByte = 'i'
}

// handleDeclName consumes the ident in decl-name position and emits the
// decl with the appropriate kind.
func (b *builder) handleDeclName(name string, span scope.Span) {
	switch b.declPending {
	case declFn:
		// Method when inside a struct/enum/union body.
		kind := scope.KindFunction
		if b.currentScopeKind() == scope.ScopeClass {
			kind = scope.KindMethod
		}
		idx := b.emitDeclLocal(name, span, kind)
		b.funcName = name
		b.funcSpan = span
		b.funcDeclIdx = idx
		b.declSeen = true
		b.pendingFunc = true
	case declConst, declVar:
		kind := scope.KindConst
		if b.declPending == declVar {
			kind = scope.KindVar
		}
		// We may upgrade kind once `=` + `struct/enum/union/error` is seen.
		// For now emit with the basic kind; rewrite to type-kind below.
		idx := b.emitDeclLocal(name, span, kind)
		b.declSeen = true
		// Stash idx so we can rewrite the kind on container detection.
		b.funcDeclIdx = idx
	case declTest:
		// `test "name" { ... }` or `test { ... }` — just open a block scope
		// at the body. Don't emit a decl; the test name (if any) is a
		// string literal.
		b.declSeen = true
		b.pendingFunc = true
	}
}

func (b *builder) handleOpenBrace() {
	switch {
	case b.pendingFunc:
		// Opening a function body or test block.
		b.openScope(scope.ScopeFunction, uint32(b.s.Pos-1))
		// Flush params + pipe captures into this scope.
		for _, p := range b.paramBuf {
			b.emitDeclLocal(p.name, p.span, p.kind)
		}
		b.paramBuf = b.paramBuf[:0]
		for _, p := range b.pipeBuf {
			b.emitDeclLocal(p.name, p.span, p.kind)
		}
		b.pipeBuf = b.pipeBuf[:0]
		b.pendingFunc = false
		b.declPending = declNone
		b.declPub = false
		b.declSeen = false
	case b.containerActive && b.pendingClassKind != "":
		// Opening a struct/enum/union body: ScopeClass.
		b.openScope(scope.ScopeClass, uint32(b.s.Pos-1))
		// Upgrade the most recent const decl to a type kind.
		if b.funcDeclIdx >= 0 && b.funcDeclIdx < len(b.res.Decls) {
			b.res.Decls[b.funcDeclIdx].Kind = b.containerKind
		}
		b.containerActive = false
		b.containerKind = ""
		b.containerScope = ""
		b.pendingClassKind = ""
		b.declPending = declNone
		b.declPub = false
		b.declSeen = false
	default:
		// Generic block — `if/while/for/while ... |...| {`, comptime { }, etc.
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		// Any pending pipe captures land here (loop / if captures).
		for _, p := range b.pipeBuf {
			b.emitDeclLocal(p.name, p.span, p.kind)
		}
		b.pipeBuf = b.pipeBuf[:0]
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
	b.stack.Push(lexkit.Scope[scopeEntry]{
		Data:     scopeEntry{kind: kind, id: id},
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
	return scope.ScopeFile
}

func (b *builder) emitDeclLocal(name string, span scope.Span, kind scope.DeclKind) int {
	scopeID := b.currentScope()
	canon := b.canonicalPath
	if canon == "" {
		canon = b.file
	}
	id := hashDecl(canon, name, scope.NSValue, scopeID)
	idx := len(b.res.Decls)
	b.res.Decls = append(b.res.Decls, scope.Decl{
		ID:        id,
		LocID:     hashLoc(b.file, span, name),
		Name:      name,
		Namespace: scope.NSValue,
		Kind:      kind,
		Scope:     scopeID,
		File:      b.file,
		Span:      span,
		FullSpan:  span,
		Exported:  b.declPub,
	})
	return idx
}

func (b *builder) emitRef(name string, span scope.Span) {
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     hashLoc(b.file, span, name),
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSValue,
		Scope:     b.currentScope(),
		Binding: scope.RefBinding{
			Kind:   scope.BindUnresolved,
			Reason: "pending",
		},
	})
}

func (b *builder) emitPropertyRef(name string, span scope.Span) {
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     hashLoc(b.file, span, name),
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSField,
		Scope:     b.currentScope(),
		Binding: scope.RefBinding{
			Kind:   scope.BindProbable,
			Reason: "property_access",
		},
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
	}
	byKey := make(map[key]*scope.Decl, len(b.res.Decls))
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		if d.Namespace != scope.NSValue {
			continue
		}
		k := key{scope: d.Scope, name: d.Name}
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
		for cur != 0 {
			if d, ok := byKey[key{scope: cur, name: r.Name}]; ok {
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
			cur = p
		}
		if !resolved {
			if builtins.Zig.Has(r.Name) {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   hashBuiltinDecl(r.Name),
					Reason: "builtin",
				}
			} else {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindUnresolved,
					Reason: "unresolved",
				}
			}
		}
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
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(scopeID))
	h.Write(buf[:])
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}

func hashBuiltinDecl(name string) scope.DeclID {
	h := sha256.New()
	h.Write([]byte("<builtin:zig>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
