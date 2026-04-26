// Package lua is the Lua scope + binding extractor.
//
// Lua's scope model is simpler than most: explicit `local` keyword for
// declarations, explicit block keywords for scope boundaries (function,
// do, then, repeat). No classes — methods are syntactic sugar over
// table-field assignment (`function obj:m()` ≈ `obj.m = function(self)`).
//
// What this builder handles:
//   - `local name = expr` / `local name`             → KindLet
//   - `local a, b, c = ...`                          → multiple KindLet
//   - `local function name(...)`                     → KindFunction (current scope)
//   - `function name(...)`                           → KindFunction (file scope)
//   - `function obj.f(...)` / `function obj:f(...)`  → KindMethod (file scope)
//   - Function parameters                            → KindParam (function scope)
//   - `for x[, y] in/= ... do BLOCK end`             → KindLet in block scope
//   - Bare-ident references                          → emitRef
//   - `obj.x` / `obj:x` / `obj["x"]`                 → emitPropertyRef
//
// Scope kinds opened:
//   - File scope: opened at start, never popped
//   - Function: function name(...) ... end / local function ... / function() ... end
//   - Block: do/end, then/end, while ... do/end, for ... do/end, repeat/until
//
// Known gaps (first-pass):
//   - Multi-segment names (`function module.sub.fn`) record only the
//     last segment as a KindFunction; the receiver chain isn't modeled.
//   - `obj["literal"]` indexing isn't tracked as a prop ref.
//   - Goto labels aren't tracked.
//   - `if x then y else z end` chain: each block is one scope; we open
//     on `then`/`else`, close on `elseif`/`else`/`end`.
package lua

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a Lua source buffer.
// File-scope decls hash with the file path.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path used when
// hashing file-scope DeclIDs. When canonicalPath is non-empty,
// file-scope decls hash with it so cross-file callers can match.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:          file,
		canonicalPath: canonicalPath,
		res:           &scope.Result{File: file},
		s:             lexkit.New(src),
	}
	b.openScope(scope.ScopeFile, 0, luaOpenerFile)
	b.run()
	for b.stack.Depth() > 0 {
		b.closeTopScope(uint32(len(src)))
	}
	b.resolveRefs()
	return b.res
}

type luaOpener int

const (
	luaOpenerFile luaOpener = iota
	luaOpenerFunction
	luaOpenerDo     // do ... end (also: while ... do, for ... do)
	luaOpenerThen   // if/elseif ... then ... [elseif|else|end]
	luaOpenerElse   // else ... end
	luaOpenerRepeat // repeat ... until expr
)

type scopeEntry struct {
	kind   scope.ScopeKind
	id     scope.ScopeID
	opener luaOpener
}

type builder struct {
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner
	stack         lexkit.ScopeStack[scopeEntry]

	// localPending: state for `local NAME[, NAME]* = ...`. After we see
	// `local`, idents up to `=` or end of statement become KindLet decls
	// in the current scope.
	localPending bool

	// forVars: state for `for NAME[, NAME]* (in|=) ... do`. Idents up to
	// `in` or `=` become KindLet decls; opened in the do-block scope.
	forVarsPending bool
	forVarsBuf     []pendingDecl

	// funcPending: state for `function ...` or `local function ...`.
	// Lua function names can be dotted/colon-chained: `function obj.sub:m`.
	// We accumulate the rightmost segment as the actual decl name and
	// note `:` to mark a method-style def with implicit self. Emission
	// is deferred until the param-list `(` so multi-segment names are
	// resolved before we register the decl.
	funcPending     bool
	funcLocal       bool        // true for `local function` (decl in current scope)
	funcMethodStyle bool        // true if `:` appeared in the chain
	funcStartByte   uint32      // byte offset of the `function` keyword
	funcLastName    string      // most recent ident scanned (the eventual decl name)
	funcLastSpan    scope.Span  // span of funcLastName

	// paramPending: between `(` and `)` of a function header, scan idents
	// as KindParam and emit into the upcoming function scope.
	paramPending bool

	// pendingFuncScope: when set, the next `(` starts a function header
	// param list; on `)` we open a Function scope and flush params.
	pendingFuncScope    bool
	pendingFuncDeclIdx  int // index into b.res.Decls of the func decl, -1 if anon
	pendingFuncSelf     bool
	pendingFuncOpenLine int

	// paramBuf accumulates pending KindParam decls until function body opens.
	paramBuf []pendingDecl

	// prevByte: last non-whitespace byte (or 'i' if last token was an
	// ident). Used to detect property-access position (prev == '.' or ':').
	prevByte byte
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
			// Newline ends a `local` statement if no `=` was seen — flush
			// pending names as decls.
			b.flushLocalIfPending()
		case c == ';':
			b.s.Pos++
			b.flushLocalIfPending()
			b.prevByte = ';'
		case c == '-' && b.s.PeekAt(1) == '-':
			b.skipComment()
		case c == '"':
			b.s.ScanSimpleString('"')
			b.prevByte = '"'
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.prevByte = '\''
		case c == '[' && (b.s.PeekAt(1) == '[' || b.s.PeekAt(1) == '='):
			save := b.s.Pos
			if !b.scanLongString() {
				b.s.Pos = save
				b.s.Pos++
				b.prevByte = '['
			} else {
				b.prevByte = ']'
			}
		case c == '(':
			b.s.Pos++
			if b.funcPending {
				// End of name chain — emit the decl now (or skip for
				// anonymous functions where no name was scanned).
				if b.funcLastName != "" {
					b.emitFunctionDecl(b.funcLastName, b.funcLastSpan)
				} else {
					b.funcPending = false
					b.funcLocal = false
				}
				b.funcLastName = ""
			}
			if b.pendingFuncScope {
				b.paramPending = true
			}
			b.prevByte = '('
		case c == ')':
			b.s.Pos++
			if b.paramPending {
				b.paramPending = false
				// Open function body scope and flush params into it.
				b.openScope(scope.ScopeFunction, b.funcStartByte, luaOpenerFunction)
				if b.pendingFuncSelf {
					// `obj:m` adds an implicit `self` parameter.
					b.emitDeclLocal("self", scope.Span{StartByte: b.funcStartByte, EndByte: b.funcStartByte}, scope.KindParam)
				}
				for _, p := range b.paramBuf {
					b.emitDeclLocal(p.name, p.span, p.kind)
				}
				b.paramBuf = b.paramBuf[:0]
				b.pendingFuncScope = false
				b.pendingFuncDeclIdx = -1
				b.pendingFuncSelf = false
			}
			b.prevByte = ')'
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == ':':
			b.s.Pos++
			b.prevByte = ':'
		case c == '=':
			b.s.Pos++
			// `==` is comparison, not assignment.
			if !b.s.EOF() && b.s.Peek() == '=' {
				b.s.Pos++
				b.prevByte = '='
				break
			}
			// `local NAME = ...` — flush pending names as decls.
			b.flushLocalIfPending()
			// `for NAME = init, limit do` — first form (numeric).
			b.flushForVarsIfPending()
			b.prevByte = '='
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
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

	// Property access: `obj.method` / `obj:method`. While parsing a
	// `function NAME(...)` header, treat each chained segment as a
	// candidate decl name (the rightmost wins) and remember `:` as
	// method-style. Outside a func header, this is a normal property
	// ref. The function-header path also suppresses the property-ref
	// emission for the prior segment, since `function obj.method` is
	// declaring `method`, not referencing `obj`.
	if b.prevByte == '.' || b.prevByte == ':' {
		if b.funcPending {
			b.funcLastName = name
			b.funcLastSpan = span
			if b.prevByte == ':' {
				b.funcMethodStyle = true
			}
			b.prevByte = 'i'
			return
		}
		b.emitPropertyRef(name, span)
		b.prevByte = 'i'
		return
	}

	// Param scanning inside a function header.
	if b.paramPending {
		if name == "..." {
			// vararg — ignore
		} else {
			b.paramBuf = append(b.paramBuf, pendingDecl{name: name, span: span, kind: scope.KindParam})
		}
		b.prevByte = 'i'
		return
	}

	// for-loop variable list (between `for` and `in`/`=`).
	if b.forVarsPending {
		switch name {
		case "in":
			b.flushForVarsIfPending()
			b.prevByte = 'i'
			return
		case "do":
			// Shouldn't happen before in/= but handle gracefully.
			b.flushForVarsIfPending()
		default:
			b.forVarsBuf = append(b.forVarsBuf, pendingDecl{name: name, span: span, kind: scope.KindLet})
			b.prevByte = 'i'
			return
		}
	}

	// Local-statement LHS list (between `local` and `=`/newline).
	if b.localPending {
		// Special: `local function name(...)` — handled below in the
		// `function` branch; localPending stays on so handleFunctionName
		// knows the decl belongs to the current scope.
		if name == "function" {
			b.funcLocal = true
			b.localPending = false
			b.beginFunctionDecl(startByte)
			b.prevByte = 'k'
			return
		}
		// Otherwise it's `local x[, y]+ [= ...]`. Buffer as KindLet.
		b.emitDeclLocal(name, span, scope.KindLet)
		b.prevByte = 'i'
		return
	}

	switch name {
	case "function":
		b.beginFunctionDecl(startByte)
		b.prevByte = 'k'
	case "local":
		b.localPending = true
		b.prevByte = 'k'
	case "do":
		// Standalone `do` or the body opener after `while`/`for`. Either
		// way, open a block scope. (For `for`, also flush loop vars.)
		if b.forVarsPending {
			b.flushForVarsIfPending()
		}
		b.openScope(scope.ScopeBlock, startByte, luaOpenerDo)
		// `for NAME in EXPR do` — emit loop vars in the new scope.
		for _, p := range b.forVarsBuf {
			b.emitDeclLocal(p.name, p.span, p.kind)
		}
		b.forVarsBuf = b.forVarsBuf[:0]
		b.prevByte = 'k'
	case "then":
		b.openScope(scope.ScopeBlock, startByte, luaOpenerThen)
		b.prevByte = 'k'
	case "else":
		// Close prior then-block, open else-block.
		b.closeOpener(luaOpenerThen, startByte)
		b.openScope(scope.ScopeBlock, startByte, luaOpenerElse)
		b.prevByte = 'k'
	case "elseif":
		// Close prior then-block; the next `then` opens a new one.
		b.closeOpener(luaOpenerThen, startByte)
		b.prevByte = 'k'
	case "for":
		b.forVarsPending = true
		b.forVarsBuf = b.forVarsBuf[:0]
		b.prevByte = 'k'
	case "while":
		b.prevByte = 'k'
	case "if":
		b.prevByte = 'k'
	case "repeat":
		b.openScope(scope.ScopeBlock, startByte, luaOpenerRepeat)
		b.prevByte = 'k'
	case "until":
		b.closeOpener(luaOpenerRepeat, endByte)
		b.prevByte = 'k'
	case "end":
		b.closeOnEnd(endByte)
		b.prevByte = 'k'
	case "return", "break", "goto", "in", "and", "or", "not", "true", "false", "nil":
		b.prevByte = 'k'
	case "self":
		b.emitRef(name, span)
		b.prevByte = 'i'
	default:
		// Function-decl name parsing: after `function` we expect either
		// the function name (possibly dotted/colon-chained) or `(` for
		// an anonymous function. The rightmost segment is the eventual
		// decl name; we just record it here and emit on `(`.
		if b.funcPending {
			b.funcLastName = name
			b.funcLastSpan = span
			b.prevByte = 'i'
			return
		}
		b.emitRef(name, span)
		b.prevByte = 'i'
	}
}

// beginFunctionDecl is called on the `function` keyword. We don't yet
// know the name (or whether it's anonymous). emitFunctionDecl finalizes
// once the name ident appears; if `(` appears first, it's anonymous.
func (b *builder) beginFunctionDecl(startByte uint32) {
	b.funcPending = true
	b.funcLastName = ""
	b.funcMethodStyle = false
	b.funcStartByte = startByte
	b.pendingFuncScope = true
	b.pendingFuncDeclIdx = -1
	b.pendingFuncSelf = false
	b.pendingFuncOpenLine = b.s.Line
	// If the next non-ws char is '(', it's anonymous and we'll just open
	// the scope when we hit `(`. Otherwise the next ident becomes the name.
}

func (b *builder) emitFunctionDecl(name string, span scope.Span) {
	// Determine where to emit. `local function` → current scope.
	// Otherwise → file scope (Lua semantics: bare `function name` is
	// equivalent to `name = function ... end` in the enclosing chunk;
	// we treat top-level as file-scope decl, which is the rename case).
	declScope := b.firstScopeID()
	declKind := scope.DeclKind(scope.KindFunction)
	if b.funcLocal {
		declScope = b.currentScope()
	}
	if b.funcMethodStyle {
		declKind = scope.KindMethod
		b.pendingFuncSelf = true
	}
	idx := b.emitDeclAt(name, declKind, span, declScope)
	b.pendingFuncDeclIdx = idx
	b.funcPending = false
	b.funcLocal = false
}

func (b *builder) closeOnEnd(endByte uint32) {
	top := b.stack.Top()
	if top == nil {
		return
	}
	switch top.Data.opener {
	case luaOpenerFunction, luaOpenerDo, luaOpenerThen, luaOpenerElse:
		b.closeTopScope(endByte)
	default:
		// File or repeat — `end` shouldn't close these.
	}
}

// closeOpener pops scopes until one with the given opener is closed.
// No-op if no such opener is on the stack.
func (b *builder) closeOpener(opener luaOpener, endByte uint32) {
	for b.stack.Depth() > 0 {
		top := b.stack.Top()
		if top.Data.opener == opener {
			b.closeTopScope(endByte)
			return
		}
		// Don't pop unrelated scopes — bail out.
		if top.Data.opener == luaOpenerFile || top.Data.opener == luaOpenerFunction {
			return
		}
		b.closeTopScope(endByte)
	}
}

func (b *builder) flushLocalIfPending() {
	if b.localPending {
		b.localPending = false
	}
}

func (b *builder) flushForVarsIfPending() {
	if b.forVarsPending {
		b.forVarsPending = false
	}
}

func (b *builder) skipComment() {
	b.s.Advance(2) // consume `--`
	// Long comment: --[[ ... ]] or --[=[ ... ]=]
	if !b.s.EOF() && b.s.Peek() == '[' {
		save := b.s.Pos
		if b.scanLongString() {
			return
		}
		b.s.Pos = save
	}
	b.s.SkipLineComment()
}

// scanLongString consumes a Lua long bracket literal (string or long
// comment) starting at '[' (= must already be past the leading '--' for
// long comments). Returns true if a valid long bracket was consumed.
func (b *builder) scanLongString() bool {
	if b.s.EOF() || b.s.Peek() != '[' {
		return false
	}
	b.s.Pos++
	level := 0
	for !b.s.EOF() && b.s.Peek() == '=' {
		level++
		b.s.Pos++
	}
	if b.s.EOF() || b.s.Peek() != '[' {
		b.s.Pos -= level + 1
		return false
	}
	b.s.Pos++
	for !b.s.EOF() {
		if b.s.Peek() == '\n' {
			b.s.Next()
			continue
		}
		if b.s.Peek() == ']' {
			b.s.Pos++
			cnt := 0
			for !b.s.EOF() && b.s.Peek() == '=' {
				cnt++
				b.s.Pos++
			}
			if !b.s.EOF() && b.s.Peek() == ']' && cnt == level {
				b.s.Pos++
				return true
			}
			continue
		}
		b.s.Pos++
	}
	return true
}

// openScope pushes a new scope onto the stack and records it in res.
func (b *builder) openScope(kind scope.ScopeKind, startByte uint32, opener luaOpener) {
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
		Data:     scopeEntry{kind: kind, id: id, opener: opener},
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

// firstScopeID returns the file-scope ID (always 1 since we open it
// first).
func (b *builder) firstScopeID() scope.ScopeID {
	if len(b.res.Scopes) == 0 {
		return 0
	}
	return b.res.Scopes[0].ID
}

func (b *builder) emitDeclLocal(name string, span scope.Span, kind scope.DeclKind) int {
	return b.emitDeclAt(name, kind, span, b.currentScope())
}

func (b *builder) emitDeclAt(name string, kind scope.DeclKind, span scope.Span, scopeID scope.ScopeID) int {
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
		Exported:  scopeID == b.firstScopeID(),
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

// resolveRefs walks each ref's scope chain looking for a same-name decl
// in the value namespace. Property refs (NSField) skip the walk — they
// stay BindProbable / property_access.
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
			if builtins.Lua.Has(r.Name) {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   hashBuiltinDecl(r.Name),
					Reason: "builtin",
				}
			} else {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindUnresolved,
					Reason: "global",
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
	h.Write([]byte("<builtin:lua>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}
