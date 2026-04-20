package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsPython is the Phase 1 import graph resolver for
// Python. After per-file parsing, it rewrites Ref.Binding on refs that
// currently point at a local KindImport decl so they point at the
// actual exported decl in the source module.
//
// Resolution is done in two passes over the import decls:
//
//  1. Named-export rewrite: `from X import Y` (origName=Y, Y in X's
//     exports) → refs to Y rebind to X's Y decl.
//  2. Module-handle + property-access rewrite: `import X[.Y] [as Z]`
//     and `from X import Y-as-submodule` bind a MODULE (not a name).
//     When a property-access ref's immediate predecessor is such a
//     handle, the property name is looked up in that module's exports
//     and the ref is rebound with Reason="import_export". This mirrors
//     Go's resolver and catches the pytorch-style idiom
//     `from torch.nn import functional as F; F.relu(...)`.
//
// Scope:
//   - Repo-internal modules only. Module paths that don't resolve to
//     a file in the repo (stdlib, third-party) are left as-is.
//   - Module resolution follows the builder's Signature convention:
//     "<modulePath>\x00<origName>". <modulePath> uses dots between
//     segments and leading dots for relative imports ("foo.bar",
//     ".", "..foo"). Absolute: try `foo/bar.py`, then
//     `foo/bar/__init__.py`. Relative: resolve against the importer's
//     directory.
//   - Chained property access (`a.b.c`) is handled one hop: `c` binds
//     only when `b` is a direct module-handle. Multi-hop namespace
//     traversal is future work.
//   - __all__ is not parsed; exported-ness falls back to the
//     no-underscore-prefix convention the builder stamps.
func resolveImportsPython(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isPyLike(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// Exports index per file: name -> DeclID of the first exported
	// same-name decl. Skip KindImport (re-exports via `from x import y`
	// at module scope are a v1.5 concern).
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

	// targets: for each KindImport decl bound to a named export, the
	// target DeclID in the source module. Keyed by decl.ID so the
	// rewrite pass survives scope.MergeDuplicateDecls renaming.
	targets := make(map[scope.DeclID]scope.DeclID)
	// moduleHandles: for each KindImport decl bound to a whole module
	// (not a name), the repo-relative file path of the target module.
	// Populated for `import X.Y [as Z]` and for `from X import Y`
	// where Y is itself a submodule of X rather than an exported name.
	moduleHandles := make(map[scope.DeclID]string)

	for _, p := range parsed {
		if !isPyLike(p.rel) {
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
			if orig == "*" {
				// Whole-module import: `import X.Y [as Z]`. The module
				// path is `path`; bind this decl to that module so
				// subsequent property-access refs resolve through it.
				if sourceRel := resolvePythonModule(p.rel, path, byRel); sourceRel != "" {
					moduleHandles[d.ID] = sourceRel
				}
				continue
			}
			sourceRel := resolvePythonModule(p.rel, path, byRel)
			if sourceRel == "" {
				continue
			}
			exports := getExports(sourceRel)
			if exports != nil {
				if tid, ok := exports[orig]; ok {
					targets[d.ID] = tid
					continue
				}
			}
			// Fall-through: `from X import Y` where Y is not an
			// exported name in X's module file. Check whether Y is
			// itself a submodule of X (i.e. X/Y.py, X/Y/__init__.py,
			// or — when `path` itself is relative — under the same
			// base). If so, record a module handle so chained
			// property access `Y.thing` resolves.
			submodulePath := path
			if submodulePath == "" || submodulePath == "." || strings.HasSuffix(submodulePath, ".") {
				submodulePath += orig
			} else {
				submodulePath += "." + orig
			}
			if subRel := resolvePythonModule(p.rel, submodulePath, byRel); subRel != "" {
				moduleHandles[d.ID] = subRel
			}
		}
	}

	if len(targets) == 0 && len(moduleHandles) == 0 {
		return
	}

	for _, p := range parsed {
		if !isPyLike(p.rel) {
			continue
		}
		refs := p.result.Refs

		// Pass 1: named-export rewrite. Any resolved ref whose Decl
		// is a named-import target gets rebound to the source decl.
		if len(targets) > 0 {
			for i := range refs {
				ref := &refs[i]
				if ref.Binding.Kind != scope.BindResolved {
					continue
				}
				tgt, ok := targets[ref.Binding.Decl]
				if !ok {
					continue
				}
				ref.Binding.Decl = tgt
				ref.Binding.Reason = "import_export"
			}
		}

		// Pass 2: property-access-through-module-handle rewrite.
		// For each probable property_access ref, check whether the
		// immediately preceding ref resolves to a module-handle
		// import decl; if so, look up the property name in that
		// module's exports and rebind.
		if len(moduleHandles) > 0 {
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
}

// resolvePythonModule maps a module path ("foo.bar" / ".foo" / "..pkg")
// to a repo-relative file path in byRel. Returns "" if no matching file
// exists in the repo.
func resolvePythonModule(fromRel, modulePath string, byRel map[string]*scope.Result) string {
	if modulePath == "" {
		return ""
	}

	// Count leading dots to determine relative-vs-absolute.
	dots := 0
	for dots < len(modulePath) && modulePath[dots] == '.' {
		dots++
	}
	tail := modulePath[dots:]
	// Convert dotted tail to path segments.
	var segments []string
	if tail != "" {
		segments = strings.Split(tail, ".")
	}

	var baseDir string
	if dots == 0 {
		// Absolute: rooted at repo root.
		baseDir = ""
	} else {
		// Relative: start at importer's directory, then walk up
		// `dots-1` additional levels (`.` = same dir, `..` = parent).
		dir := filepath.Dir(fromRel)
		if dir == "." {
			dir = ""
		}
		for step := 1; step < dots; step++ {
			if dir == "" {
				// Walked past repo root; no resolution.
				return ""
			}
			dir = filepath.Dir(dir)
			if dir == "." {
				dir = ""
			}
		}
		baseDir = dir
	}

	joined := baseDir
	for _, seg := range segments {
		if joined == "" {
			joined = seg
		} else {
			joined = joined + "/" + seg
		}
	}
	joined = filepath.ToSlash(joined)

	// Try <path>.py, <path>.pyi, then <path>/__init__.py[i].
	// (pyi preferred for stubs when both exist? v1: .py wins.)
	if joined != "" {
		for _, ext := range []string{".py", ".pyi"} {
			if _, ok := byRel[joined+ext]; ok {
				return joined + ext
			}
		}
	}
	// Package init: "<joined>/__init__.py". When dots>0 and tail is
	// empty (e.g. `from . import X`), joined==baseDir — try its init.
	initBase := joined
	if initBase == "" {
		// `from . import X` at repo root with no tail: no package init
		// to land on. Bail.
		return ""
	}
	for _, ext := range []string{".py", ".pyi"} {
		cand := initBase + "/__init__" + ext
		if _, ok := byRel[cand]; ok {
			return cand
		}
	}
	return ""
}

// isPyLike reports whether a file extension is one of the Python
// extensions produced by the Python scope builder.
func isPyLike(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".py", ".pyi":
		return true
	}
	return false
}
