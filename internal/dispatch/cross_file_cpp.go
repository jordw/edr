package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// cppCrossFileSpans is the Cpp branch of
// scopeAwareCrossFileSpans. Uses canonical-DeclID matching via the
// namespace abstraction; falls through (returns empty map) when no
// cross-file matches are found, so the dispatch switch can defer
// to the generic ref-filtering path for cases this resolver
// doesn't model yet.
func cppCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	if !cpp_ext_matches(sym.File, []string{".cpp", ".cxx", ".cc", ".c++", ".hpp", ".hxx", ".hh", ".h++", ".h"}) {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewCppResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)
	if canonical == "" || targetRes == nil {
		return nil, false
	}
	// The C++ symbol-index parser doesn't extract `Type::` qualifiers
	// from out-of-line method defs, so sym.Receiver can be empty for
	// `int Counter::value() const { … }`. Peek at the source for a
	// `Type::name` pattern just before sym.StartByte to recover it.
	derivedReceiver := sym.Receiver
	if derivedReceiver == "" {
		src := resolver.Source(sym.File)
		derivedReceiver = cppDeriveReceiver(src, sym.StartByte, sym.EndByte, sym.Name)
	}

	var target *scope.Decl
	for i := range targetRes.Decls {
		d := &targetRes.Decls[i]
		if d.Name != sym.Name {
			continue
		}
		if d.Span.StartByte >= sym.StartByte && d.Span.EndByte <= sym.EndByte {
			target = d
			break
		}
	}
	// C++ out-of-line method defs (`int Counter::value() const {...}`)
	// live in .cpp, but the decl itself is in the sibling .hpp. When
	// sym.File is the .cpp, targetRes.Decls won't contain the method —
	// search same-dir siblings for a method named sym.Name inside a
	// class scope owned by sym.Receiver.
	if target == nil && derivedReceiver != "" {
		for _, sib := range resolver.SamePackageFiles(sym.File) {
			sres := resolver.Result(sib)
			if sres == nil {
				continue
			}
			for i := range sres.Decls {
				d := &sres.Decls[i]
				if d.Name != sym.Name || d.Kind != scope.KindMethod {
					continue
				}
				// Enclosing scope must be ScopeClass and owned by a
				// class decl matching sym.Receiver.
				sid := int(d.Scope) - 1
				if sid < 0 || sid >= len(sres.Scopes) {
					continue
				}
				if sres.Scopes[sid].Kind != scope.ScopeClass {
					continue
				}
				var owner *scope.Decl
				var bestEnd uint32
				for j := range sres.Decls {
					c := &sres.Decls[j]
					if c.Kind != scope.KindClass {
						continue
					}
					if c.Span.EndByte > sres.Scopes[sid].Span.StartByte {
						continue
					}
					if c.Span.EndByte <= bestEnd {
						continue
					}
					bestEnd = c.Span.EndByte
					owner = c
				}
				if owner != nil && owner.Name == derivedReceiver {
					target = d
					break
				}
			}
			if target != nil {
				break
			}
		}
	}
	if target == nil {
		return out, true
	}

	if derivedReceiver != "" {
		cppEmitHierarchySpans(out, resolver, sym, target, derivedReceiver)
	}

	candidates := map[string]bool{}
	if refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File); err == nil {
		for _, r := range refs {
			if r.File != sym.File {
				candidates[r.File] = true
			}
		}
	}
	for _, sib := range resolver.SamePackageFiles(sym.File) {
		candidates[sib] = true
	}
	// Out-of-line method defs: sym.File (the .cpp) has a
	// `Type::name` ref that the same-file pass missed (same-file
	// target resolution looks for a decl, not a scope_qualified
	// ref). Include sym.File here so those refs get rewritten too.
	if derivedReceiver != "" && derivedReceiver != sym.Receiver {
		candidates[sym.File] = true
	}

	// For method renames, accept property-access refs whose base
	// local var is of an acceptable type (concrete receiver).
	isMethod := derivedReceiver != ""
	acceptableTypes := map[string]bool{}
	if derivedReceiver != "" {
		acceptableTypes[derivedReceiver] = true
		// Hierarchy: sym.File for the class decl is usually the
		// header (.hpp). Walk both sym.File and any same-basename
		// sibling for the inheritance graph.
		hierSrc := resolver.Source(sym.File)
		for _, t := range namespace.CppRelatedTypes(hierSrc, derivedReceiver) {
			acceptableTypes[t] = true
		}
		for _, sib := range resolver.SamePackageFiles(sym.File) {
			for _, t := range namespace.CppRelatedTypes(resolver.Source(sib), derivedReceiver) {
				acceptableTypes[t] = true
			}
		}
	}
	// Namespace-qualifier of the target. Non-empty for free
	// functions / vars declared inside `namespace utils { ... }`;
	// empty for top-level decls and methods. Drives the namespace-
	// aware matching below: candidates that import the target file
	// are admitted (skipping the namespace-name precondition), and
	// `utils::compute()` call sites are recognized via their
	// scope_qualified_access ref.
	targetNs := cppNamespaceOf(targetRes, target)

	pop := namespace.CppPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		// Methods don't live at file scope so their names aren't in
		// the namespace. Admit every candidate when renaming a
		// method and rely on per-ref disambiguation below.
		// Namespace functions also bypass the file-scope check —
		// the target lives at `utils::compute`, not at the
		// candidate's top level, so ns.Matches would never fire.
		// Per-ref scope_qualified_access matching does the work.
		if !isMethod && targetNs == "" && !ns.Matches(sym.Name, target.ID) {
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		src := resolver.Source(cand)
		var varTypes map[string]string
		if isMethod {
			varTypes = buildVarTypes(candRes, src)
			// C++ builder doesn't emit user-defined variable decls
			// from `Type name;` patterns — both idents come through
			// as refs. Supplement varTypes by pairing consecutive
			// refs `Type name` where the name isn't followed by
			// another `::`, `.`, or opening brace (which would
			// indicate a different construct).
			cppPairRefsIntoVarTypes(candRes, src, varTypes)
		}
		// Shadow guard: collect `Type name` patterns inside function
		// bodies. The C++ builder doesn't emit local var decls, so
		// without this gate a global rename would also rewrite a
		// same-name local variable's decl + uses.
		shadows := cppCollectLocalShadows(candRes, src)

		// File-scope decls whose ID matches the target (the
		// canonical-path merge brings prototypes / siblings here).
		for _, d := range candRes.Decls {
			if d.ID != target.ID {
				continue
			}
			if d.Kind == scope.KindImport {
				continue
			}
			out[cand] = append(out[cand], span{
				start: d.Span.StartByte,
				end:   d.Span.EndByte,
			})
		}

		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			// Local-shadow skip: ref name matches and ref falls inside
			// a function whose body declares a local of the same name.
			shadowed := false
			for _, sh := range shadows {
				if sh.name == ref.Name &&
					sh.funcStart <= ref.Span.StartByte && ref.Span.EndByte <= sh.funcEnd {
					shadowed = true
					break
				}
			}
			if shadowed {
				continue
			}
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					if local.ID != target.ID {
						continue
					}
				}
			}
			// Disambiguate qualified accesses: `x.foo()`, `x->foo()`,
			// and out-of-line `Type::foo` defs all need to filter
			// by receiver type. The cpp scope builder reports
			// `.`/`->` as reason=property_access and `::` as
			// reason=scope_qualified_access — both go through this
			// branch.
			isQualified := ref.Binding.Reason == "property_access" || ref.Binding.Reason == "scope_qualified_access"
			if isMethod && isQualified && ref.Span.StartByte > 0 && len(src) > 0 {
				prev := src[ref.Span.StartByte-1]
				isDot := prev == '.'
				isArrow := ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == '-' && prev == '>'
				isScope := ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == ':' && prev == ':'
				// Accept x.foo() / x->foo() when x's declared type is
				// in acceptableTypes; accept Type::member when Type
				// itself is the acceptable receiver. Span stays
				// identifier-only.
				accept := false
				if isDot || isArrow {
					baseIdent := cppBaseIdentBefore(src, ref.Span.StartByte, isArrow)
					if baseIdent != "" && acceptableTypes[varTypes[baseIdent]] {
						accept = true
					}
				}
				if isScope {
					baseIdent := cppBaseIdentBefore(src, ref.Span.StartByte, false)
					if acceptableTypes[baseIdent] {
						accept = true
					}
				}
				if !accept {
					continue
				}
			}
			// Namespace function call: `utils::compute()` reaches us
			// as reason=scope_qualified_access. Accept when the
			// qualifier matches the target's enclosing namespace.
			if !isMethod && targetNs != "" && ref.Binding.Reason == "scope_qualified_access" &&
				ref.Span.StartByte >= 2 && len(src) > 0 &&
				src[ref.Span.StartByte-1] == ':' && src[ref.Span.StartByte-2] == ':' {
				qualifier := cppBaseIdentBefore(src, ref.Span.StartByte, false)
				if qualifier != targetNs {
					continue
				}
			}
			out[cand] = append(out[cand], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
			})
		}
	}
	return out, true
}

// cppNamespaceOf returns the immediate enclosing namespace name for a
// decl, or "" if the decl is at file scope or inside a non-namespace
// scope (class, function). For `namespace utils { int compute(); }`
// the function's enclosing scope is `utils`, so this returns "utils".
// Multiple levels of nesting collapse to the innermost name —
// callers only need it to match the qualifier in `qualifier::name`
// scope-qualified refs at call sites.
func cppNamespaceOf(r *scope.Result, target *scope.Decl) string {
	if r == nil || target == nil || target.Scope == 0 {
		return ""
	}
	sid := int(target.Scope) - 1
	if sid < 0 || sid >= len(r.Scopes) {
		return ""
	}
	s := &r.Scopes[sid]
	if s.Kind != scope.ScopeNamespace {
		return ""
	}
	// Find the namespace decl whose FullSpan contains this scope's
	// span — that decl is the namespace this scope belongs to.
	var best *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Kind != scope.KindNamespace {
			continue
		}
		if d.FullSpan.StartByte <= s.Span.StartByte && s.Span.StartByte <= d.FullSpan.EndByte {
			if best == nil || d.FullSpan.StartByte > best.FullSpan.StartByte {
				best = d
			}
		}
	}
	if best == nil {
		return ""
	}
	return best.Name
}

// cppBaseIdentBefore returns the identifier immediately before a
// property-access operator (`.`, `->`, or `::`). arrowMode=true
// means the separator was `->` (2 bytes); the caller passes false
// for `.` and `::`.
func cppBaseIdentBefore(src []byte, refStart uint32, arrowMode bool) string {
	if int(refStart) <= 0 || int(refStart) > len(src) {
		return ""
	}
	end := int(refStart) - 1 // position of `.`, `>`, or `:`
	if arrowMode {
		end--
	} else if src[end] == ':' && end > 0 && src[end-1] == ':' {
		end--
	}
	i := end - 1
	for i >= 0 {
		c := src[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			i--
			continue
		}
		break
	}
	return string(src[i+1 : end])
}

func cpp_ext_matches(file string, exts []string) bool {
	e := strings.ToLower(file)
	for _, ext := range exts {
		if strings.HasSuffix(e, ext) {
			return true
		}
	}
	return false
}


// cppDeriveReceiver scans within [symStart, symEnd] for the
// pattern `<Type>::<symName>` and returns Type. The C++ symbol-index
// parser doesn't extract the `Type::` qualifier on out-of-line
// method defs (`int Counter::value() const {…}`), so sym.Receiver
// can be empty. This peek recovers it.
func cppDeriveReceiver(src []byte, symStart, symEnd uint32, symName string) string {
	if len(src) == 0 {
		return ""
	}
	end := int(symEnd)
	if end > len(src) {
		end = len(src)
	}
	start := int(symStart)
	if start < 0 || start >= end {
		return ""
	}
	// Find the symName identifier in src[start:end] preceded by `::`.
	nameBytes := []byte(symName)
	for i := start; i+len(nameBytes) <= end; i++ {
		// Match nameBytes at position i.
		if string(src[i:i+len(nameBytes)]) != symName {
			continue
		}
		// Must be an identifier boundary on either side.
		if i+len(nameBytes) < len(src) {
			c := src[i+len(nameBytes)]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '_' {
				continue
			}
		}
		if i < 2 {
			continue
		}
		if src[i-1] != ':' || src[i-2] != ':' {
			continue
		}
		// Walk back from i-2 for the Type identifier.
		j := i - 3
		for j >= 0 {
			c := src[j]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '_' {
				j--
				continue
			}
			break
		}
		if j+1 < i-2 {
			return string(src[j+1 : i-2])
		}
	}
	return ""
}


// cppPairRefsIntoVarTypes scans consecutive refs in r looking for
// the `<TypeIdent> <varIdent>[;=({,]` pattern that indicates a
// variable declaration with a user-defined type. The C++ scope
// builder emits both idents as refs (not as decl + type ref), so
// buildVarTypes misses these. We pair them up directly from the
// source to populate varTypes[varIdent] = TypeIdent.
func cppPairRefsIntoVarTypes(r *scope.Result, src []byte, varTypes map[string]string) {
	if r == nil || len(src) == 0 || varTypes == nil {
		return
	}
	for i := 0; i+1 < len(r.Refs); i++ {
		typeRef := r.Refs[i]
		nameRef := r.Refs[i+1]
		if typeRef.Span.EndByte >= nameRef.Span.StartByte {
			continue
		}
		// Gap between refs must be whitespace + optional `*` or `&`
		// (pointer/reference decls like `Counter* p` or `Counter &r`).
		// Balanced `<...>` is also tolerated so smart-pointer decls
		// like `std::unique_ptr<IGreeter> g` still pair, and we
		// remember the innermost ident inside the angle brackets
		// (`IGreeter`) — that's the more useful "effective type"
		// for inheritance checks than the outer wrapper.
		gap := src[typeRef.Span.EndByte:nameRef.Span.StartByte]
		allowed := true
		innerType := ""
		for i := 0; i < len(gap); {
			c := gap[i]
			switch {
			case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '*' || c == '&':
				i++
			case c == '<':
				depth := 1
				i++
				identStart := -1
				identEnd := -1
				for i < len(gap) && depth > 0 {
					cc := gap[i]
					switch {
					case cc == '<':
						depth++
					case cc == '>':
						depth--
					case (cc >= 'a' && cc <= 'z') || (cc >= 'A' && cc <= 'Z') ||
						(cc >= '0' && cc <= '9') || cc == '_':
						if identStart < 0 {
							identStart = i
						}
						identEnd = i + 1
					default:
						identStart = -1
						identEnd = -1
					}
					i++
				}
				if identStart >= 0 && identEnd > identStart {
					innerType = string(gap[identStart:identEnd])
				}
			default:
				allowed = false
				i = len(gap)
			}
		}
		if !allowed {
			continue
		}
		// The byte AFTER nameRef should be ;, =, (, ,, {, or whitespace
		// (for multi-decl statements like `Counter a, b;`).
		end := int(nameRef.Span.EndByte)
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
		if end >= len(src) {
			continue
		}
		next := src[end]
		if next != ';' && next != '=' && next != '(' && next != ',' && next != '{' && next != ')' {
			continue
		}
		// Skip if the byte BEFORE typeRef is an identifier continuation
		// (means typeRef is itself mid-expression, not a type-position).
		if typeRef.Span.StartByte > 0 {
			prev := src[typeRef.Span.StartByte-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') ||
				(prev >= '0' && prev <= '9') || prev == '_' || prev == '.' {
				continue
			}
		}
		// Don't overwrite an existing entry.
		if _, exists := varTypes[nameRef.Name]; !exists {
			ty := typeRef.Name
			if innerType != "" {
				ty = innerType
			}
			varTypes[nameRef.Name] = ty
		}
	}
}

// cppShadow records a `Type name` local-decl pattern detected inside
// a function body. The C++ scope builder doesn't emit local var
// decls, so cross-file rename can't shadow-skip refs that bind to a
// same-name local. This struct lets the dispatch handler recover the
// shadow declaratively: if a ref's name matches and the ref falls
// inside funcStart..funcEnd, treat it as a shadow.
type cppShadow struct {
	funcStart, funcEnd uint32
	name               string
}

// cppCollectLocalShadows walks each function/method's FullSpan and
// records `Type name` patterns inside the body — these are local
// variable declarations the C++ builder doesn't emit. Returned shadows
// gate same-name refs in the dispatch handler so they aren't rewritten
// by a cross-file rename targeting an unrelated global of the same
// name.
//
// Only emits shadows whose `Type` ref appears INSIDE a function body.
// Pairs follow the same gap rules as cppPairRefsIntoVarTypes
// (whitespace + `*`, `&`, balanced `<...>`).
func cppCollectLocalShadows(r *scope.Result, src []byte) []cppShadow {
	if r == nil || len(src) == 0 {
		return nil
	}
	type fnSpan struct {
		start, end uint32
	}
	var fns []fnSpan
	for _, d := range r.Decls {
		if d.Kind != scope.KindFunction && d.Kind != scope.KindMethod {
			continue
		}
		if d.FullSpan.EndByte == 0 {
			continue
		}
		fns = append(fns, fnSpan{d.FullSpan.StartByte, d.FullSpan.EndByte})
	}
	if len(fns) == 0 {
		return nil
	}
	var out []cppShadow
	for i := 0; i+1 < len(r.Refs); i++ {
		typeRef := r.Refs[i]
		nameRef := r.Refs[i+1]
		if typeRef.Span.EndByte >= nameRef.Span.StartByte {
			continue
		}
		// Both refs must lie inside the same function body. Find the
		// smallest enclosing function span.
		var fn *fnSpan
		for k := range fns {
			f := &fns[k]
			if typeRef.Span.StartByte >= f.start && nameRef.Span.EndByte <= f.end {
				if fn == nil || (f.end-f.start) < (fn.end-fn.start) {
					fn = f
				}
			}
		}
		if fn == nil {
			continue
		}
		gap := src[typeRef.Span.EndByte:nameRef.Span.StartByte]
		ok := true
		for j := 0; j < len(gap); j++ {
			c := gap[j]
			switch {
			case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '*' || c == '&' || c == '>':
			case c == '<':
				depth := 1
				j++
				for j < len(gap) && depth > 0 {
					switch gap[j] {
					case '<':
						depth++
					case '>':
						depth--
					}
					j++
				}
				j--
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		// Trailing char after nameRef must be `;`, `=`, or `,` for a
		// real local var decl (rules out function calls, blocks).
		end := int(nameRef.Span.EndByte)
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
			end++
		}
		if end >= len(src) {
			continue
		}
		next := src[end]
		if next != ';' && next != '=' && next != ',' {
			continue
		}
		out = append(out, cppShadow{funcStart: fn.start, funcEnd: fn.end, name: nameRef.Name})
	}
	return out
}

// cppEmitHierarchySpans finds same-named methods in classes/
// structs related to derivedReceiver via the inheritance graph
// in either sym.File or its sibling .hpp/.cpp, and emits isDef
// spans into out for each matching decl.
//
// C++ class declarations live in the header; out-of-line method
// definitions live in the source (.cpp). The dispatch already
// includes sym.File and sibling files in the candidate set, so
// the per-candidate ref/decl walks pick up the .cpp side. This
// helper handles the .hpp side: walk every class body in the
// header, find method decls in related classes' bodies.
func cppEmitHierarchySpans(out map[string][]span, resolver *namespace.CppResolver, sym *index.SymbolInfo, target *scope.Decl, receiver string) {
	files := []string{sym.File}
	files = append(files, resolver.SamePackageFiles(sym.File)...)
	for _, file := range files {
		src := resolver.Source(file)
		if len(src) == 0 {
			continue
		}
		hier := namespace.CppFindClassHierarchy(src)
		if len(hier) == 0 {
			continue
		}
		related := map[string]bool{}
		for _, t := range namespace.CppRelatedTypes(src, receiver) {
			related[t] = true
		}
		if len(related) == 0 {
			continue
		}
		res := resolver.Result(file)
		if res == nil {
			continue
		}
		for _, h := range hier {
			if !related[h.Name] || h.Name == receiver {
				continue
			}
			for i := range res.Decls {
				d := &res.Decls[i]
				if d.Name != sym.Name {
					continue
				}
				if d.Kind != scope.KindMethod && d.Kind != scope.KindFunction {
					continue
				}
				if d.Span.StartByte < h.BodyStart || d.Span.EndByte > h.BodyEnd {
					continue
				}
				if d.ID == target.ID {
					continue
				}
				out[file] = append(out[file], span{
					start: d.Span.StartByte,
					end:   d.Span.EndByte,
					})
			}
		}
	}
}
