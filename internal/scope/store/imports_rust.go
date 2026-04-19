package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsRust is Phase 1 of the cross-file import graph for
// Rust. After per-file parsing + within/cross-file decl merging, it
// rewrites Ref.Binding so that refs to local KindImport decls point at
// the actual exported Decl in the source file.
//
// Scope (v1):
//   - Repo-internal only. External crates (`use std::fmt::Display`,
//     `use serde::Serialize`) stay bound to the local Import decl —
//     the honest "external" answer.
//   - Module-path resolution: since edr doesn't know crate roots, we
//     use a path-suffix heuristic rather than parsing `mod` trees:
//     * `crate::foo::Bar`  -> any file ending in `/foo.rs` or
//       `/foo/mod.rs` within the repo (the module is `foo`, and we
//       treat `crate` as the project root glob).
//     * `self::foo::Bar`   -> relative to importer's directory:
//       `<dir>/foo.rs` or `<dir>/foo/mod.rs`.
//     * `super::foo::Bar`  -> relative to importer's parent dir.
//     * No-prefix `foo::Bar` -> treated like `crate::foo::Bar` for v1
//       (edition-2018+ convention).
//   - Exports index per file: `pub` decls at file scope (Decl.Exported).
//   - Aliased use (`use foo::Bar as Qux`) resolves via the origName
//     stored in the Signature.
//   - Glob (`use foo::*`) — Signature has origName "*". V1 does not
//     enumerate glob targets; the ref stays bound to the local Import
//     decl.
//
// Out of scope (v1):
//   - Inline `mod foo { ... }` modules with nested items.
//   - `#[path = "..."]` attribute overrides on mod declarations.
//   - `pub use` re-exports (barrel-style aggregation).
//   - Workspace crate resolution across Cargo packages.
//   - Visibility checks (pub(crate) vs pub(super) — any Exported decl
//     is considered reachable).
func resolveImportsRust(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Build a fast lookup: relPath -> *scope.Result. Only .rs files
	// participate; other languages are skipped.
	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !strings.EqualFold(filepath.Ext(p.rel), ".rs") {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// Index files by repo-relative path for path-suffix lookups.
	// We'll walk byRel for each importer; the repo is small enough
	// that this is fine for v1.
	//
	// Exports index per file: name -> DeclID of the first exported
	// same-name decl. Built lazily.
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
		// Prefer value-namespace decls; fall back to type namespace.
		// Skip KindImport (re-exports are v1.5).
		for _, preferred := range []scope.Namespace{scope.NSValue, scope.NSType, ""} {
			for i := range r.Decls {
				d := &r.Decls[i]
				if !d.Exported {
					continue
				}
				if d.Kind == scope.KindImport {
					continue
				}
				if preferred != "" && d.Namespace != preferred {
					continue
				}
				if _, exists := idx[d.Name]; !exists {
					idx[d.Name] = d.ID
				}
			}
		}
		exportsByRel[rel] = idx
		return idx
	}

	// Pre-compute lookup for each decl ID -> target DeclID (or unresolved).
	type importTarget struct {
		targetID scope.DeclID
	}
	targets := make(map[scope.DeclID]importTarget)

	for _, p := range parsed {
		if !strings.EqualFold(filepath.Ext(p.rel), ".rs") {
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
			modulePath, orig := parseImportSignature(d.Signature)
			if orig == "*" {
				// Glob: v1 does not enumerate.
				continue
			}
			if orig == "" {
				continue
			}
			candidates := resolveRustModule(p.rel, modulePath, byRel)
			if len(candidates) == 0 {
				continue
			}
			// Scan candidates in order; first with a matching exported
			// name wins.
			for _, cand := range candidates {
				exports := getExports(cand)
				if exports == nil {
					continue
				}
				if tid, ok := exports[orig]; ok {
					targets[d.ID] = importTarget{targetID: tid}
					break
				}
			}
		}
	}

	if len(targets) == 0 {
		return
	}

	// Rewrite refs whose Binding.Decl points at a local Import decl
	// that now has a resolved target.
	for _, p := range parsed {
		if !strings.EqualFold(filepath.Ext(p.rel), ".rs") {
			continue
		}
		// Quick-exit: any Import decls with targets?
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
			ref.Binding.Decl = tgt.targetID
			ref.Binding.Reason = "import_export"
		}
	}
}

// resolveRustModule returns candidate source files (repo-relative paths)
// that the given modulePath may resolve to. The list is ordered by
// specificity; callers try them in order.
//
// modulePath is the `::`-joined path prefix from the Signature (e.g.
// "crate::foo::bar", "self::foo", "super", or "foo::bar"). The final
// binder is NOT part of modulePath — it's the `orig` name.
//
// Returns the empty slice for unresolvable/external paths.
func resolveRustModule(fromRel, modulePath string, byRel map[string]*scope.Result) []string {
	if modulePath == "" {
		return nil
	}
	segs := strings.Split(modulePath, "::")
	if len(segs) == 0 {
		return nil
	}

	// Determine base directory and remaining segments after prefix
	// handling. `crate` is treated as an open-ended prefix (search
	// the whole repo by path-suffix); `self` and `super` re-root to
	// importer-relative dirs.
	var baseDir string
	var rem []string
	var crateRooted bool
	switch segs[0] {
	case "crate":
		crateRooted = true
		rem = segs[1:]
	case "self":
		baseDir = filepath.ToSlash(filepath.Dir(fromRel))
		rem = segs[1:]
	case "super":
		baseDir = filepath.ToSlash(filepath.Dir(filepath.Dir(fromRel)))
		rem = segs[1:]
	default:
		// No-prefix: edition-2018+ treats this as crate-relative.
		crateRooted = true
		rem = segs
	}

	if len(rem) == 0 {
		// Module path was just "self" or "super" or "crate" with
		// nothing following — the `orig` (final binder) is a direct
		// member. For `self` this means the importer's own module.
		// V1 does not attempt to resolve these.
		return nil
	}

	// Construct candidate file patterns. The last remaining segment
	// is the module name; everything before it is a path prefix.
	//
	// For `foo::bar::Baz` with orig=Baz:
	//   modulePath = "crate::foo::bar"
	//   rem        = ["foo", "bar"]
	//   file       = ".../foo/bar.rs" or ".../foo/bar/mod.rs"
	// The final binder (Baz) is resolved against that file's exports.
	suffixA := filepath.ToSlash(filepath.Join(append([]string{}, rem...)...) + ".rs")
	remMod := append(append([]string{}, rem...), "mod.rs")
	suffixB := filepath.ToSlash(filepath.Join(remMod...))

	var out []string
	if crateRooted {
		// Match any repo path whose rel ends with the candidate suffix.
		// Prefer shorter paths (less nesting) first for determinism.
		matches := func(suffix string) []string {
			var hits []string
			for rel := range byRel {
				if rel == suffix || strings.HasSuffix(rel, "/"+suffix) {
					hits = append(hits, rel)
				}
			}
			return hits
		}
		out = append(out, matches(suffixA)...)
		out = append(out, matches(suffixB)...)
	} else {
		// Relative to baseDir.
		tryA := filepath.ToSlash(filepath.Join(baseDir, suffixA))
		tryB := filepath.ToSlash(filepath.Join(baseDir, suffixB))
		if _, ok := byRel[tryA]; ok {
			out = append(out, tryA)
		}
		if _, ok := byRel[tryB]; ok {
			out = append(out, tryB)
		}
	}

	return out
}
