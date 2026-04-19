package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsC is Phase 1 of the cross-file import graph for C.
//
// C has no module system — only the `#include` preprocessor directive.
// The useful v1 behavior is: when file a.c does `#include "foo.h"` and
// foo.h exports (non-static) declarations, any ref in a.c whose local
// scope chain left it BindUnresolved gets rewritten to bind to the
// matching exported decl in the header. This gives jump-to-definition
// and refs-to behavior across the most common C idiom: "header
// declares, source defines".
//
// Scope (v1 — narrow and honest):
//   - Both `#include "local.h"` and `#include <proj/path.h>` are
//     attempted against the repo tree. System headers (`<stdio.h>`,
//     `<stdint.h>`) don't match any repo file and get filtered out
//     naturally. Large C/C++ projects (pytorch, torch, most CMake-
//     driven repos) use angle-bracket form for project-internal
//     headers via `-I` flags, so restricting to quoted would skip
//     the bulk of real cross-file linkage.
//   - Direct includes only — no transitive walk. If a.c includes
//     foo.h and foo.h includes bar.h, a.c's refs resolve against
//     foo.h's exports, not bar.h's.
//   - Only BindUnresolved refs are rewritten. A ref already bound to
//     a local decl (e.g. a file-scope static) keeps that binding.
//     This prevents "declaration in header, definition in same source
//     file" from being redirected away from the definition — the
//     local definition wins.
//   - The resolver binds to the header's decl (the declaration), not
//     to the source file's definition. Header/source pairing ("find
//     foo.c whose `#include "foo.h"` matches and prefer its definition
//     of bar over the header's declaration of bar") is NOT attempted
//     in v1. Follow-up work can iterate on this: the cross-file-refs
//     layer in internal/dispatch already harvests sibling .c files
//     for refs/definitions by name, so downstream consumers can still
//     find the definition via a second hop.
//   - Unresolved header paths (e.g. `#include "missing.h"`) are
//     silently skipped. No diagnostics.
//
// Out of scope (v1): preprocessor expansion, conditional includes
// (`#if 0`), include guards, pragma-once semantics, include paths
// from build flags (-I), typedef name resolution.
func resolveImportsC(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Build lookup of repo-relative path -> *scope.Result for C
	// files only. Both .c and .h participate (headers can include
	// headers).
	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isCLike(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// exportsIdx: per-file name -> DeclID of the first exported,
	// non-import same-name decl at file scope. Built lazily.
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
			// File-scope only. The C builder only stamps Exported
			// at ScopeID(1), but be defensive.
			if d.Scope != scope.ScopeID(1) {
				continue
			}
			if _, exists := idx[d.Name]; !exists {
				idx[d.Name] = d.ID
			}
		}
		exportsByRel[rel] = idx
		return idx
	}

	// Per-file: for each quoted `#include "..."` decl, resolve the
	// target header path (if any) and accumulate that header's
	// exports into a merged "visible names" map for the includer.
	// If the same name is exported by two different included headers,
	// the first include wins (ODR violations aren't our problem).
	for _, p := range parsed {
		if !isCLike(p.rel) {
			continue
		}

		// Gather included headers (quoted, local).
		var headerRels []string
		for i := range p.result.Decls {
			d := &p.result.Decls[i]
			if d.Kind != scope.KindImport {
				continue
			}
			if d.Signature == "" {
				continue
			}
			path, _ := parseImportSignature(d.Signature)
			if path == "" {
				continue
			}
			// Attempt path resolution for both quoted and angle-bracket
			// includes. System headers naturally won't match any repo
			// file and are filtered by the empty-return below.
			headerRel := resolveQuotedInclude(p.rel, path, byRel)
			if headerRel == "" {
				continue
			}
			headerRels = append(headerRels, headerRel)
		}
		if len(headerRels) == 0 {
			continue
		}

		// Merge header exports. First include wins for collisions.
		visible := make(map[string]scope.DeclID)
		for _, h := range headerRels {
			for name, id := range getExports(h) {
				if _, exists := visible[name]; !exists {
					visible[name] = id
				}
			}
		}
		if len(visible) == 0 {
			continue
		}

		// Rewrite BindUnresolved refs whose name matches a visible
		// header export.
		for i := range p.result.Refs {
			ref := &p.result.Refs[i]
			if ref.Binding.Kind != scope.BindUnresolved {
				continue
			}
			tgt, ok := visible[ref.Name]
			if !ok {
				continue
			}
			ref.Binding = scope.RefBinding{
				Kind:   scope.BindResolved,
				Decl:   tgt,
				Reason: "include_resolution",
			}
		}
	}
}

// resolveQuotedInclude maps a `#include "path"` specifier from a
// given includer to a repo-relative path in byRel. C's include-path
// resolution is build-system dependent (`-I` flags); v1 uses two
// simple strategies that cover the bulk of real-world layouts:
//
//  1. Relative to the includer's directory (the compiler's default
//     for quoted includes).
//  2. Relative to the repo root.
//
// First hit wins. If neither matches, returns "".
func resolveQuotedInclude(fromRel, spec string, byRel map[string]*scope.Result) string {
	// Strategy 1: includer-relative.
	dir := filepath.Dir(fromRel)
	cand := filepath.ToSlash(filepath.Join(dir, spec))
	if _, ok := byRel[cand]; ok {
		return cand
	}
	// Strategy 2: repo-root-relative.
	cand = filepath.ToSlash(filepath.Clean(spec))
	if _, ok := byRel[cand]; ok {
		return cand
	}
	return ""
}

// isCLike reports whether a file is a C source or header file.
// (.cpp/.cc/.hpp etc belong to C++ and are handled by
// resolveImportsCpp.)
func isCLike(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".c", ".h":
		return true
	}
	return false
}
