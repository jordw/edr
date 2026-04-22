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

	// For method renames we also need to rewrite call sites through
	// interface-typed variables. acceptableTypes is the set of type
	// names whose methods we treat as aliases of the target method:
	// the concrete receiver plus every same-package interface whose
	// full method set is a subset of the receiver's method set AND
	// includes sym.Name.
	isMethod := sym.Receiver != ""
	acceptableTypes := map[string]bool{}
	if sym.Receiver != "" {
		acceptableTypes[sym.Receiver] = true
	}
	var ifaceHits []goIfaceHit
	if isMethod {
		ifaceHits = goSatisfiedInterfaces(ctx, db, sym, resolver)
		for _, h := range ifaceHits {
			acceptableTypes[h.name] = true
		}
	}

	targetPkgPath := canonical
	pop := namespace.GoPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		// For non-method renames the namespace must carry our target
		// DeclID for sym.Name. For methods the namespace doesn't
		// surface method names at all (methods live inside type
		// scopes), so we admit every candidate and rely on per-ref
		// disambiguation below.
		if !isMethod && !ns.Matches(sym.Name, target.ID) {
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
		// For methods: map each local var/param/field name to the
		// last identifier of its declared type. Used to accept
		// property_access refs whose base is a var of an acceptable
		// type.
		var varTypes map[string]string
		if isMethod {
			varTypes = goBuildVarTypes(candRes, src)
		}

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
				// Accept when base resolves to an import of the
				// target package (cross-package function call), OR
				// (methods only) when base is a local of an
				// acceptable type (concrete receiver or satisfied
				// interface).
				if gatewayPath, ok := gateways[baseIdent]; ok && gatewayPath == targetPkgPath {
					// ok
				} else if isMethod && varTypes != nil && acceptableTypes[varTypes[baseIdent]] {
					// ok
				} else {
					continue
				}
			}
			// Expand the span back through a leading `.` for
			// property-access refs. The rename pipeline's call-site
			// regex is `\.method\b` when sym.Receiver is set; without
			// the leading dot in the span, the regex would not match.
			startByte := ref.Span.StartByte
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 && src[startByte-1] == '.' {
				startByte--
			}
			out[cand] = append(out[cand], span{
				start: startByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}

	// Emit spans for each satisfied interface's declaration of
	// sym.Name. isDef=true makes the rename engine use a bare-name
	// regex at this location (the span covers the method identifier
	// itself, not a property-access).
	for _, h := range ifaceHits {
		out[h.file] = append(out[h.file], span{
			start: h.methodSpan.StartByte,
			end:   h.methodSpan.EndByte,
			isDef: true,
		})
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



// goIfaceHit records a same-package interface whose method set is a
// subset of sym.Receiver's method set, along with the span of its
// declaration of sym.Name.
type goIfaceHit struct {
	name       string
	file       string
	methodSpan scope.Span
}

// goSatisfiedInterfaces finds same-package interfaces that sym.Receiver
// structurally implements. An interface qualifies only when every
// method it declares is also a method on sym.Receiver AND sym.Name is
// one of them. The Go scope builder emits `type X interface {...}`
// as a KindType decl plus a child ScopeInterface; we walk scopes and
// match each ScopeInterface back to its owning type decl by proximity.
func goSatisfiedInterfaces(
	ctx context.Context,
	db index.SymbolStore,
	sym *index.SymbolInfo,
	resolver *namespace.GoResolver,
) []goIfaceHit {
	if sym.Receiver == "" {
		return nil
	}
	files := append([]string{sym.File}, resolver.SamePackageFiles(sym.File)...)
	receiverMethods := map[string]bool{}
	for _, f := range files {
		syms, err := db.GetSymbolsByFile(ctx, f)
		if err != nil {
			continue
		}
		for _, s := range syms {
			if s.Type == "method" && s.Receiver == sym.Receiver {
				receiverMethods[s.Name] = true
			}
		}
	}
	if !receiverMethods[sym.Name] {
		return nil
	}
	var hits []goIfaceHit
	for _, f := range files {
		res := resolver.Result(f)
		if res == nil {
			continue
		}
		for si := range res.Scopes {
			sc := &res.Scopes[si]
			if sc.Kind != scope.ScopeInterface {
				continue
			}
			var owner *scope.Decl
			var bestEnd uint32
			for k := range res.Decls {
				d := &res.Decls[k]
				if d.Kind != scope.KindType {
					continue
				}
				if d.Span.EndByte > sc.Span.StartByte {
					continue
				}
				if d.Span.EndByte <= bestEnd {
					continue
				}
				bestEnd = d.Span.EndByte
				owner = d
			}
			if owner == nil {
				continue
			}
			// Collect methods whose Scope matches this interface.
			// Skip identifiers that don't look like exported method
			// names — the builder occasionally emits trailing
			// return-type idents like `error` as KindMethod inside
			// interface bodies.
			ifaceMethods := map[string]scope.Span{}
			allSubset := true
			for k := range res.Decls {
				m := &res.Decls[k]
				if m.Kind != scope.KindMethod || m.Scope != sc.ID {
					continue
				}
				if m.Name == "" || m.Name[0] < 'A' || m.Name[0] > 'Z' {
					continue
				}
				if !receiverMethods[m.Name] {
					allSubset = false
					break
				}
				ifaceMethods[m.Name] = m.Span
			}
			if !allSubset || len(ifaceMethods) == 0 {
				continue
			}
			if methodSpan, ok := ifaceMethods[sym.Name]; ok {
				hits = append(hits, goIfaceHit{
					name:       owner.Name,
					file:       f,
					methodSpan: methodSpan,
				})
			}
		}
	}
	return hits
}

// goBuildVarTypes maps each local var/param/field decl to the last
// identifier of its declared type. Handles Go's `name Type`,
// `name *Type`, and `name pkg.Type` orderings.
func goBuildVarTypes(r *scope.Result, src []byte) map[string]string {
	out := make(map[string]string)
	if r == nil || len(src) == 0 {
		return out
	}
	for _, d := range r.Decls {
		if d.Kind != scope.KindVar && d.Kind != scope.KindParam && d.Kind != scope.KindField {
			continue
		}
		if name := goFindTypeAfterDecl(r, src, d.Span.EndByte); name != "" {
			out[d.Name] = name
		}
	}
	return out
}

// goFindTypeAfterDecl returns the tail identifier of the type
// annotation that begins immediately after declEnd.
func goFindTypeAfterDecl(r *scope.Result, src []byte, declEnd uint32) string {
	var first *scope.Ref
	var firstStart uint32 = ^uint32(0)
	for i := range r.Refs {
		ref := &r.Refs[i]
		if ref.Span.StartByte < declEnd {
			continue
		}
		if ref.Span.StartByte >= firstStart {
			continue
		}
		gap := src[declEnd:ref.Span.StartByte]
		if !onlyWsOrStar(gap) {
			continue
		}
		firstStart = ref.Span.StartByte
		first = ref
	}
	if first == nil {
		return ""
	}
	cur := first
	for {
		var next *scope.Ref
		var nextStart uint32 = ^uint32(0)
		for i := range r.Refs {
			ref := &r.Refs[i]
			if ref.Span.StartByte < cur.Span.EndByte {
				continue
			}
			if ref.Span.StartByte >= nextStart {
				continue
			}
			gap := src[cur.Span.EndByte:ref.Span.StartByte]
			if len(gap) != 1 || gap[0] != '.' {
				continue
			}
			nextStart = ref.Span.StartByte
			next = ref
		}
		if next == nil {
			break
		}
		cur = next
	}
	return cur.Name
}

func onlyWsOrStar(gap []byte) bool {
	for _, c := range gap {
		switch c {
		case ' ', '\t', '\n', '\r', '*':
			continue
		}
		return false
	}
	return true
}
