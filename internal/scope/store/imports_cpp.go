package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsCpp is Phase 1 of the cross-file import graph for
// C++. After per-file parsing + cross-file decl merging, it rewrites
// Ref.Binding so that refs which bind to a local KindImport decl (a
// `#include "foo.hpp"` or a `using` decl) or remain unresolved point
// at the actual exported Decl in the included or aliased source file.
//
// Three forms are handled (v1):
//
//   - `#include "foo.hpp"` — any ref in the includer whose name matches
//     an Exported decl in foo.hpp is rewritten to that decl. Transitive
//     includes are NOT followed; each includer lists its own headers.
//
//   - `using Foo::Bar;` — the builder emits a KindImport decl named
//     Bar with Signature=`Foo\x00Bar`. If a repo-local namespace Foo
//     exports a decl named Bar, refs in the using'er file whose local
//     binding is that import decl are rewritten to the exported decl.
//
//   - `using namespace Foo;` — the builder emits a KindImport decl
//     with Signature=`Foo\x00*`. For each unresolved ref N in the
//     same file, if namespace Foo exports a decl named N, the ref is
//     rewritten to that decl.
//
// Out of scope (v1):
//   - System includes (`#include <vector>`) — no repo-local source.
//   - C++20 `import foo;` modules — rare; the builder documents a punt.
//   - `extern "C"` blocks — transparent to linkage tracking.
//   - ADL (argument-dependent lookup) — per architectural-ceiling memo.
//   - Transitive includes — A->B->C propagation; v1 binds only against
//     files directly listed in the includer.
func resolveImportsCpp(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Fast lookup: relPath -> *scope.Result for cpp-family files.
	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isCppLike(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// Per-file exports index: name -> DeclID, built lazily.
	type exportsIdx map[string]scope.DeclID
	exportsByRel := make(map[string]exportsIdx, len(byRel))
	getExports := func(rel string) exportsIdx {
		if idx, ok := exportsByRel[rel]; ok {
			return idx
		}
		r, ok := byRel[rel]
		if !ok {
			exportsByRel[rel] = nil
			return nil
		}
		idx := make(exportsIdx, len(r.Decls))
		for i := range r.Decls {
			d := &r.Decls[i]
			if !d.Exported {
				continue
			}
			if d.Kind == scope.KindImport {
				continue
			}
			// First definition wins (top-level over shadowed).
			if _, exists := idx[d.Name]; !exists {
				idx[d.Name] = d.ID
			}
		}
		exportsByRel[rel] = idx
		return idx
	}

	// Namespace-qualified exports across all cpp files:
	// "NsA::NsB::Name" -> DeclID. Used by using-decl and using-
	// namespace resolution.
	nsMembers := buildCppNamespaceIndex(byRel)

	// Per-decl-ID rewrite targets (the `using Foo::Bar;` specific case).
	targets := make(map[scope.DeclID]scope.DeclID)

	// Widening directives applied per includer file: each entry is
	// either an included-header ref or a using-namespace FQN.
	type wideningEntry struct {
		includeRel string // "" when this is a namespace widening.
		nsFQN      string // "" when this is an include widening.
	}
	wideningByFile := make(map[string][]wideningEntry)

	for _, p := range parsed {
		if !isCppLike(p.rel) {
			continue
		}
		for i := range p.result.Decls {
			d := &p.result.Decls[i]
			if d.Kind != scope.KindImport {
				continue
			}
			if d.Signature == "" {
				continue
			}
			left, right := parseImportSignature(d.Signature)
			if left == "" {
				continue
			}
			switch right {
			case "\"", "<>":
				// `#include "left"` or `#include <left>`. Both forms are
				// tried against the repo tree; system headers naturally
				// fail the lookup and get filtered. Large CMake-driven
				// projects route project-internal includes through `-I`
				// paths + angle-brackets, so limiting to quoted would
				// miss most cross-file linkage.
				if sourceRel := resolveCppInclude(p.rel, left, byRel); sourceRel != "" {
					wideningByFile[p.rel] = append(wideningByFile[p.rel], wideningEntry{
						includeRel: sourceRel,
					})
				}
			case "*":
				// `using namespace Foo[::Inner]*;` — widening directive.
				wideningByFile[p.rel] = append(wideningByFile[p.rel], wideningEntry{
					nsFQN: left,
				})
			default:
				// `using Foo::Bar;` — left=Foo (qualifier), right=Bar.
				if tid, ok := nsMembers[left+"::"+right]; ok {
					targets[d.ID] = tid
				}
			}
		}
	}

	// Phase A: rewrite refs whose Binding points at a local KindImport
	// decl that now has a resolved target.
	if len(targets) > 0 {
		for _, p := range parsed {
			if !isCppLike(p.rel) {
				continue
			}
			for i := range p.result.Refs {
				ref := &p.result.Refs[i]
				if ref.Binding.Kind != scope.BindResolved {
					continue
				}
				if tid, ok := targets[ref.Binding.Decl]; ok {
					ref.Binding.Decl = tid
					ref.Binding.Reason = "import_export"
				}
			}
		}
	}

	// Phase B: for each file with widening entries, rewrite its
	// unresolved refs whose name matches an exported decl reachable
	// through any of the widening entries.
	for _, p := range parsed {
		if !isCppLike(p.rel) {
			continue
		}
		widenings := wideningByFile[p.rel]
		if len(widenings) == 0 {
			continue
		}
		for i := range p.result.Refs {
			ref := &p.result.Refs[i]
			if ref.Binding.Kind == scope.BindResolved {
				continue
			}
			if ref.Binding.Reason == "property_access" {
				continue
			}
			if ref.Binding.Reason == "scope_qualified_access" {
				// Handled in Phase C, which knows about the qualifier
				// chain.
				continue
			}
			for _, w := range widenings {
				var tid scope.DeclID
				var ok bool
				switch {
				case w.includeRel != "":
					exports := getExports(w.includeRel)
					if exports == nil {
						continue
					}
					tid, ok = exports[ref.Name]
				case w.nsFQN != "":
					tid, ok = nsMembers[w.nsFQN+"::"+ref.Name]
				}
				if ok {
					ref.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   tid,
						Reason: "import_export",
					}
					break
				}
			}
		}
	}

	// Phase C: resolve cross-file namespace-qualified access. For each
	// consecutive (prev, ref) pair where ref is a scope_qualified_access
	// still unresolved after Phase A/B, try to find the chain
	// prev.Name + "::" + ref.Name in the global namespace index. If the
	// hit is reachable through one of the file's includes, rewrite both
	// refs: prev → the namespace decl, ref → the member decl.
	//
	// This handles pytorch's canonical `at::Tensor` pattern: `at` is
	// declared in an included header, usage is in a .cpp file. The
	// builder now emits a ref for `at` (pre-fix it was dropped) with
	// BindUnresolved; the following scope_qualified_access ref for
	// `Tensor` carries the qualification relationship via source order.
	for _, p := range parsed {
		if !isCppLike(p.rel) {
			continue
		}
		widenings := wideningByFile[p.rel]
		// Precompute the set of included-header rels for this file;
		// used to validate that a matching nsMembers entry lives in an
		// included file (not some arbitrary repo file).
		reachable := make(map[string]bool, len(widenings))
		reachable[p.rel] = true // self-include
		for _, w := range widenings {
			if w.includeRel != "" {
				reachable[w.includeRel] = true
			}
		}
		refs := p.result.Refs
		for i := 1; i < len(refs); i++ {
			r := &refs[i]
			if r.Binding.Reason != "scope_qualified_access" {
				continue
			}
			if r.Binding.Kind == scope.BindResolved {
				continue
			}
			prev := &refs[i-1]
			fqn := prev.Name + "::" + r.Name
			tid, ok := nsMembers[fqn]
			if !ok {
				continue
			}
			// Where does this DeclID live? Cheap linear scan over
			// reachable files. Typical includer lists are small.
			hit := findDeclFile(byRel, reachable, tid)
			if hit == "" {
				continue
			}
			r.Binding = scope.RefBinding{
				Kind:   scope.BindResolved,
				Decl:   tid,
				Reason: "qualified_member",
			}
			// Optionally also rebind prev to the namespace decl. We
			// don't have a direct name→namespaceID index, but the
			// simplest correct fallback is to leave prev's binding
			// alone (unresolved). Phase 1.5 can tighten this.
		}
	}
}

// findDeclFile returns the relPath of the file containing the given
// DeclID if it lives within one of the reachable rels; "" otherwise.
// Linear scan is fine: reachable sets are O(#includes) per file, not
// global.
func findDeclFile(byRel map[string]*scope.Result, reachable map[string]bool, id scope.DeclID) string {
	for rel := range reachable {
		r, ok := byRel[rel]
		if !ok {
			continue
		}
		for i := range r.Decls {
			if r.Decls[i].ID == id {
				return rel
			}
		}
	}
	return ""
}

// buildCppNamespaceIndex returns a map of "NsA::NsB::Name" ->
// DeclID over every Exported decl nested inside a namespace declaration
// in any cpp-family file. Nesting is detected by Span containment using
// each namespace decl's FullSpan; this avoids needing Scope-owner
// metadata to be preserved end-to-end.
func buildCppNamespaceIndex(byRel map[string]*scope.Result) map[string]scope.DeclID {
	out := make(map[string]scope.DeclID)
	for _, r := range byRel {
		if r == nil {
			continue
		}
		nsDecls := make([]*scope.Decl, 0)
		for i := range r.Decls {
			if r.Decls[i].Kind == scope.KindNamespace {
				nsDecls = append(nsDecls, &r.Decls[i])
			}
		}
		if len(nsDecls) == 0 {
			continue
		}
		// FQN of each namespace decl: join the chain of outer namespace
		// decls whose FullSpan contains this namespace's Span.
		fqnOf := func(nd *scope.Decl) string {
			parts := []string{nd.Name}
			cursor := nd
			for {
				var outer *scope.Decl
				for _, cand := range nsDecls {
					if cand == cursor {
						continue
					}
					if spanStrictlyContains(cand.FullSpan, cursor.Span) {
						if outer == nil || spanStrictlyContains(outer.FullSpan, cand.FullSpan) {
							outer = cand
						}
					}
				}
				if outer == nil {
					break
				}
				parts = append([]string{outer.Name}, parts...)
				cursor = outer
			}
			return strings.Join(parts, "::")
		}
		nsFQN := make(map[*scope.Decl]string, len(nsDecls))
		for _, nd := range nsDecls {
			nsFQN[nd] = fqnOf(nd)
		}
		// Map exported decls to their innermost enclosing namespace.
		for i := range r.Decls {
			d := &r.Decls[i]
			if !d.Exported {
				continue
			}
			if d.Kind == scope.KindImport {
				continue
			}
			if d.Kind == scope.KindNamespace {
				// Register the namespace by its FQN so `using Ns::Inner`
				// works when Inner is itself a namespace. Rarely
				// meaningful for this phase, but cheap.
				if fq, ok := nsFQN[d]; ok {
					if _, exists := out[fq]; !exists {
						out[fq] = d.ID
					}
				}
				continue
			}
			var innermost *scope.Decl
			for _, nd := range nsDecls {
				if !spanStrictlyContains(nd.FullSpan, d.Span) {
					continue
				}
				if innermost == nil || spanStrictlyContains(innermost.FullSpan, nd.FullSpan) {
					innermost = nd
				}
			}
			if innermost == nil {
				continue
			}
			key := nsFQN[innermost] + "::" + d.Name
			if _, exists := out[key]; !exists {
				out[key] = d.ID
			}
		}
	}
	return out
}

// spanStrictlyContains reports whether outer contains inner (inclusive
// at both ends) AND outer has a non-empty footprint.
func spanStrictlyContains(outer, inner scope.Span) bool {
	if outer.EndByte <= outer.StartByte {
		return false
	}
	return inner.StartByte >= outer.StartByte && inner.EndByte <= outer.EndByte
}

// resolveCppInclude resolves `#include "spec"` against the repo's
// cpp-family file set. Tries the spec joined to the includer's
// directory first (the traditional lookup path), then treats it as
// repo-relative (common when include paths are configured to repo
// root). Returns the matched rel path, or "" if nothing matches.
func resolveCppInclude(fromRel, spec string, byRel map[string]*scope.Result) string {
	dir := filepath.Dir(fromRel)
	joined := filepath.ToSlash(filepath.Join(dir, spec))
	if _, ok := byRel[joined]; ok {
		return joined
	}
	normalized := filepath.ToSlash(filepath.Clean(spec))
	if _, ok := byRel[normalized]; ok {
		return normalized
	}
	return ""
}

// isCppLike reports whether a file extension is one of the C++
// extensions dispatched to cpp.Parse by Build. Pure C files (.c, .h)
// are handled by resolveImportsC.
func isCppLike(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh":
		return true
	}
	return false
}
