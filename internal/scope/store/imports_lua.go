package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsLua is the Phase 1 import graph resolver for Lua.
// After the per-file Lua scope builder stamps each
// `local NAME = require("PATH")` decl as KindImport with
// Signature = "<PATH>\x00*", this pass:
//
//  1. Maps each KindImport's path to a repo-relative .lua file. Lua
//     module paths are dotted: `require("foo.bar")` resolves to
//     foo/bar.lua, then foo/bar/init.lua. Slash-form paths
//     (`require("foo/bar")`) are also accepted because they appear
//     in repos that follow the Penlight / LuaRocks file-path
//     convention. Paths that don't match any in-repo file are left
//     as external (no rewrite).
//
//  2. Treats every Lua require as a module handle (Lua has no named-
//     export syntax — `require` always returns the whole module
//     table). Property-access refs `mod.func` whose immediate
//     receiver resolves to such a module-handle decl are rebound to
//     the matching exported decl in the source module, with
//     Reason="import_export".
//
// Out of scope:
//   - Lua's package.path / LUA_PATH searcher overrides. We assume
//     repo-rooted dotted paths; that's the convention every Lua
//     repo we've corpus-walked actually uses.
//   - `require"long string"` (the [[...]] long-bracket form) is not
//     captured by the builder; rare in the wild.
//   - Indirect requires through aliases (`local r = require; r("x")`).
//   - Multi-hop chains (`mod.sub.fn`): only the first property hop
//     resolves through the module handle; further hops fall through
//     to the in-file resolver, which won't bind across modules.
func resolveImportsLua(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isLuaFile(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// Lazy per-file exports index: name -> DeclID of first exported
	// non-import decl. Mirrors the Python resolver shape.
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

	// moduleHandles: for each KindImport decl bound to a whole module,
	// the repo-relative file path of the target module.
	moduleHandles := make(map[scope.DeclID]string)

	for _, p := range parsed {
		if !isLuaFile(p.rel) {
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
			path, orig := parseImportSignature(d.Signature)
			if path == "" {
				continue
			}
			// Only "*" is meaningful for Lua — `require` always
			// returns the whole module. Defensive: skip anything else.
			if orig != "*" {
				continue
			}
			sourceRel := resolveLuaModule(path, byRel)
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
		if !isLuaFile(p.rel) {
			continue
		}
		refs := p.result.Refs
		// Property-access-through-module-handle rewrite. The Lua
		// builder marks `obj.x` and `obj:x` ref bindings with
		// Reason="property_access" on the property segment.
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

// resolveLuaModule maps a Lua module path (dotted "foo.bar" or
// slashed "foo/bar") to a repo-relative .lua file path. Returns ""
// if no matching file exists in the repo.
//
// Tries (in order): foo/bar.lua, foo/bar/init.lua. Slash-form input
// is accepted as-is; dotted form is converted to slashes.
func resolveLuaModule(modulePath string, byRel map[string]*scope.Result) string {
	if modulePath == "" {
		return ""
	}
	// Normalize to slash form. Lua uses "." as a module separator but
	// repos sometimes use "/"; accept either. Filesystem layout uses
	// `/` regardless of OS — paths in byRel are forward-slash repo-
	// relative.
	rel := strings.ReplaceAll(modulePath, ".", "/")
	rel = filepath.ToSlash(rel)
	rel = strings.TrimPrefix(rel, "./")

	if cand := rel + ".lua"; byRel[cand] != nil {
		return cand
	}
	if cand := rel + "/init.lua"; byRel[cand] != nil {
		return cand
	}
	return ""
}

// isLuaFile reports whether a repo-relative path is a Lua source.
func isLuaFile(rel string) bool {
	return strings.HasSuffix(rel, ".lua")
}
