// Package cpp is the C++ scope + binding extractor.
//
// Covers class / struct / union / namespace / enum / function / method
// declarations with brace-based scoping. Emits fields and methods in
// NSField so they do not shadow same-named top-level types. Handles
// the C preprocessor by skipping each preprocessor line as a single
// logical token (with line-continuation support).
//
// v1 limitations (documented here so future fixes have a single list):
//   - Templates: `template<...>` parameter lists are recognized and
//     skipped via angle-bracket matching, but type params are NOT
//     emitted as decls. Refs to template params inside bodies fall
//     through as ordinary idents.
//   - Out-of-line method definitions `void Foo::bar() {}` are emitted
//     as a ref to Foo plus a top-level function-like decl named `bar`.
//     The proper treatment (attaching bar to Foo as a method) needs
//     cross-pass resolution and is deferred.
//   - Lambda `[]()...{}` pushes a function scope via the usual `(`+`{`
//     machinery; lambda captures `[=, &x]` are skipped without emitting
//     decls for captured names.
//   - Operator overloading: `operator+` and friends are emitted as a
//     decl named "operator" (loses specificity; a v2 concern).
//   - Conditional compilation (`#ifdef` / `#endif`) is IGNORED — we
//     parse all branches as if visible.
//   - `using namespace X;` is parsed but does not expand X's contents
//     into the current scope — refs to names from X stay unresolved.
//   - `typedef` / `using T = Expr;` emit the alias as KindType; the
//     aliased type's identifiers fall through as ordinary refs.
//   - Field declarations inside a class body (`int x;`) do NOT emit x
//     as KindField — distinguishing type-name-name patterns without
//     type info is left for a v2 pass.
package cpp

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
)

// Parse extracts a scope.Result from a C or C++ source buffer. The
// same builder serves both — callers dispatch here for .c/.h/.cpp/
// .cc/.hpp/.hxx/.hh/.cxx. Pure C files parse the subset they use.
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

type scopeEntry struct {
	kind         scope.ScopeKind
	id           scope.ScopeID
	ownerDeclIdx int
}

type builder struct {
	file string
	res  *scope.Result
	s    lexkit.Scanner

	stack lexkit.ScopeStack[scopeEntry]

	stmtStart bool

	pendingScope *scope.ScopeKind
	declContext  scope.DeclKind

	paramListPending      bool
	inParamList           bool
	paramDepth            int
	paramSectionNeedsName bool

	pendingParamName string
	pendingParamSpan scope.Span

	pendingParams []pendingParam

	classDepth int

	// angleDepth counts unmatched '<' we believe are template brackets
	// (seen after an ident in a type-like context). While > 0 idents
	// are not emitted as refs — template-arg lists do not produce scope-
	// relevant refs in v1.
	angleDepth int

	inPreproc bool

	pendingFullStart uint32
	pendingOwnerDecl int

	controlBlockExpected bool
	compositeLitDepth    int

	// pendingTemplateAngle is set by the `template` keyword so the
	// next `<` is recognized as a template-parameter-list opener even
	// though prevByte is the `k` marker (not an ident).
	pendingTemplateAngle bool

	prevByte byte
}

type pendingParam struct {
	name string
	span scope.Span
	kind scope.DeclKind
}

func (b *builder) run() {
	for !b.s.EOF() {
		if b.inPreproc {
			b.skipPreprocLine()
			continue
		}
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.s.Pos++
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
		case c == '#' && b.stmtStart:
			b.inPreproc = true
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == '(':
			b.s.Pos++
			b.prevByte = '('
			if b.paramListPending {
				b.paramListPending = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
				b.pendingParamName = ""
			} else if b.inParamList {
				b.paramDepth++
			}
		case c == ')':
			b.s.Pos++
			b.prevByte = ')'
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.commitPendingParamSection()
					b.inParamList = false
					b.paramSectionNeedsName = false
				}
			}
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
			if b.inParamList && b.paramDepth == 1 {
				b.commitPendingParamSection()
				b.paramSectionNeedsName = true
				b.pendingParamName = ""
			}
		case c == ';':
			b.s.Pos++
			b.prevByte = ';'
			b.stmtStart = true
			b.declContext = ""
		case c == ':':
			b.s.Pos++
			if b.s.Peek() == ':' {
				b.s.Pos++
				b.prevByte = '.'
			} else {
				b.prevByte = ':'
			}
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == '-' && b.s.PeekAt(1) == '>':
			b.s.Advance(2)
			b.prevByte = '.'
		case c == '<':
			if b.prevByte == 'i' || b.angleDepth > 0 || b.pendingTemplateAngle {
				b.angleDepth++
				b.pendingTemplateAngle = false
				b.s.Pos++
			} else {
				b.s.Pos++
				b.prevByte = '<'
			}
		case c == '>':
			if b.angleDepth > 0 {
				b.angleDepth--
			}
			b.s.Pos++
			b.prevByte = '>'
		case isIdentStart(c):
			word := b.s.ScanIdentTable(&identStart, &identCont)
			if len(word) == 0 {
				b.s.Pos++
				continue
			}
			b.handleIdent(word)
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

func (b *builder) skipPreprocLine() {
	b.s.Pos++
	b.skipWS()
	directive := b.s.ScanIdentTable(&identStart, &identCont)
	switch string(directive) {
	case "define":
		b.skipWS()
		name := b.s.ScanIdentTable(&identStart, &identCont)
		if len(name) > 0 {
			start := uint32(b.s.Pos - len(name))
			end := uint32(b.s.Pos)
			b.emitDecl(string(name), scope.KindConst, mkSpan(start, end))
		}
	case "include":
		b.skipWS()
		start := b.s.Pos
		if b.s.Peek() == '<' {
			b.s.Pos++
			for !b.s.EOF() && b.s.Peek() != '>' && b.s.Peek() != '\n' {
				b.s.Pos++
			}
			if !b.s.EOF() && b.s.Peek() == '>' {
				b.s.Pos++
			}
		} else if b.s.Peek() == '"' {
			b.s.ScanSimpleString('"')
		}
		end := b.s.Pos
		if end > start {
			pathText := string(b.s.Src[start:end])
			if len(pathText) >= 2 {
				switch pathText[0] {
				case '<', '"':
					pathText = pathText[1 : len(pathText)-1]
				}
			}
			b.emitDecl(pathText, scope.KindImport, mkSpan(uint32(start), uint32(end)))
		}
	}
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '\\' && b.s.PeekAt(1) == '\n' {
			b.s.Advance(2)
			continue
		}
		if c == '\n' {
			b.s.Pos++
			break
		}
		b.s.Pos++
	}
	b.inPreproc = false
	b.stmtStart = true
}

func (b *builder) skipWS() {
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' {
			b.s.Pos++
			continue
		}
		break
	}
}

func (b *builder) commitPendingParamSection() {
	if b.pendingParamName != "" {
		b.pendingParams = append(b.pendingParams, pendingParam{
			name: b.pendingParamName,
			span: b.pendingParamSpan,
			kind: scope.KindParam,
		})
	}
	b.pendingParamName = ""
}

func (b *builder) handleIdent(word []byte) {
	if len(word) == 0 {
		return
	}
	startByte := uint32(b.s.Pos - len(word))
	endByte := uint32(b.s.Pos)
	name := string(word)
	b.stmtStart = false

	switch name {
	case "class", "struct", "union":
		k := scope.ScopeClass
		b.pendingScope = &k
		b.declContext = scope.KindClass
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "namespace":
		k := scope.ScopeNamespace
		b.pendingScope = &k
		b.declContext = scope.KindNamespace
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "enum":
		k := scope.ScopeBlock
		b.pendingScope = &k
		b.declContext = scope.KindEnum
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "typedef":
		b.declContext = scope.KindType
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "using":
		b.prevByte = 'k'
		return
	case "template":
		// The following `<...>` is a template param list; force angle-
		// bracket tracking even though prevByte is a keyword.
		b.pendingTemplateAngle = true
		b.prevByte = 'k'
		return
	case "if", "else", "for", "while", "do", "switch", "try", "catch":
		b.controlBlockExpected = true
		b.prevByte = 'k'
		return
	case "case", "default", "break", "continue", "return", "goto", "throw":
		b.prevByte = 'k'
		return
	case "public", "private", "protected", "virtual", "static", "const",
		"extern", "inline", "constexpr", "consteval", "noexcept",
		"explicit", "friend", "mutable", "volatile", "register", "auto",
		"signed", "unsigned", "short", "long", "new", "delete", "this",
		"true", "false", "nullptr", "NULL":
		b.prevByte = 'k'
		return
	case "operator":
		b.declContext = scope.KindFunction
		b.prevByte = 'k'
		return
	}

	if b.angleDepth > 0 {
		b.prevByte = 'i'
		return
	}

	if b.prevByte == '.' {
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	if b.compositeLitDepth > 0 && b.peekNonWSByte() == ':' {
		b.prevByte = 'i'
		return
	}

	if b.inParamList && b.paramDepth == 1 {
		b.pendingParamName = name
		b.pendingParamSpan = mkSpan(startByte, endByte)
		b.prevByte = 'i'
		return
	}

	if b.declContext != "" {
		kind := b.declContext
		b.emitDecl(name, kind, mkSpan(startByte, endByte))
		b.declContext = ""
		switch kind {
		case scope.KindFunction, scope.KindMethod:
			b.paramListPending = true
		}
		b.prevByte = 'i'
		return
	}

	// At file / namespace / class top-level, an ident followed by `(`
	// is a function or method name. This is a structural heuristic —
	// `foo(args)` at statement level without an enclosing expression
	// is almost always a decl. At local (function/block) scope it is a
	// call, not a decl, so we only apply this at the decl-permissive
	// scope kinds.
	scopeK := b.currentScopeKind()
	if b.peekNonWSByte() == '(' {
		switch scopeK {
		case scope.ScopeFile, scope.ScopeNamespace:
			b.emitDecl(name, scope.KindFunction, mkSpan(startByte, endByte))
			b.paramListPending = true
			b.prevByte = 'i'
			return
		case scope.ScopeClass:
			if b.classDepth == 0 {
				b.emitDecl(name, scope.KindMethod, mkSpan(startByte, endByte))
				b.paramListPending = true
				b.prevByte = 'i'
				return
			}
		}
	}

	b.emitRef(name, mkSpan(startByte, endByte))
	b.prevByte = 'i'
}

func (b *builder) handleOpenBrace() {
	b.s.Pos++
	prev := b.prevByte
	b.stmtStart = true
	b.prevByte = '{'

	if b.pendingScope != nil {
		kind := *b.pendingScope
		b.pendingScope = nil
		b.openScope(kind, uint32(b.s.Pos-1))
		if kind == scope.ScopeClass {
			b.classDepth = 0
		}
		if len(b.pendingParams) > 0 {
			for _, p := range b.pendingParams {
				b.emitDecl(p.name, p.kind, p.span)
			}
			b.pendingParams = nil
		}
		return
	}

	if len(b.pendingParams) > 0 {
		b.openScope(scope.ScopeFunction, uint32(b.s.Pos-1))
		for _, p := range b.pendingParams {
			b.emitDecl(p.name, p.kind, p.span)
		}
		b.pendingParams = nil
		return
	}

	if b.controlBlockExpected {
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		return
	}

	if prev == 'i' || prev == ']' || prev == ')' || prev == '}' {
		b.compositeLitDepth++
		return
	}

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
		Data:     scopeEntry{kind: kind, id: id, ownerDeclIdx: owner},
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
	for b.stack.Depth() > depth {
		b.closeTopScope(uint32(b.s.Pos))
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
	p := b.s.Pos
	for p < len(b.s.Src) {
		c := b.s.Src[p]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			p++
			continue
		}
		if c == '/' && p+1 < len(b.s.Src) && b.s.Src[p+1] == '/' {
			for p < len(b.s.Src) && b.s.Src[p] != '\n' {
				p++
			}
			continue
		}
		return c
	}
	return 0
}

func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span) {
	scopeID := b.currentScope()
	ns := scope.NSValue
	if kind == scope.KindField || kind == scope.KindMethod {
		if sk := b.currentScopeKind(); sk == scope.ScopeClass || sk == scope.ScopeInterface {
			ns = scope.NSField
		}
	}
	locID := hashLoc(b.file, span, name)
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
		scope.KindInterface, scope.KindType, scope.KindEnum,
		scope.KindNamespace:
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
		Namespace: scope.NSValue,
		Scope:     scopeID,
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
			p, pok := parent[cur]
			if !pok {
				break
			}
			if cur == 0 {
				break
			}
			cur = p
		}
		if !resolved {
			r.Binding = scope.RefBinding{
				Kind:   scope.BindUnresolved,
				Reason: "missing_import",
			}
		}
	}
}

var (
	identStart [256]bool
	identCont  [256]bool
)

func init() {
	for c := 0; c < 256; c++ {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c >= 0x80 {
			identStart[c] = true
			identCont[c] = true
		}
		if c >= '0' && c <= '9' {
			identCont[c] = true
		}
	}
}

func isIdentStart(c byte) bool { return identStart[c] }

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
