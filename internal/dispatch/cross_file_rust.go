package dispatch

import (
	"context"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// rustCrossFileSpans is the Rust branch of scopeAwareCrossFileSpans.
// It uses a canonical-path-hashed DeclID to match call sites across
// files that `use` the target: for each candidate file's namespace,
// a ref whose name matches and whose namespace entry carries the
// target DeclID is a precise cross-file occurrence.
//
// v1 scope:
//   - Free items at file scope (fn / struct / enum / trait / type /
//     const / static) with explicit `use` imports.
//   - Path-qualified calls (`mod::name(...)`, `Type::method(...)`)
//     via the namespace's presence of the item in the caller file.
//
// Deferred:
//   - Method renames: sym.Receiver is never set for Rust symbols,
//     so `obj.method()` call sites cannot be disambiguated by type.
//     A trait/impl-aware pass would need receiver-type inference
//     first. For now these fall through to the regex fallback.
//   - Glob imports and aliased imports.
//
// Returns (spans, ok). ok=false only when the target's canonical
// path can't be computed (no Cargo.toml) — caller then falls back.
func rustCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	out := map[string][]span{}
	resolver := namespace.NewRustResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)
	if canonical == "" || targetRes == nil {
		return nil, false
	}

	// Find the target Decl (the one being renamed).
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

	// Hierarchy override propagation. Rust expresses "Foo implements
	// Trait" via `impl Trait for Foo { ... }` blocks; the Rust scope
	// builder emits a synthetic KindClass decl per impl block with
	// SuperTypes=[Trait] (or empty for inherent `impl Foo`). The
	// shared EmitOverrideSpans walker needs sym.Receiver populated;
	// for Rust we derive it by finding the synthetic class decl
	// whose body span encloses the target method.
	if receiver := rustDeriveReceiver(targetRes, target); receiver != "" {
		var extraCandidates []string
		if refs, err := db.FindSemanticReferences(ctx, receiver, sym.File); err == nil {
			seen := map[string]struct{}{sym.File: {}}
			for _, r := range refs {
				if _, ok := seen[r.File]; ok {
					continue
				}
				seen[r.File] = struct{}{}
				extraCandidates = append(extraCandidates, r.File)
			}
		}
		symCopy := *sym
		symCopy.Receiver = receiver
		EmitOverrideSpans(out, rustResolverDeps{r: resolver}, &symCopy, extraCandidates...)
	}

	// Candidate files: symbol-index references plus every .rs file
	// under the crate's src/. The crate walk catches callers whose
	// import graph hasn't been indexed (common for Rust because the
	// symbol store doesn't currently track `use` edges).
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

	pop := namespace.RustPopulator(resolver)
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

		// Rewrite matching `use` import decls. A KindImport decl
		// whose Signature resolves to our target file + item is a
		// cross-file rename target — the identifier in
		// `use crate::runtime::spawn` must be updated alongside the
		// definition.
		for _, d := range candRes.Decls {
			if d.Kind != scope.KindImport || d.Name != sym.Name {
				continue
			}
			if len(d.Signature) == 0 {
				continue
			}
			// Signature is "modPath\0item" — reconstruct full path
			// and check whether it resolves to sym.File.
			var modPath string
			for i, c := range d.Signature {
				if c == 0 {
					modPath = d.Signature[:i]
					break
				}
			}
			if modPath == "" {
				continue
			}
			targetFiles := resolver.FilesForImport(modPath+"::"+sym.Name, cand)
			hit := false
			for _, tf := range targetFiles {
				if tf == sym.File {
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
			// Shadow guard: ref bound to a nested-scope same-name
			// decl in this file is a local, not our target.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					var localScopeKind scope.ScopeKind
					if sid := int(local.Scope) - 1; sid >= 0 && sid < len(candRes.Scopes) {
						localScopeKind = candRes.Scopes[sid].Kind
					}
					// Import decls ARE our target when they name the
					// same item; keep those. File-scope decls shadow
					// the import — treat as not-our-target.
					if local.Kind != scope.KindImport && localScopeKind != scope.ScopeFile {
						continue
					}
					if local.Kind != scope.KindImport && localScopeKind == scope.ScopeFile {
						// Re-declaration of the same name at file
						// scope — a different entity. Skip.
						continue
					}
				}
			}
			// Skip method-call refs (`obj.name`): Rust gives us no
			// receiver type, so we cannot tell whether `obj`'s type
			// is the target. Path-qualified calls (`Foo::name`) and
			// bare calls reach the regex unchanged.
			startByte := ref.Span.StartByte
			src := resolver.Source(cand)
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 && src[startByte-1] == '.' {
				continue
			}
			out[cand] = append(out[cand], span{
				start: startByte,
				end:   ref.Span.EndByte,
			})
		}
	}

	return out, true
}

// rustResolverDeps adapts RustResolver to the dispatch HierarchyDeps
// interface used by EmitOverrideSpans.
type rustResolverDeps struct {
	r *namespace.RustResolver
}

func (d rustResolverDeps) Result(file string) *scope.Result { return d.r.Result(file) }
func (d rustResolverDeps) SamePackageFiles(file string) []string {
	return d.r.SamePackageFiles(file)
}
func (d rustResolverDeps) FilesForImport(spec, importingFile string) []string {
	return d.r.FilesForImport(spec, importingFile)
}

// ImportSpec returns the bare module path from a `use` decl. The
// imported name is part of the path's last segment, but Rust's
// FilesForImport only consults the path.
func (d rustResolverDeps) ImportSpec(decl *scope.Decl) string {
	module, _ := SplitImportSignature(decl)
	return module
}

// rustDeriveReceiver finds the enclosing impl-block target type for
// the rename target. The Rust scope builder emits a synthetic
// KindClass decl per impl block whose FullSpan covers the impl
// body; the target method lives inside that span. Returns the
// synthetic decl's name (e.g. "Foo" for a method inside `impl Trait
// for Foo`), or "" if the target isn't inside any impl block (e.g.
// a free function or a trait-method declaration on a trait decl).
func rustDeriveReceiver(res *scope.Result, target *scope.Decl) string {
	if res == nil || target == nil {
		return ""
	}
	// Pick the tightest enclosing class-like decl by FullSpan
	// containment. The synthetic impl-block decls are KindClass at
	// file scope; tighter (smaller) FullSpan wins so nested impls
	// don't bleed into outer ones.
	bestName := ""
	var bestSpan uint32
	for i := range res.Decls {
		d := &res.Decls[i]
		if d.Kind != scope.KindClass {
			continue
		}
		if d.Scope != 1 {
			continue
		}
		// Containment check.
		if d.FullSpan.StartByte > target.Span.StartByte ||
			d.FullSpan.EndByte < target.Span.EndByte {
			continue
		}
		// Skip the synthetic decl that IS the target (in case the
		// user is renaming a class-name itself).
		if d.ID == target.ID {
			continue
		}
		spanWidth := d.FullSpan.EndByte - d.FullSpan.StartByte
		if bestName == "" || spanWidth < bestSpan {
			bestName = d.Name
			bestSpan = spanWidth
		}
	}
	if bestName != "" {
		return bestName
	}
	// Trait method declaration: target's enclosing scope is a
	// ScopeInterface owned by the trait decl. Find a trait decl
	// whose FullSpan contains the target.
	for i := range res.Decls {
		d := &res.Decls[i]
		if d.Kind != scope.KindInterface {
			continue
		}
		if d.Scope != 1 {
			continue
		}
		if d.FullSpan.StartByte > target.Span.StartByte ||
			d.FullSpan.EndByte < target.Span.EndByte {
			continue
		}
		return d.Name
	}
	return ""
}
