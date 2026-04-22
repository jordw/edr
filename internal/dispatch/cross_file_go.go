package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// goCrossFileSpans replaces the per-language Go branch of
// scopeAwareCrossFileSpans with a uniform namespace-based approach:
// for each candidate file, build its effective namespace, then accept
// refs whose name matches the target AND whose namespace entry for
// that name carries the targets canonical DeclID.
//
// This collapses the propOK widening, goRefPrefixMatches package-
// prefix check, and goSamePackageRefs supplement into one loop,
// because the namespace knows which decls are visible in each file
// and disambiguates `output.Rel` from `filepath.Rel` by DeclID.
//
// Returns (spans, ok). ok=false only when the targets canonical
// DeclID could not be computed (no go.mod, parse failure) — caller
// then falls back to the regex path. ok=true with an empty map is a
// legitimate "no cross-file callers" answer.
func goCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	out := map[string][]span{}
	resolver := namespace.NewGoResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)

	// Same-package walker runs unconditionally as a shadow-safe backup.
	// It only emits BindUnresolved refs in same-package siblings, so
	// shadowed locals (BindResolved-to-local) are skipped by construction.
	// Even when the namespace path also runs and produces overlapping
	// spans for the same call sites, the rename engine's overlap-skip
	// dedups them.
	for _, idRef := range goSamePackageRefs(sym.File, goPackageOfFile(sym.File), sym.Name) {
		out[idRef.file] = append(out[idRef.file], span{
			start: idRef.startByte,
			end:   idRef.endByte,
			isDef: false,
		})
	}

	// Namespace path: requires a canonical path (go.mod found) and a
	// successful parse of the target file. When unavailable we still
	// return the same-package spans above — partial coverage but
	// shadow-safe.
	if canonical == "" || targetRes == nil {
		return out, true
	}

	// Find the target Decl by matching name + span containment. The
	// scope builder's Decl.Span is the identifier; sym.StartByte/
	// EndByte covers the full symbol (including signature/body). We
	// match the decl whose identifier span falls inside the symbol.
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

	// Candidate files: cross-package callers via the import graph,
	// plus same-package siblings (covered by walker above too — the
	// rename engine dedupes overlapping spans).
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

	targetPkgPath := canonical
	pop := namespace.GoPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		if !ns.Matches(sym.Name, target.ID) {
			continue
		}
		// Per-file: shadow guard map + import-gateway map. Gateways
		// translate `pkg.Name` property accesses into the import path
		// `pkg` resolves to, so we can confirm the ref points to our
		// target's package — not some unrelated `other.Name` call.
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		gateways := goImportGateways(candRes)
		// Source bytes for base-identifier extraction. The resolver
		// caches them — same allocation as the Result parse used.
		src := resolver.Source(cand)

		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			// Shadow guard: ref bound to a NESTED-scope same-name
			// decl in this file → it's a local, not our target.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					var localScopeKind scope.ScopeKind
					if sid := int(local.Scope) - 1; sid >= 0 && sid < len(candRes.Scopes) {
						localScopeKind = candRes.Scopes[sid].Kind
					}
					if local.Kind != scope.KindImport && localScopeKind != scope.ScopeFile {
						continue
					}
				}
			}
			// Property-access disambiguation: `X.Name` is only our
			// target if X resolves to an import for our target's
			// package. Bare refs (BindUnresolved) skip this — they're
			// either same-package siblings (handled by walker) or
			// builtins (filtered out elsewhere).
			if ref.Binding.Reason == "property_access" && len(src) > 0 {
				baseIdent := goBaseIdentBefore(src, ref.Span.StartByte)
				if baseIdent == "" {
					continue
				}
				gatewayPath, ok := gateways[baseIdent]
				if !ok || gatewayPath != targetPkgPath {
					continue
				}
			}
			out[cand] = append(out[cand], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}

	return out, true
}

// goImportGateways maps each KindImport decl's local name to the
// import path it brings in. Used by goCrossFileSpans to decide
// whether `X.Name` refers to our target's package.
func goImportGateways(r *scope.Result) map[string]string {
	out := make(map[string]string)
	if r == nil {
		return out
	}
	for _, d := range r.Decls {
		if d.Kind != scope.KindImport {
			continue
		}
		path := d.Signature
		// The Go scope builder packs the import path as
		// "<importPath>\x00*" — strip the trailing marker.
		if i := strings.IndexByte(path, 0); i >= 0 {
			path = path[:i]
		}
		if path == "" {
			continue
		}
		out[d.Name] = path
	}
	return out
}

// goBaseIdentBefore returns the identifier immediately before a `.`
// at refStart, or "" if the preceding char is not a dot or no
// identifier precedes it. Pure byte-level scan — we cannot rely on
// the scope builder to expose the base of property_access refs.
func goBaseIdentBefore(src []byte, refStart uint32) string {
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

