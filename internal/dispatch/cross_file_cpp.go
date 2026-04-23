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
	}

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
		if !isMethod && !ns.Matches(sym.Name, target.ID) {
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
				isDef: true,
			})
		}

		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					if local.ID != target.ID {
						continue
					}
				}
			}
			startByte := ref.Span.StartByte
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 {
				prev := src[startByte-1]
				isDot := prev == '.'
				isArrow := startByte >= 2 && src[startByte-2] == '-' && prev == '>'
				isScope := startByte >= 2 && src[startByte-2] == ':' && prev == ':'
				// For method renames: accept x.foo() / x->foo() when
				// x's declared type is in acceptableTypes. Skip
				// `Type::member` scope-qualified access unless
				// Type itself is the acceptable receiver (handled
				// below).
				accept := false
				if isMethod && (isDot || isArrow) {
					baseIdent := cppBaseIdentBefore(src, startByte, isArrow)
					if baseIdent != "" && acceptableTypes[varTypes[baseIdent]] {
						accept = true
					}
				}
				if isMethod && isScope {
					baseIdent := cppBaseIdentBefore(src, startByte, false)
					if acceptableTypes[baseIdent] {
						accept = true
					}
				}
				if !accept {
					continue
				}
				// Expand the span back through the separator so the
				// call-site regex `\.name` (or equivalent) matches.
				if isDot {
					startByte--
				} else if isArrow {
					startByte -= 2
				} else if isScope {
					startByte -= 2
				}
			}
			out[cand] = append(out[cand], span{
				start: startByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}
	return out, true
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
		gap := src[typeRef.Span.EndByte:nameRef.Span.StartByte]
		allowed := true
		for _, c := range gap {
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '*' || c == '&' {
				continue
			}
			allowed = false
			break
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
			varTypes[nameRef.Name] = typeRef.Name
		}
	}
}
