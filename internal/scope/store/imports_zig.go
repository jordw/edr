package store

import (
	"path"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsZig is the Phase 1 import graph resolver for Zig.
// After the per-file Zig scope builder stamps each
// `const NAME = @import("PATH")` (or `var`) decl as KindImport with
// Signature = "<PATH>\x00*", this pass:
//
//  1. Maps each KindImport's path to a repo-relative .zig file.
//     Zig's @import takes either a relative file path (`@import(
//     "../bar/baz.zig")`) or a logical module name (`@import("std")`,
//     `@import("root")`). Only the relative-path form maps to a
//     real repo file; logical names — std, root, build_options, and
//     anything declared via the build.zig package graph — stay as
//     external imports (no rewrite, but no panic).
//
//  2. Treats every Zig @import as a module handle (Zig has no per-
//     name import syntax — the imported file's struct is a single
//     value bound to the local const). Property-access refs
//     `mod.thing` whose immediate receiver resolves to such a
//     module-handle decl are rebound to the matching exported decl
//     in the source file, with Reason="import_export".
//
// Out of scope:
//   - The build.zig dependency graph. Logical names like "std" and
//     dependencies declared via std.Build.dependency() resolve at
//     build time, not from source. We don't read build.zig.
//   - Multi-hop chains (`mod.sub.fn`): only the first property hop
//     is rewritten through the module handle.
//   - @cImport (C interop): generated bindings have no in-repo
//     source; left as external.
func resolveImportsZig(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isZigFile(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

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
			if _, exists := idx[d.Name]; !exists {
				idx[d.Name] = d.ID
			}
		}
		exportsByRel[rel] = idx
		return idx
	}

	moduleHandles := make(map[scope.DeclID]string)

	for _, p := range parsed {
		if !isZigFile(p.rel) {
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
			pth, orig := parseImportSignature(d.Signature)
			if pth == "" || orig != "*" {
				continue
			}
			sourceRel := resolveZigImport(p.rel, pth, byRel)
			if sourceRel == "" {
				continue
			}
			moduleHandles[d.ID] = sourceRel
		}
	}

	if len(moduleHandles) == 0 {
		return
	}

	for _, p := range parsed {
		if !isZigFile(p.rel) {
			continue
		}
		refs := p.result.Refs
		for i := 1; i < len(refs); i++ {
			cur := &refs[i]
			if cur.Binding.Reason != "property_access" {
				continue
			}
			prev := refs[i-1]
			if prev.Binding.Kind != scope.BindResolved {
				continue
			}
			modRel, ok := moduleHandles[prev.Binding.Decl]
			if !ok {
				continue
			}
			exports := getExports(modRel)
			if exports == nil {
				continue
			}
			tid, ok := exports[cur.Name]
			if !ok {
				continue
			}
			cur.Binding.Kind = scope.BindResolved
			cur.Binding.Decl = tid
			cur.Binding.Reason = "import_export"
		}
	}
}

// resolveZigImport maps a Zig @import argument to a repo-relative
// .zig file. Returns "" for logical module names (std, root, etc.)
// and for relative paths that don't reach a real file.
//
// fromRel is the importer's repo-relative path; importPath is the
// literal string passed to @import — either a logical name or a
// relative file path.
func resolveZigImport(fromRel, importPath string, byRel map[string]*scope.Result) string {
	// Logical names don't have an in-repo file. Recognise them by
	// the absence of a path separator AND the absence of a .zig
	// suffix. (`@import("foo.zig")` from the file's own dir is a
	// valid relative path with no separator; .zig presence
	// disambiguates.)
	if !strings.Contains(importPath, "/") && !strings.HasSuffix(importPath, ".zig") {
		return ""
	}

	dir := path.Dir(fromRel)
	if dir == "." {
		dir = ""
	}
	var rel string
	if dir == "" {
		rel = importPath
	} else {
		rel = path.Join(dir, importPath)
	}
	rel = path.Clean(rel)
	// path.Clean turns leading "../" into a "../" prefix when it
	// would escape the repo root. Treat that as unresolvable.
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return ""
	}

	if byRel[rel] != nil {
		return rel
	}
	return ""
}

// isZigFile reports whether a repo-relative path is a Zig source.
func isZigFile(rel string) bool {
	return strings.HasSuffix(rel, ".zig")
}
