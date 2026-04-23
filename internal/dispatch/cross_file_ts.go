package dispatch

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// tsCrossFileSpans is the TS/JS branch of scopeAwareCrossFileSpans.
// It matches imported decls by canonical DeclID: for each candidate
// file's effective namespace, refs named sym.Name whose namespace
// entry carries target.ID are precise cross-file occurrences, plus
// the identifier inside each `import { … }` statement that resolves
// to the target.
//
// v1 scope:
//   - Free exports at file scope (function / const / let / class /
//     interface / type / enum) with explicit relative imports.
//   - Path-qualified refs are not specialized — TS's property-access
//     is skipped because `obj.method()` call sites can't be typed
//     without receiver inference.
//
// Deferred:
//   - tsconfig paths, baseUrl aliases, node_modules packages.
//   - Barrel re-exports (`export { X } from './bar'`).
//   - Default exports with renamed local bindings.
func tsCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	if !isTSLikeFile(sym.File) {
		return nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewTSResolver(db.Root())
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

	pop := namespace.TSPopulator(resolver)
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

		// Rewrite import decls whose signature resolves to our
		// target file + item. `import { foo } from './lib'` →
		// KindImport decl Name=foo Signature="./lib\0foo".
		for _, d := range candRes.Decls {
			if d.Kind != scope.KindImport || d.Name != sym.Name {
				continue
			}
			modPath, origName := tsImportPartsFromSignature(d.Signature)
			if modPath == "" || origName != sym.Name {
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
			// Skip property-access refs. Without receiver-type
			// inference, `obj.foo` may or may not be our target.
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

// tsImportPartsFromSignature mirrors the namespace package's helper
// at the dispatch layer. Kept local to avoid cross-package exposure.
func tsImportPartsFromSignature(sig string) (string, string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

func isTSLikeFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts":
		return true
	}
	return strings.HasSuffix(file, ".d.ts")
}
