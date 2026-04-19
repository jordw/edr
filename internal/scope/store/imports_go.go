package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsGo is the Phase 1 import graph resolver for Go. After
// the per-file Go scope builder stamps each KindImport decl with
// Signature = "<importPath>\x00*" (see internal/scope/golang/builder.go),
// this pass rewrites property-access refs of the form `pkg.Name` so
// they bind to the actual exported Decl in the imported package when
// that package is a directory in the same repo.
//
// Approach (v1, repo-local only):
//   1. Index parsed .go files by their containing repo-relative
//      directory. That directory is the implicit Go package.
//   2. For each KindImport decl, parse its Signature to get the
//      import path (e.g. "github.com/jordw/edr/internal/scope"). Find
//      the in-repo directory whose path is the longest suffix of that
//      import path. (Go's module path → filesystem layout isn't
//      trivially recoverable without go.mod; suffix-matching covers
//      the common case where module path prefix = github.com/user/repo
//      and everything after that maps to a directory under the root.)
//      Record the mapping from decl ID → target directory.
//   3. Build per-target-dir exports indexes lazily (name → DeclID,
//      over every .go file in that dir; skipping KindImport).
//   4. Per file, walk refs in order. A property-access ref (produced
//      by the builder for identifiers after `.`) takes its receiver
//      from the immediately preceding ref. When that receiver resolves
//      to a KindImport decl with a known target dir AND the property
//      name is exported in that dir, rewrite the property ref's
//      Binding to the target decl with Reason="import_export".
//
// Out of scope (v1):
//   - go.mod / vendor / GOPATH lookups — we infer package dirs by
//     suffix-matching the import path against actual repo dirs.
//   - Cross-package method dispatch on imported types.
//   - Refs on chained expressions (x.y.Z): only the last segment is
//     considered, and only when its immediate receiver is a resolved
//     import decl.
//   - Imports whose binding is unresolved (external / bare specifiers
//     with no matching repo dir) stay bound to the local Import decl,
//     which remains the honest external answer.
func resolveImportsGo(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// 1. Partition parsed files into Go vs everything else, and index
	//    by directory for package-lookup. Non-Go files are ignored.
	goFiles := make([]*parsedFile, 0, len(parsed))
	filesByDir := make(map[string][]*scope.Result)
	for i := range parsed {
		p := &parsed[i]
		if !isGoFile(p.rel) {
			continue
		}
		goFiles = append(goFiles, p)
		dir := filepath.ToSlash(filepath.Dir(p.rel))
		if dir == "." {
			dir = ""
		}
		filesByDir[dir] = append(filesByDir[dir], p.result)
	}
	if len(goFiles) == 0 {
		return
	}

	// 2. Map each KindImport decl → resolved repo-local target
	//    directory (or leave unset for external / unresolved imports).
	importTargetDir := make(map[scope.DeclID]string)
	// Collect dirs that have any .go file — candidates for suffix match.
	repoDirs := make([]string, 0, len(filesByDir))
	for dir := range filesByDir {
		repoDirs = append(repoDirs, dir)
	}
	for _, p := range goFiles {
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
			if dir, ok := resolveGoImport(path, repoDirs, filesByDir); ok {
				importTargetDir[d.ID] = dir
			}
		}
	}
	if len(importTargetDir) == 0 {
		return
	}

	// 3. Build exports-by-dir indexes lazily. The index is the union
	//    over every .go file in the dir of that file's exported decls
	//    (first wins on duplicate names — cross-file reconcile should
	//    have already merged true duplicates to the same ID anyway).
	type exportsIdx map[string]scope.DeclID
	exportsByDir := make(map[string]exportsIdx)
	getExports := func(dir string) exportsIdx {
		if idx, ok := exportsByDir[dir]; ok {
			return idx
		}
		files := filesByDir[dir]
		idx := make(exportsIdx, 16*len(files))
		for _, r := range files {
			for j := range r.Decls {
				d := &r.Decls[j]
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
		}
		exportsByDir[dir] = idx
		return idx
	}

	// 4. Per file, rewrite property-access refs whose immediate
	//    receiver is a resolved import with a known target dir.
	for _, p := range goFiles {
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
			dir, ok := importTargetDir[prev.Binding.Decl]
			if !ok {
				continue
			}
			// Sanity check: receiver should sit directly before cur's
			// identifier (separated by a single `.`). Without this a
			// chain like `a.b.c` where `a` is an import but `b.c` is a
			// further property-access on whatever `a.b` returned would
			// wrongly resolve `c` into the package. The per-segment
			// receiver check guards against that because in that chain
			// the ref immediately before `c` is `b`, not `a`, and `b`
			// is a property_access ref — not a resolved import.
			exports := getExports(dir)
			targetID, ok := exports[cur.Name]
			if !ok {
				continue
			}
			cur.Binding.Kind = scope.BindResolved
			cur.Binding.Decl = targetID
			cur.Binding.Reason = "import_export"
		}
	}
}

// resolveGoImport maps a Go import-path literal to a repo-relative
// directory. Returns ("", false) for external / unresolvable paths.
//
// Heuristic: pick the directory whose path is the longest suffix of
// the import path. This handles the common `module = github.com/a/b`
// +  `package = github.com/a/b/x/y` → `x/y` case without reading
// go.mod. It also handles the flat case where repo layout matches
// path exactly. False positives are possible (e.g. an in-repo dir
// "scope" will suffix-match an external "github.com/other/scope")
// — in practice the longest-suffix rule gives the correct answer
// when a genuine match exists, and the empty-string case (dir ""
// i.e. repo root) is excluded to avoid trivially matching everything.
func resolveGoImport(path string, repoDirs []string, filesByDir map[string][]*scope.Result) (string, bool) {
	bestDir := ""
	bestLen := 0
	for _, dir := range repoDirs {
		if dir == "" {
			continue
		}
		if path == dir || strings.HasSuffix(path, "/"+dir) {
			if len(dir) > bestLen {
				bestDir = dir
				bestLen = len(dir)
			}
		}
	}
	if bestLen == 0 {
		return "", false
	}
	return bestDir, true
}

// isGoFile reports whether a repo-relative path ends in ".go". We
// only operate on Go sources here — mixed-language repos are fine
// because non-Go files are filtered out before any lookup.
func isGoFile(rel string) bool {
	return strings.HasSuffix(strings.ToLower(rel), ".go")
}
