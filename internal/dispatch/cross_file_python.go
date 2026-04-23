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

	pop := namespace.PythonPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		if !ns.Matches(sym.Name, target.ID) {
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
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
				isDef: false,
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
			// Skip property-access refs (`obj.name`) — no receiver
			// inference means we can't confirm the target.
			startByte := ref.Span.StartByte
			src := resolver.Source(cand)
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 && src[startByte-1] == '.' {
				continue
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

// pyImportPartsFromSignatureDispatch parses the Python builder's
// KindImport signature ("module\x00origName") at the dispatch layer.
func pyImportPartsFromSignatureDispatch(sig string) (string, string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}
