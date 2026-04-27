// Package cpp is the C++ scope + binding extractor.
//
// Covers class / struct / union / namespace / enum / function / method
// declarations with brace-based scoping. Emits fields and methods in
// NSField so they do not shadow same-named top-level types. Handles
// the C preprocessor by skipping each preprocessor line as a single
// logical token (with line-continuation support).
//
// v1 limitations (documented here so future fixes have a single list):
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
//   - Template specialization and instantiation are not modeled; refs
//     to template arguments resolve against the surrounding scope only.
//   - ADL (argument-dependent lookup) and namespace expansion are not
//     performed — name resolution is strictly lexical.
package cpp

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a C or C++ source buffer. The
// same builder serves both — callers dispatch here for .c/.h/.cpp/
// .cc/.hpp/.hxx/.hh/.cxx. Pure C files parse the subset they use.
// Parse extracts a scope.Result from a source buffer. File-scope
// decls hash with the file path.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path for
// file-scope DeclID hashing. When canonicalPath is non-empty,
// file-scope decls hash with it instead of the file path — so
// cross-file references via imports can bind to matching DeclIDs.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:                file,
		canonicalPath:       canonicalPath,
		res:                 &scope.Result{File: file},
		s:                   lexkit.New(src),
		pendingOwnerDecl:    -1,
		pendingClassDeclIdx: -1,
		classSuperTypes:     map[int][]string{},
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
	return b.res
}

type scopeEntry struct {
	kind         scope.ScopeKind
	id           scope.ScopeID
	ownerDeclIdx int
}

type builder struct {
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner

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

	// functionDeclPending is set when a function or method name has
	// been emitted as a decl. It survives the `(...)` param list and
	// any trailing qualifiers (`const`, `noexcept`, `override`, etc.),
	// clearing on `{` (body opens a proper function scope) or `;`
	// (prototype-only declaration). Without this flag, the `{` after
	// a function's `)` would be misclassified as a compound-literal
	// opener because prevByte == ')' matches the composite-lit
	// heuristic in handleOpenBrace.
	functionDeclPending bool

	// scopeQualifierPending is set by `::` so the next ident's
	// emitPropertyRef stamps Reason="scope_qualified_access" instead
	// of "property_access". The distinction lets the post-pass
	// resolver target qualified chains (`Foo::bar`, `A::B::C`) without
	// over-firing on object member access (`obj.name` / `obj->name`).
	scopeQualifierPending bool

	// pendingTemplateAngle is set by the `template` keyword so the
	// next `<` is recognized as a template-parameter-list opener even
	// though prevByte is the `k` marker (not an ident).
	pendingTemplateAngle bool

	// Template parameter tracking. When inTemplateParams is true, we
	// are inside a `template<...>` decl header and angle-bracket depth
	// is tracked in templateAngleDepth. Idents captured here become
	// pending template-param decls that are flushed into the next
	// opened class/function/struct scope.
	inTemplateParams         bool
	templateAngleDepth       int
	templateSectionNeedsName bool
	templateParamName        string
	templateParamSpan        scope.Span
	pendingTemplateParams    []pendingParam

	// Class-body field tracking. When inside a class/struct scope at
	// classBodyDepth == 0 (i.e., top of the body, not inside a method
	// or nested block), consecutive idents on a statement are buffered
	// in classStmtIdents. On `;` or `,` the last ident is emitted as
	// KindField and earlier ones as type refs. On `(` (method decl)
	// or `{` (method body / nested block) the buffer is flushed as
	// plain refs (no field emitted).
	classStmtIdents []pendingParam

	// sawStatic: set on `static`; cleared on `;` or on emitDecl for
	// function/method/var/const. File-scope functions/vars declared
	// `static` are NOT Exported (internal linkage).
	sawStatic bool

	// Class-supertype tracking — populates Decl.SuperTypes for class
	// / struct decls so the cross-file hierarchy walker
	// (EmitOverrideSpans) can walk both directions.
	//
	// pendingClassDeclIdx holds the just-emitted class/struct decl
	// until its body `{` opens. inSuperTypes is on between the
	// inheritance `:` and the body `{`. supertypeAngleDepth tracks
	// generic args inside the supertype clause (`: Container<T>`)
	// — C++ template args are independent of the decl-name template
	// machinery so we keep our own counter. classBaseLastIdent
	// captures qualified `std::iostream::base` via "last ident
	// wins" so the leaf segment is what reaches Decl.SuperTypes.
	//
	// Access modifiers (public/private/protected) and `virtual` are
	// keywords that flow through unrecognised — they're recognised
	// as keywords but don't affect the supertype tracker. The
	// classBaseLastIdent overwrite-on-each-ident pattern means
	// modifier idents don't end up captured as bases (the actual
	// type ident comes after).
	pendingClassDeclIdx int
	inSuperTypes        bool
	supertypeAngleDepth int
	classBaseLastIdent  string
	classSuperTypes     map[int][]string

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
			if b.inTemplateParams && b.templateAngleDepth == 1 {
				b.commitPendingTemplateParamSection()
				b.templateSectionNeedsName = true
				b.templateParamName = ""
			} else if b.inParamList && b.paramDepth == 1 {
				b.commitPendingParamSection()
				b.paramSectionNeedsName = true
				b.pendingParamName = ""
			} else if b.inSuperTypes && b.supertypeAngleDepth == 0 {
				// Multi-inheritance separator (`: A, B, C`).
				b.flushSuperTypeArg()
			} else if b.currentScopeKind() == scope.ScopeClass && b.classDepth == 0 {
				b.flushClassField()
			}
		case c == ';':
			if b.currentScopeKind() == scope.ScopeClass && b.classDepth == 0 {
				b.flushClassField()
			}
			b.s.Pos++
			b.prevByte = ';'
			b.stmtStart = true
			b.declContext = ""
			b.sawStatic = false
			// A prototype-only function declaration (`void foo();`)
			// terminates here without opening a body.
			b.functionDeclPending = false
		case c == ':':
			b.s.Pos++
			if b.s.Peek() == ':' {
				b.s.Pos++
				b.prevByte = '.'
				b.scopeQualifierPending = true
			} else {
				b.prevByte = ':'
				// Inheritance `:` after a class/struct header.
				// pendingClassDeclIdx is set between the class
				// decl name and its body `{`; cleared on body
				// open, so other `:` uses (constructor init
				// list, ternary, bit-field) are filtered.
				if b.pendingClassDeclIdx >= 0 && !b.inSuperTypes {
					b.inSuperTypes = true
					b.classBaseLastIdent = ""
					b.supertypeAngleDepth = 0
				}
			}
		case c == '.':
			b.s.Pos++
			b.prevByte = '.'
		case c == '-' && b.s.PeekAt(1) == '>':
			b.s.Advance(2)
			b.prevByte = '.'
		case c == '<':
			if b.pendingTemplateAngle {
				b.pendingTemplateAngle = false
				b.inTemplateParams = true
				b.templateAngleDepth = 1
				b.templateSectionNeedsName = true
				b.angleDepth++
				b.s.Pos++
			} else if b.inTemplateParams {
				b.templateAngleDepth++
				b.angleDepth++
				b.s.Pos++
			} else if b.prevByte == 'i' || b.angleDepth > 0 {
				b.angleDepth++
				if b.inSuperTypes {
					b.supertypeAngleDepth++
				}
				b.s.Pos++
			} else {
				b.s.Pos++
				b.prevByte = '<'
			}
		case c == '>':
			if b.inTemplateParams {
				b.templateAngleDepth--
				if b.templateAngleDepth == 0 {
					b.commitPendingTemplateParamSection()
					b.inTemplateParams = false
				}
			}
			if b.angleDepth > 0 {
				b.angleDepth--
			}
			if b.inSuperTypes && b.supertypeAngleDepth > 0 {
				b.supertypeAngleDepth--
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
		quoteStyle := ""
		if b.s.Peek() == '<' {
			quoteStyle = "<>"
			b.s.Pos++
			for !b.s.EOF() && b.s.Peek() != '>' && b.s.Peek() != '\n' {
				b.s.Pos++
			}
			if !b.s.EOF() && b.s.Peek() == '>' {
				b.s.Pos++
			}
		} else if b.s.Peek() == '"' {
			quoteStyle = "\""
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
			// Stamp Signature = "<path>\x00<quoteStyle>" so the Phase
			// 1 include-graph resolver (imports_cpp.go) can resolve
			// quoted includes against repo files while leaving system
			// <> includes alone.
			if len(b.res.Decls) > 0 && quoteStyle != "" {
				b.res.Decls[len(b.res.Decls)-1].Signature = pathText + "\x00" + quoteStyle
			}
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

func (b *builder) commitPendingTemplateParamSection() {
	if b.templateParamName != "" {
		b.pendingTemplateParams = append(b.pendingTemplateParams, pendingParam{
			name: b.templateParamName,
			span: b.templateParamSpan,
			kind: scope.KindType,
		})
	}
	b.templateParamName = ""
}

// flushClassField emits the buffered class-body idents on a `;` or `,`:
// the last ident becomes a KindField, earlier ones are type refs.
func (b *builder) flushClassField() {
	if len(b.classStmtIdents) == 0 {
		return
	}
	last := b.classStmtIdents[len(b.classStmtIdents)-1]
	for _, id := range b.classStmtIdents[:len(b.classStmtIdents)-1] {
		b.emitRef(id.name, id.span)
	}
	b.emitDecl(last.name, scope.KindField, last.span)
	b.classStmtIdents = nil
}

// flushClassFieldAsRefs drains the buffered class-body idents as plain
// refs (no field decl). Called when a `{` or method-decl `(` appears,
// meaning the buffered idents were the return type of a method or
// preamble to a nested scope.
func (b *builder) flushClassFieldAsRefs() {
	for _, id := range b.classStmtIdents {
		b.emitRef(id.name, id.span)
	}
	b.classStmtIdents = nil
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
		// Three forms:
		//   1) `using namespace Foo;`     → KindImport, Sig=`Foo\x00*`.
		//   2) `using Foo::Bar;`          → KindImport Bar, Sig=`Foo\x00Bar`.
		//   3) `using T = Expr;`          → type alias; falls through.
		b.prevByte = 'k'
		if b.handleUsingDirective(startByte) {
			return
		}
		b.declContext = scope.KindType
		b.pendingFullStart = startByte + 1
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
	case "static":
		// `static` at file scope marks internal linkage. Cleared on
		// `;` or on decl emission.
		b.sawStatic = true
		b.prevByte = 'k'
		return
	case "public", "private", "protected", "virtual", "const",
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

	if b.inTemplateParams && b.templateAngleDepth == 1 {
		// typename / class inside `<...>` introduce a param; they are
		// not captured as the param name themselves.
		if name == "typename" || name == "class" {
			b.templateSectionNeedsName = true
			b.prevByte = 'i'
			return
		}
		// First non-keyword ident per section is the param name. A
		// following ident (e.g. default value or trailing junk) is
		// ignored until the next `,` or `>` commits.
		if b.templateSectionNeedsName {
			b.templateParamName = name
			b.templateParamSpan = mkSpan(startByte, endByte)
			b.templateSectionNeedsName = false
		}
		b.prevByte = 'i'
		return
	}

	if b.angleDepth > 0 {
		b.prevByte = 'i'
		return
	}

	if b.prevByte == '.' {
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		// Qualified supertype name: `: std::iostream::base` —
		// each `::`-separated segment after the first overwrites
		// classBaseLastIdent so the leaf wins.
		if b.inSuperTypes && b.supertypeAngleDepth == 0 {
			b.classBaseLastIdent = name
		}
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
			b.functionDeclPending = true
		case scope.KindClass:
			// Track the class/struct/union so the upcoming
			// `: public Base, ...` clause appends supertypes.
			b.pendingClassDeclIdx = len(b.res.Decls) - 1
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
			b.functionDeclPending = true
			b.prevByte = 'i'
			return
		case scope.ScopeClass:
			if b.classDepth == 0 {
				// A method decl discards any class-body ident buffer —
				// those idents were the return type; emit them as refs.
				b.flushClassFieldAsRefs()
				b.emitDecl(name, scope.KindMethod, mkSpan(startByte, endByte))
				b.paramListPending = true
				b.functionDeclPending = true
				b.prevByte = 'i'
				return
			}
		}
	}

	// Buffer idents inside a class body at top depth; the last one on
	// the statement becomes a KindField when we hit `;` or `,`.
	if scopeK == scope.ScopeClass && b.classDepth == 0 {
		b.classStmtIdents = append(b.classStmtIdents, pendingParam{
			name: name,
			span: mkSpan(startByte, endByte),
		})
		b.prevByte = 'i'
		return
	}

	b.emitRef(name, mkSpan(startByte, endByte))
	// Supertype name capture: while inside the inheritance clause
	// at depth 0 (outside `<...>`), record the latest ident.
	if b.inSuperTypes && b.supertypeAngleDepth == 0 {
		b.classBaseLastIdent = name
	}
	b.prevByte = 'i'
}

// flushSuperTypeArg appends the captured base ident to
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

// flushSuperTypeSection finalises the inheritance window and
// clears inSuperTypes. Called on body `{`.
func (b *builder) flushSuperTypeSection() {
	b.flushSuperTypeArg()
	b.inSuperTypes = false
	b.supertypeAngleDepth = 0
}

// handleUsingDirective scans a `using ...;` statement after the
// `using` keyword has been consumed. Returns true if it handled a
// directive or using declaration (and consumed up through `;`).
// Returns false for `using T = Expr;` (type alias), leaving the
// scanner at its original position so the caller falls through.
//
// Directive form:   `using namespace Foo[::Bar]*;` → emits KindImport
//                   name=<full>, Signature=`<full>\x00*`.
// Declaration form: `using Qual::Name;`            → emits KindImport
//                   name=Name, Signature=`Qual\x00Name`.
func (b *builder) handleUsingDirective(keywordStart uint32) bool {
	b.skipUsingWS()
	if b.s.EOF() {
		return false
	}
	savePos := b.s.Pos

	// `using namespace Foo[::Bar]*;`
	if b.peekCppKeyword("namespace") {
		b.s.Pos += len("namespace")
		b.skipUsingWS()
		full := b.scanQualifiedName()
		if full == "" {
			b.consumeToSemicolon()
			return true
		}
		nameSpan := mkSpan(keywordStart, uint32(b.s.Pos))
		b.emitDecl(full, scope.KindImport, nameSpan)
		if len(b.res.Decls) > 0 {
			b.res.Decls[len(b.res.Decls)-1].Signature = full + "\x00*"
		}
		b.consumeToSemicolon()
		return true
	}

	// Using declaration or type alias.
	qual := b.scanQualifiedName()
	if qual == "" {
		b.s.Pos = savePos
		return false
	}
	b.skipUsingWS()
	if !b.s.EOF() && b.s.Peek() == '=' {
		// Type alias: rewind so the outer loop re-scans the alias
		// ident under declContext = KindType.
		b.s.Pos = savePos
		return false
	}
	idx := lastDoubleColon(qual)
	if idx < 0 {
		// `using Name;` (namespace-scope redeclaration). Not modeled.
		b.consumeToSemicolon()
		return true
	}
	qualifier := qual[:idx]
	member := qual[idx+2:]
	nameSpan := mkSpan(keywordStart, uint32(b.s.Pos))
	b.emitDecl(member, scope.KindImport, nameSpan)
	if len(b.res.Decls) > 0 {
		b.res.Decls[len(b.res.Decls)-1].Signature = qualifier + "\x00" + member
	}
	b.consumeToSemicolon()
	return true
}

// skipUsingWS skips whitespace / newlines / comments inside a `using`
// statement.
func (b *builder) skipUsingWS() {
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			b.s.Pos++
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		default:
			return
		}
	}
}

// peekCppKeyword reports whether the next ident scan would yield
// word. Does not advance the scanner.
func (b *builder) peekCppKeyword(word string) bool {
	if b.s.EOF() {
		return false
	}
	n := len(word)
	if b.s.Pos+n > len(b.s.Src) {
		return false
	}
	for i := 0; i < n; i++ {
		if b.s.Src[b.s.Pos+i] != word[i] {
			return false
		}
	}
	if b.s.Pos+n < len(b.s.Src) {
		c := b.s.Src[b.s.Pos+n]
		if identCont[c] {
			return false
		}
	}
	return true
}

// scanQualifiedName reads ident (`::` ident)* from the current
// position, returning the full `A::B::C` text. Advances past the
// name. Returns "" when no leading ident is present.
func (b *builder) scanQualifiedName() string {
	first := b.s.ScanIdentTable(&identStart, &identCont)
	if len(first) == 0 {
		return ""
	}
	parts := string(first)
	for {
		save := b.s.Pos
		b.skipUsingWS()
		if b.s.Pos+1 >= len(b.s.Src) || b.s.Peek() != ':' || b.s.PeekAt(1) != ':' {
			b.s.Pos = save
			return parts
		}
		b.s.Pos += 2
		b.skipUsingWS()
		next := b.s.ScanIdentTable(&identStart, &identCont)
		if len(next) == 0 {
			return parts
		}
		parts += "::" + string(next)
	}
}

// consumeToSemicolon advances past the next top-level `;`, swallowing
// matched `{}` groups along the way.
func (b *builder) consumeToSemicolon() {
	depth := 0
	for !b.s.EOF() {
		c := b.s.Peek()
		switch {
		case c == '{':
			depth++
			b.s.Pos++
		case c == '}':
			if depth > 0 {
				depth--
			}
			b.s.Pos++
		case c == ';' && depth == 0:
			b.s.Pos++
			b.stmtStart = true
			b.declContext = ""
			b.sawStatic = false
			b.prevByte = ';'
			return
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '"' || c == '\'':
			b.s.ScanSimpleString(c)
		default:
			b.s.Pos++
		}
	}
}

// lastDoubleColon returns the byte index of the last `::` in s, or
// -1 if not found.
func lastDoubleColon(s string) int {
	for i := len(s) - 2; i >= 0; i-- {
		if s[i] == ':' && s[i+1] == ':' {
			return i
		}
	}
	return -1
}

func (b *builder) handleOpenBrace() {
	// Any buffered class-body idents at this point were NOT fields —
	// flush them as plain refs so they don't leak into the inner scope.
	if b.currentScopeKind() == scope.ScopeClass && b.classDepth == 0 {
		b.flushClassFieldAsRefs()
	}

	b.s.Pos++
	prev := b.prevByte
	b.stmtStart = true
	b.prevByte = '{'

	// Class/struct body opens — flush the supertype section onto
	// Decl.SuperTypes and clear the trackers.
	if b.inSuperTypes {
		b.flushSuperTypeSection()
	}

	if b.pendingScope != nil {
		kind := *b.pendingScope
		b.pendingScope = nil
		b.openScope(kind, uint32(b.s.Pos-1))
		if kind == scope.ScopeClass {
			b.classDepth = 0
			b.pendingClassDeclIdx = -1
		}
		if len(b.pendingParams) > 0 {
			for _, p := range b.pendingParams {
				b.emitDecl(p.name, p.kind, p.span)
			}
			b.pendingParams = nil
		}
		// Flush template params into the class/struct/union scope.
		if len(b.pendingTemplateParams) > 0 {
			for _, tp := range b.pendingTemplateParams {
				b.emitDecl(tp.name, tp.kind, tp.span)
			}
			b.pendingTemplateParams = nil
		}
		return
	}

	if len(b.pendingParams) > 0 {
		b.openScope(scope.ScopeFunction, uint32(b.s.Pos-1))
		for _, p := range b.pendingParams {
			b.emitDecl(p.name, p.kind, p.span)
		}
		b.pendingParams = nil
		// Flush template params into the function scope.
		if len(b.pendingTemplateParams) > 0 {
			for _, tp := range b.pendingTemplateParams {
				b.emitDecl(tp.name, tp.kind, tp.span)
			}
			b.pendingTemplateParams = nil
		}
		return
	}

	if b.controlBlockExpected {
		b.controlBlockExpected = false
		b.openScope(scope.ScopeBlock, uint32(b.s.Pos-1))
		return
	}

	// A function/method decl emitted upstream and whose param list has
	// now closed opens a proper function scope here. This must precede
	// the composite-lit heuristic below because `) {` matches both
	// (prev==')'), and function bodies would otherwise be silently
	// misclassified as compound literals.
	if b.functionDeclPending {
		b.functionDeclPending = false
		b.openScope(scope.ScopeFunction, uint32(b.s.Pos-1))
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

// namespaceQualifier returns the dotted namespace path (e.g.
// "utils::inner") if every enclosing scope above file-scope is a
// namespace, otherwise "". Used by emitDecl to canonicalize IDs of
// decls that live directly inside namespaces — without this, the same
// `int utils::compute(int)` in lib.hpp and lib.cpp would hash to
// different DeclIDs because their namespace ScopeID is per-file.
func (b *builder) namespaceQualifier() string {
	entries := b.stack.Entries()
	if len(entries) <= 1 {
		// No enclosing namespaces above the file scope.
		return ""
	}
	var parts []string
	for i := range entries {
		e := &entries[i]
		if i == 0 && e.Data.kind == scope.ScopeFile {
			continue
		}
		if e.Data.kind != scope.ScopeNamespace {
			return ""
		}
		owner := e.Data.ownerDeclIdx
		if owner < 0 || owner >= len(b.res.Decls) {
			return ""
		}
		parts = append(parts, b.res.Decls[owner].Name)
	}
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += "::" + parts[i]
	}
	return out
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
	// File-scope decls hash with canonicalPath when set, so
	// cross-file references through imports/includes bind to
	// matching DeclIDs. Decls directly inside one or more
	// namespaces are also canonicalized: the namespace qualifier
	// (e.g. "utils::inner") gets folded into hashPath and scopeID
	// is collapsed to file-scope so `int utils::compute(int)` in
	// lib.hpp and lib.cpp share a DeclID. Other nested-scope decls
	// (class/function/block locals) keep the file path + local
	// scopeID for shadow correctness.
	hashPath := b.file
	hashScope := scopeID
	if b.canonicalPath != "" {
		if scopeID == scope.ScopeID(1) {
			hashPath = b.canonicalPath
		} else if qual := b.namespaceQualifier(); qual != "" {
			hashPath = b.canonicalPath + "::" + qual
			hashScope = scope.ScopeID(1)
		}
	}
	declID := hashDecl(hashPath, name, ns, hashScope)

	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}

	idx := len(b.res.Decls)
	exported := b.computeExported(kind, scopeID)
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

	switch kind {
	case scope.KindFunction, scope.KindMethod, scope.KindClass,
		scope.KindInterface, scope.KindType, scope.KindEnum,
		scope.KindNamespace:
		b.pendingOwnerDecl = idx
	}
	// `static` applies to one decl only. Clear once consumed by a
	// function/method/var/const.
	switch kind {
	case scope.KindFunction, scope.KindMethod, scope.KindVar, scope.KindConst:
		b.sawStatic = false
	}
	b.pendingFullStart = 0
}

// computeExported decides whether a freshly-emitted decl has external
// linkage for the Phase 1 include-graph resolver. C++ rules:
//   - NSField members, params, imports, non-file/-namespace-scope
//     decls: never exported.
//   - At file or namespace scope: classes / interfaces / enums /
//     typedefs / namespaces default to external linkage.
//   - Functions and file-scope vars/consts: external UNLESS `static`.
//   - Out-of-line method definitions in .cpp (emitted as KindFunction
//     at file/namespace scope) are exported — linker-visible.
func (b *builder) computeExported(kind scope.DeclKind, scopeID scope.ScopeID) bool {
	switch kind {
	case scope.KindField, scope.KindParam, scope.KindImport, scope.KindLet:
		return false
	}
	sk := scope.ScopeFile
	for _, s := range b.res.Scopes {
		if s.ID == scopeID {
			sk = s.Kind
			break
		}
	}
	if sk != scope.ScopeFile && sk != scope.ScopeNamespace {
		return false
	}
	switch kind {
	case scope.KindClass, scope.KindInterface, scope.KindEnum,
		scope.KindType, scope.KindNamespace:
		return true
	case scope.KindFunction, scope.KindMethod, scope.KindVar, scope.KindConst:
		return !b.sawStatic
	}
	return false
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
	reason := "property_access"
	if b.scopeQualifierPending {
		reason = "scope_qualified_access"
		b.scopeQualifierPending = false
	}
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSValue,
		Scope:     scopeID,
		Binding: scope.RefBinding{
			Kind:   scope.BindProbable,
			Reason: reason,
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
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "scope_qualified_access" {
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
			if builtins.CPP.Has(r.Name) {
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

	b.resolveQualifiedMemberRefs()
}

// resolveQualifiedMemberRefs rebinds scope_qualified_access refs
// (emitted from `Foo::bar` where the `::` was seen) by finding the
// preceding ref's target scope and looking up the member inside it.
// Handles chains like `A::B::C` because each link of the chain gets
// rebound in source order before being read as "preceding ref" for
// the next link.
//
// Only same-file resolution here — cross-file namespace qualification
// (e.g., pytorch's `at::Tensor` where `at` is declared in a header)
// happens in the store-level resolver.
func (b *builder) resolveQualifiedMemberRefs() {
	if len(b.res.Refs) < 2 {
		return
	}
	// DeclID → body ScopeID for every scope-owning decl (namespace,
	// class, struct, interface).
	ownerBody := make(map[scope.DeclID]scope.ScopeID)
	for _, s := range b.res.Scopes {
		var want func(scope.DeclKind) bool
		switch s.Kind {
		case scope.ScopeNamespace:
			want = func(k scope.DeclKind) bool { return k == scope.KindNamespace }
		case scope.ScopeClass:
			want = func(k scope.DeclKind) bool { return k == scope.KindClass || k == scope.KindType }
		case scope.ScopeInterface:
			want = func(k scope.DeclKind) bool { return k == scope.KindInterface }
		default:
			continue
		}
		var best *scope.Decl
		for i := range b.res.Decls {
			d := &b.res.Decls[i]
			if d.Scope != s.Parent {
				continue
			}
			if !want(d.Kind) {
				continue
			}
			if d.Span.EndByte > s.Span.StartByte {
				continue
			}
			if best == nil || d.Span.EndByte > best.Span.EndByte {
				best = d
			}
		}
		if best != nil {
			ownerBody[best.ID] = s.ID
		}
	}
	if len(ownerBody) == 0 {
		return
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
	for i := 1; i < len(b.res.Refs); i++ {
		r := &b.res.Refs[i]
		if r.Binding.Reason != "scope_qualified_access" {
			continue
		}
		prev := &b.res.Refs[i-1]
		if prev.Binding.Kind != scope.BindResolved {
			continue
		}
		body, ok := ownerBody[prev.Binding.Decl]
		if !ok {
			continue
		}
		// Try value, type, field namespaces in turn.
		for _, ns := range []scope.Namespace{scope.NSValue, scope.NSType, scope.NSField, r.Namespace} {
			if d, ok := byKey[key{scope: body, name: r.Name, ns: ns}]; ok {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   d.ID,
					Reason: "qualified_member",
				}
				break
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

func hashBuiltinDecl(name string) scope.DeclID {
	h := sha256.New()
	h.Write([]byte("<builtin:cpp>"))
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
