package dispatch

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// pythonCrossFileSpans is the Python branch of
// scopeAwareCrossFileSpans. It matches imported decls by canonical
// DeclID: for each candidate file's effective namespace, refs named
// sym.Name whose namespace entry carries target.ID are precise
// cross-file occurrences, plus the identifier inside each
// `from X import Y` decl that resolves to the target.
//
// v1 scope:
//   - Module-level defs (def / class / var).
//   - `from X import Y` style imports (absolute and relative).
//   - Same-package siblings visible via `from .sibling import Y`.
//
// Deferred:
//   - `import X` bare module imports — the binding is the module,
//     not an inner decl, so rename propagation stops at the module
//     level.
//   - __all__ respect for `from X import *`.
//   - Method-call refs (obj.method) — no receiver inference.
func pythonCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	ext := strings.ToLower(filepath.Ext(sym.File))
	if ext != ".py" && ext != ".pyi" {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewPythonResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)
	if canonical == "" || targetRes == nil {
		return nil, false
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
	if target == nil {
		return out, true
	}

	// Hierarchy propagation: emit rewrite spans for same-named
	// methods in classes related to sym.Receiver via Python's
	// `class Foo(Bar, Mixin):` inheritance graph (same-file
	// only for v1).
	if sym.Receiver != "" {
		pyEmitHierarchySpans(out, resolver, sym, target)
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

	isMethod := sym.Receiver != ""
	acceptableTypes := map[string]bool{}
	if sym.Receiver != "" {
		acceptableTypes[sym.Receiver] = true
		for _, t := range namespace.PyRelatedTypes(resolver.Source(sym.File), sym.Receiver) {
			acceptableTypes[t] = true
		}
	}

	pop := namespace.PythonPopulator(resolver)
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
		}

		// Rewrite matching `from X import Y` idents.
		for _, d := range candRes.Decls {
			if d.Kind != scope.KindImport || d.Name != sym.Name {
				continue
			}
			modPath, origName := pyImportPartsFromSignatureDispatch(d.Signature)
			if origName != sym.Name {
				continue
			}
			files := resolver.FilesForImport(modPath, cand)
			hit := false
			for _, f := range files {
				if f == sym.File {
					hit = true
					break
				}
			}
			if !hit {
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
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					if local.ID != target.ID {
						var localScopeKind scope.ScopeKind
						if sid := int(local.Scope) - 1; sid >= 0 && sid < len(candRes.Scopes) {
							localScopeKind = candRes.Scopes[sid].Kind
						}
						if localScopeKind != scope.ScopeFile && local.Kind != scope.KindImport {
							continue
						}
					}
				}
			}
			// Property-access handling. For method renames we accept
			// `obj.method` when obj's declared type is in
			// acceptableTypes OR the base ident IS an acceptable
			// type itself. Span stays identifier-only.
			if ref.Binding.Reason == "property_access" && ref.Span.StartByte > 0 && len(src) > 0 && src[ref.Span.StartByte-1] == '.' {
				if !isMethod {
					continue
				}
				baseIdent := pyBaseIdentBefore(src, ref.Span.StartByte)
				if baseIdent == "" {
					continue
				}
				if !acceptableTypes[varTypes[baseIdent]] && !acceptableTypes[baseIdent] {
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

// pyImportPartsFromSignatureDispatch parses the Python builder's
// KindImport signature ("module\x00origName") at the dispatch layer.
func pyImportPartsFromSignatureDispatch(sig string) (string, string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}


// pyBaseIdentBefore returns the identifier immediately before `.` at
// refStart, or "" if the preceding char isn't `.` or no identifier
// precedes.
func pyBaseIdentBefore(src []byte, refStart uint32) string {
	if int(refStart) <= 0 || int(refStart) > len(src) {
		return ""
	}
	i := int(refStart) - 1
	if src[i] != '.' {
		return ""
	}
	end := i
	i--
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


// pyEmitHierarchySpans finds same-named methods in classes related
// to sym.Receiver via the inheritance graph in sym.File and emits
// isDef spans into out[sym.File].
func pyEmitHierarchySpans(out map[string][]span, resolver *namespace.PythonResolver, sym *index.SymbolInfo, target *scope.Decl) {
	src := resolver.Source(sym.File)
	if len(src) == 0 {
		return
	}
	hier := namespace.PyFindClassHierarchy(src)
	if len(hier) == 0 {
		return
	}
	// Find target's enclosing class by body-span containment.
	var enclosing *namespace.PyClassHierarchy
	for i := range hier {
		h := &hier[i]
		if h.BodyStart <= target.Span.StartByte && target.Span.EndByte <= h.BodyEnd {
			enclosing = h
			break
		}
	}
	if enclosing == nil {
		return
	}
	related := map[string]bool{}
	for _, t := range namespace.PyRelatedTypes(src, enclosing.Name) {
		related[t] = true
	}
	if len(related) == 0 {
		return
	}
	targetRes := resolver.Result(sym.File)
	if targetRes == nil {
		return
	}
	for _, h := range hier {
		if !related[h.Name] || h.Name == enclosing.Name {
			continue
		}
		for i := range targetRes.Decls {
			d := &targetRes.Decls[i]
			if d.Name != sym.Name {
				continue
			}
			// Python builder emits class methods as KindFunction
			// (no separate method kind for nested defs).
			if d.Kind != scope.KindMethod && d.Kind != scope.KindFunction {
				continue
			}
			if d.Span.StartByte < h.BodyStart || d.Span.EndByte > h.BodyEnd {
				continue
			}
			if d.ID == target.ID {
				continue
			}
			out[sym.File] = append(out[sym.File], span{
				start: d.Span.StartByte,
				end:   d.Span.EndByte,
			})
		}
	}
}
