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
// Scope (v1):
//   - Repo-internal modules only. Module paths that don't resolve to
//     a file in the repo (stdlib, third-party) are left as-is; the
//     ref keeps its binding to the local Import decl.
//   - Module resolution follows the builder's Signature convention:
//     "<modulePath>\x00<origName>". <modulePath> uses dots between
//     segments and leading dots for relative imports ("foo.bar",
//     ".", "..foo"). Absolute: try `foo/bar.py`, then
//     `foo/bar/__init__.py`. Relative: resolve against the importer's
//     directory; `.` → `<dir>/__init__.py` (skipped if missing).
//   - `import X.Y` (origName=="*") is a whole-module binding; v1
//     doesn't rewrite — property-access like `X.Y.func()` is a v2
//     concern once we track namespace handles.
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

	// targets: for each KindImport decl we've resolved, the target
	// DeclID in the source module. Keyed by decl.ID so the rewrite
	// pass survives scope.MergeDuplicateDecls renaming.
	targets := make(map[scope.DeclID]scope.DeclID)

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
				// Whole-module namespace import (`import X.Y [as Z]`).
				// v1: no rewrite — property-access is future work.
				continue
			}
			sourceRel := resolvePythonModule(p.rel, path, byRel)
			if sourceRel == "" {
				continue
			}
			exports := getExports(sourceRel)
			if exports == nil {
				continue
			}
			if tid, ok := exports[orig]; ok {
				targets[d.ID] = tid
			}
		}
	}

	if len(targets) == 0 {
		return
	}

	for _, p := range parsed {
		if !isPyLike(p.rel) {
			continue
		}
		// Quick-exit: does this file have any resolved Import decls?
		hasAny := false
		for i := range p.result.Decls {
			if p.result.Decls[i].Kind != scope.KindImport {
				continue
			}
			if _, ok := targets[p.result.Decls[i].ID]; ok {
				hasAny = true
				break
			}
		}
		if !hasAny {
			continue
		}
		for i := range p.result.Refs {
			ref := &p.result.Refs[i]
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
