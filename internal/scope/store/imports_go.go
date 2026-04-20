package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
// Approach:
//   1. Index parsed .go files by their containing repo-relative
//      directory. That directory is the implicit Go package.
//   2. Walk the repo for go.mod files and parse each one's `module`
//      directive. This yields a set of (modulePath, dirOfGoMod) pairs
//      that let us map an import path to its filesystem layout
//      without any heuristic guessing — the authoritative Go way.
//   3. For each KindImport decl, parse its Signature to get the
//      import path. Try go.mod-prefix resolution first (exact or
//      prefix match against any parsed module path); fall back to
//      longest-suffix matching for repos with no go.mod or imports
//      that don't belong to any parsed module. Record the mapping
//      from decl ID → target directory.
//   4. Build per-target-dir exports indexes lazily (name → DeclID,
//      over every .go file in that dir; skipping KindImport).
//   5. Per file, walk refs in order. A property-access ref (produced
//      by the builder for identifiers after `.`) takes its receiver
//      from the immediately preceding ref. When that receiver resolves
//      to a KindImport decl with a known target dir AND the property
//      name is exported in that dir, rewrite the property ref's
//      Binding to the target decl with Reason="import_export".
//
// Out of scope:
//   - go.mod `replace` directives (used by kubernetes staging/ and
//     some monorepos to remap module paths to in-repo dirs).
//   - Vendored dependencies under vendor/.
//   - Cross-package method dispatch on imported types.
//   - Refs on chained expressions (x.y.Z): only the last segment is
//     considered, and only when its immediate receiver is a resolved
//     import decl.
//   - Imports whose binding is unresolved (external / bare specifiers
//     with no matching repo dir) stay bound to the local Import decl,
//     which remains the honest external answer.
func resolveImportsGo(parsed []parsedFile, root string) {
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
	// Parse go.mod files for authoritative module-path → dir mapping.
	// Empty slice is fine — resolveGoImport falls back to suffix match.
	modules := readGoModules(root)
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
			if dir, ok := resolveGoImport(path, modules, repoDirs, filesByDir); ok {
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
// Two-stage resolution:
//
//  1. go.mod prefix match (authoritative). For each parsed module,
//     if the import path equals the module path or has it as a
//     slash-delimited prefix, compute the subdirectory relative to
//     the go.mod file and check that the resulting repo-relative dir
//     has .go files. Modules are pre-sorted deepest-first so nested
//     go.mod files win over outer ones in a monorepo.
//
//  2. Longest-suffix fallback. Used when no module prefix matches —
//     for repos without a go.mod, imports that point outside the
//     parsed modules, or cases where the deepest-module logic didn't
//     resolve (e.g. the target dir isn't present in filesByDir
//     because it's empty or excluded). Picks the directory whose
//     path is the longest suffix of the import path; the empty-
//     string dir (repo root) is excluded to avoid trivially matching
//     every import.
func resolveGoImport(path string, modules []goModule, repoDirs []string, filesByDir map[string][]*scope.Result) (string, bool) {
	// Stage 1: go.mod prefix match.
	for _, m := range modules {
		var sub string
		switch {
		case path == m.modulePath:
			sub = ""
		case strings.HasPrefix(path, m.modulePath+"/"):
			sub = path[len(m.modulePath)+1:]
		default:
			continue
		}
		var candidate string
		switch {
		case m.dir == "" && sub == "":
			candidate = ""
		case m.dir == "":
			candidate = sub
		case sub == "":
			candidate = m.dir
		default:
			candidate = m.dir + "/" + sub
		}
		if _, ok := filesByDir[candidate]; ok {
			return candidate, true
		}
		// If the deepest module owns this import path but the target
		// dir has no .go files, don't fall through to other modules —
		// it's genuinely external to the repo, or vendored.
		return "", false
	}

	// Stage 2: longest-suffix fallback.
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

// goModule pairs a module path declared by a go.mod's `module`
// directive with the repo-relative directory containing that go.mod.
// An empty `dir` means the go.mod sits at the repo root.
type goModule struct {
	modulePath string
	dir        string
}

// readGoModules walks `root` for go.mod files, parses each one's
// `module` directive, and returns the resulting modules sorted
// deepest-first (longest module path first). Deepest-first order
// makes a naive linear scan in resolveGoImport prefer the most
// specific module when a monorepo nests modules.
//
// Directories commonly excluded from builds (.git, vendor,
// node_modules, build, dist, target) are skipped so the walk stays
// fast on large repos.
func readGoModules(root string) []goModule {
	if root == "" {
		return nil
	}
	var mods []goModule
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" ||
				name == "build" || name == "dist" || name == "target" ||
				name == ".edr" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "go.mod" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		mp := parseGoModulePath(data)
		if mp == "" {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "." {
			relSlash = ""
		}
		mods = append(mods, goModule{modulePath: mp, dir: relSlash})
		return nil
	})
	sort.Slice(mods, func(i, j int) bool {
		return len(mods[i].modulePath) > len(mods[j].modulePath)
	})
	return mods
}

// parseGoModulePath returns the argument of a go.mod file's `module`
// directive, or "" if none is present. We recognize the directive on
// any line whose first non-whitespace token is `module`; comments
// (`//`) and blank lines are skipped. The argument may be quoted
// ("module \"foo\"" is legal per the go.mod grammar) — quotes are
// stripped. Trailing line comments after the path are tolerated.
//
// This is deliberately a hand-rolled parser rather than a dependency
// on golang.org/x/mod/modfile: we only need the module directive,
// not the full grammar (require / replace / retract / exclude /
// block form), and adding a stdlib-adjacent dep would pull in
// golang.org/x/mod transitively for one line of text.
func parseGoModulePath(data []byte) string {
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "module")
		if !ok {
			continue
		}
		if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
			// `moduleSomething` — not the directive.
			continue
		}
		rest = strings.TrimSpace(rest)
		// Strip a trailing line comment if any.
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		// Strip surrounding quotes.
		if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
			rest = rest[1 : len(rest)-1]
		}
		if rest == "" {
			continue
		}
		return rest
	}
	return ""
}

// isGoFile reports whether a repo-relative path ends in ".go". We
// only operate on Go sources here — mixed-language repos are fine
// because non-Go files are filtered out before any lookup.
func isGoFile(rel string) bool {
	return strings.HasSuffix(strings.ToLower(rel), ".go")
}
