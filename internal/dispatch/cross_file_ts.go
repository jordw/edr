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

	// Hierarchy propagation: when renaming a method, emit rewrite
	// spans for same-named methods in classes/interfaces that the
	// target's enclosing class extends or implements (and vice
	// versa — subclasses overriding the target). Keeps the type-
	// check consistent across the inheritance graph.
	if sym.Receiver != "" {
		tsEmitHierarchySpans(out, resolver, sym, target)
	}

	candidates := map[string]bool{}
	if refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File); err == nil {
		for _, r := range refs {
			if r.File != sym.File {
				candidates[r.File] = true
			}
		}
	}
	// For CJS modules we also rewrite `module.exports = { name }`
	// property-shorthand references in sym.File itself, because
	// the TS scope builder parses the inside of `{ name }` as a
	// namespace decl rather than a ref — the same-file pass
	// doesn't catch it. Scan sym.File directly.
	{
		targetSrc := resolver.Source(sym.File)
		for _, sp := range namespace.TSModuleExportsShorthand(targetSrc, sym.Name) {
			out[sym.File] = append(out[sym.File], span{
				start: sp.OrigNameStart,
				end:   sp.OrigNameEnd,
			})
		}
	}
	// Barrel expansion: for each primary candidate, walk its
	// KindImport decls. Any import whose module resolves to a
	// barrel that re-exports sym.Name from sym.File also needs
	// rewriting — the barrel file itself isn't in the index's
	// ref set because `export { X } from '…'` doesn't show up as
	// a ref to X in the symbol index's view. Add those barrel
	// files to the candidate set.
	{
		frontier := make([]string, 0, len(candidates))
		for c := range candidates {
			frontier = append(frontier, c)
		}
		visited := map[string]bool{}
		for len(frontier) > 0 {
			next := frontier[0]
			frontier = frontier[1:]
			if visited[next] {
				continue
			}
			visited[next] = true
			nextRes := resolver.Result(next)
			if nextRes == nil {
				continue
			}
			src := resolver.Source(next)
			for _, re := range findReExportsWithSpans(src) {
				for _, f := range resolver.FilesForImport(re.ModPath, next) {
					if f == sym.File || resolveTSBarrelSourceFile(resolver, f, re.OrigName) == sym.File {
						if !candidates[f] && f != sym.File {
							candidates[f] = true
						}
						// Also the barrel file itself.
						if !candidates[next] && next != sym.File {
							candidates[next] = true
						}
					}
					if !visited[f] && f != sym.File {
						frontier = append(frontier, f)
					}
				}
			}
			for _, d := range nextRes.Decls {
				if d.Kind != scope.KindImport {
					continue
				}
				modPath, origName := tsImportPartsFromSignature(d.Signature)
				if modPath == "" || origName == "" {
					continue
				}
				for _, f := range resolver.FilesForImport(modPath, next) {
					if f == sym.File || resolveTSBarrelSourceFile(resolver, f, origName) == sym.File {
						if !candidates[f] && f != sym.File {
							candidates[f] = true
						}
					}
					if !visited[f] && f != sym.File {
						frontier = append(frontier, f)
					}
				}
			}
		}
	}

	isMethod := sym.Receiver != ""
	acceptableTypes := map[string]bool{}
	if sym.Receiver != "" {
		acceptableTypes[sym.Receiver] = true
		// Hierarchy-aware: also accept call sites whose receiver
		// is typed as a supertype/subtype of sym.Receiver so
		// e.g. `const g: IGreeter = new Hi(); g.greet()` rewrites
		// when renaming Hi.greet.
		for _, t := range tsRelatedTypes(resolver.Source(sym.File), sym.Receiver) {
			acceptableTypes[t] = true
		}
	}

	pop := namespace.TSPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		_ = ns
		// Candidate set was already narrowed (FindSemanticReferences +
		// barrel expansion), so we admit everything here and rely
		// on the per-decl / per-ref / per-re-export filtering below
		// to decide what gets a span.
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		src := resolver.Source(cand)
		var varTypes map[string]string
		if isMethod {
			varTypes = buildVarTypes(candRes, src)
		}

		// CJS: `const { X } = require("./mod")` destructuring.
		// Emit a span for the X position so the local binding gets
		// renamed alongside refs to it.
		for _, b := range namespace.TSFindCJSBindings(src) {
			if b.OrigName != sym.Name {
				continue
			}
			for _, f := range resolver.FilesForImport(b.ModPath, cand) {
				if f == sym.File || resolveTSBarrelSourceFile(resolver, f, b.OrigName) == sym.File {
					out[cand] = append(out[cand], span{
						start: b.OrigNameStart,
						end:   b.OrigNameEnd,
					})
					break
				}
			}
		}

		// Scan re-export clauses for barrel files:
		//   export { X } from "./y"
		//   export { X as Y } from "./y"
		// When './y' (chased through further barrels) ends at
		// sym.File and X == sym.Name, rewrite the X position.
		for _, re := range findReExportsWithSpans(src) {
			if re.OrigName != sym.Name {
				continue
			}
			for _, f := range resolver.FilesForImport(re.ModPath, cand) {
				if f == sym.File || resolveTSBarrelSourceFile(resolver, f, re.OrigName) == sym.File {
					out[cand] = append(out[cand], span{
						start: re.OrigNameStart,
						end:   re.OrigNameEnd,
					})
					break
				}
			}
		}

		// Rewrite import decls whose signature resolves to our
		// target file + item. `import { foo } from './lib'` →
		// KindImport decl Name=foo Signature="./lib\0foo". For
		// aliased imports `import { orig as local }`, the local
		// name is d.Name and the original is the second half of
		// the signature; when origName != d.Name, we must also
		// rewrite the origName position in the source.
		for _, d := range candRes.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			modPath, origName := tsImportPartsFromSignature(d.Signature)
			if modPath == "" {
				continue
			}
			// Allow origName=="default" when sym is the default
			// export of the module it imports from. The default
			// import conveys no specific name at the call site —
			// the consumer picks the local name — but if they
			// happened to pick the same name as the function,
			// rename propagates.
			if origName == "default" && origName != sym.Name {
				files := resolver.FilesForImport(modPath, cand)
				for _, f := range files {
					if f != sym.File {
						continue
					}
					defName := namespace.TSFileDefaultExportName(resolver.Source(f))
					if defName == sym.Name {
						origName = sym.Name
					}
					break
				}
			}
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
				// Chase barrel re-exports to find the TRUE source
				// file for origName starting from f. If it ends at
				// sym.File, this import pulls our target through
				// the barrel.
				if resolveTSBarrelSourceFile(resolver, f, origName) == sym.File {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
			if d.Name == sym.Name {
				// Non-aliased: local name IS the target.
				out[cand] = append(out[cand], span{
					start: d.Span.StartByte,
					end:   d.Span.EndByte,
				})
			} else {
				// Aliased: scan backward to the start of the line
				// (or at most 500 bytes) for the origName token
				// between `{` and ` as `.
				lineStart := uint32(0)
				if d.Span.StartByte > 0 {
					lo := int(d.Span.StartByte) - 1
					limit := int(d.Span.StartByte) - 500
					if limit < 0 {
						limit = 0
					}
					for lo >= limit && src[lo] != '\n' {
						lo--
					}
					if lo < 0 {
						lo = 0
					} else {
						lo++ // skip the newline itself
					}
					lineStart = uint32(lo)
				}
				if s, e, ok := findTSOrigNameSpan(src, lineStart, d.Span.StartByte, origName); ok {
					out[cand] = append(out[cand], span{
						start: s,
						end:   e,
					})
				}
			}
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
			// acceptableTypes OR base ident IS an acceptable type
			// (Class.staticMethod). Span stays identifier-only.
			if ref.Binding.Reason == "property_access" && ref.Span.StartByte > 0 && len(src) > 0 && src[ref.Span.StartByte-1] == '.' {
				if !isMethod {
					continue
				}
				baseIdent := dotBaseIdentBefore(src, ref.Span.StartByte)
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


// findTSOrigNameSpan scans src[fullStart:localStart] for origName,
// returning its byte range. Used to rewrite the original-name
// portion of aliased imports like `import { orig as local }`.
func findTSOrigNameSpan(src []byte, fullStart, localStart uint32, origName string) (uint32, uint32, bool) {
	if int(localStart) > len(src) || fullStart >= localStart {
		return 0, 0, false
	}
	region := src[fullStart:localStart]
	needle := []byte(origName)
	for i := 0; i+len(needle) <= len(region); i++ {
		// Word-boundary check.
		if i > 0 {
			prev := region[i-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') ||
				(prev >= '0' && prev <= '9') || prev == '_' || prev == '$' {
				continue
			}
		}
		if i+len(needle) < len(region) {
			next := region[i+len(needle)]
			if (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') ||
				(next >= '0' && next <= '9') || next == '_' || next == '$' {
				continue
			}
		}
		if string(region[i:i+len(needle)]) == origName {
			return fullStart + uint32(i), fullStart + uint32(i+len(needle)), true
		}
	}
	return 0, 0, false
}


// resolveTSBarrelSourceFile chases `export { name } from '…'` chains
// starting from file and returns the path of the file that actually
// declares name, or "" when the chain can't be resolved.
func resolveTSBarrelSourceFile(r *namespace.TSResolver, file, name string) string {
	hit := namespace.ResolveTSBarrelForDispatch(r, file, name)
	return hit
}

// findReExportsWithSpans wraps namespace.FindTSReExportsWithSpans
// for use from the dispatch package.
func findReExportsWithSpans(src []byte) []namespace.TSReExportSpan {
	return namespace.FindTSReExportsWithSpans(src)
}


// tsEmitHierarchySpans finds same-named methods in classes/
// interfaces reachable from sym's enclosing type via extends/
// implements, emits def-spans for each match into `out`.
//
// Scope: same-file for now. Cross-file hierarchy can be chased
// through import graphs in a later pass.
func tsEmitHierarchySpans(out map[string][]span, resolver *namespace.TSResolver, sym *index.SymbolInfo, target *scope.Decl) {
	src := resolver.Source(sym.File)
	if len(src) == 0 {
		return
	}
	hierarchy := namespace.TSFindClassHierarchy(src)
	if len(hierarchy) == 0 {
		return
	}
	// Find the target's enclosing class/interface by containment.
	var enclosing *namespace.TSClassHierarchy
	for i := range hierarchy {
		h := &hierarchy[i]
		if h.BodyStart <= target.Span.StartByte && target.Span.EndByte <= h.BodyEnd {
			enclosing = h
			break
		}
	}
	if enclosing == nil {
		return
	}
	// Build set of "related" class/interface names:
	//   1. The enclosing's supers (types it extends/implements).
	//   2. Any class that extends/implements the enclosing.
	related := map[string]bool{}
	for _, s := range enclosing.Supers {
		related[s] = true
	}
	for _, h := range hierarchy {
		for _, s := range h.Supers {
			if s == enclosing.Name {
				related[h.Name] = true
				break
			}
		}
	}
	if len(related) == 0 {
		return
	}
	// For each related class/interface, find a method decl with
	// matching name inside its body span and emit a span.
	targetRes := resolver.Result(sym.File)
	if targetRes == nil {
		return
	}
	for _, h := range hierarchy {
		if !related[h.Name] || h.Name == enclosing.Name {
			continue
		}
		for i := range targetRes.Decls {
			d := &targetRes.Decls[i]
			if d.Name != sym.Name {
				continue
			}
			if d.Kind != scope.KindMethod {
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

// tsRelatedTypes returns the transitive set of class/interface
// names reachable from className via extends/implements edges in
// src. Used to widen acceptableTypes so a rename on Foo.method
// accepts call sites where the receiver is typed as a super of
// Foo or a sub of Foo.
func tsRelatedTypes(src []byte, className string) []string {
	hierarchy := namespace.TSFindClassHierarchy(src)
	byName := map[string]namespace.TSClassHierarchy{}
	for _, h := range hierarchy {
		byName[h.Name] = h
	}
	related := map[string]bool{}
	var walkUp func(name string)
	walkUp = func(name string) {
		h, ok := byName[name]
		if !ok {
			return
		}
		for _, s := range h.Supers {
			if related[s] {
				continue
			}
			related[s] = true
			walkUp(s)
		}
	}
	walkUp(className)
	var walkDown func(name string)
	walkDown = func(name string) {
		for _, h := range hierarchy {
			for _, s := range h.Supers {
				if s == name && !related[h.Name] {
					related[h.Name] = true
					walkDown(h.Name)
					break
				}
			}
		}
	}
	walkDown(className)
	out := make([]string, 0, len(related))
	for k := range related {
		out = append(out, k)
	}
	return out
}
