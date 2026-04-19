package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsTS is Phase 1 of the cross-file import graph for
// TypeScript/JavaScript. After per-file parsing + within/cross-file
// decl merging, it rewrites Ref.Binding so that refs to local
// KindImport decls point at the actual exported Decl in the source
// file.
//
// Scope (v1):
//   - Relative module specifiers only (`./foo`, `../bar/baz`).
//     Non-relative specifiers (`react`, `@acme/ui`) are left as-is
//     — the ref keeps its binding to the local Import decl, which
//     remains the honest \"external\" answer.
//   - Extension fallback for bare paths: .ts, .tsx, .d.ts, /index.ts,
//     /index.tsx, /index.d.ts. First hit wins.
//   - Default imports resolve to the source file's \"default\" export
//     (v1: heuristic — a top-level decl named \"default\", or a lone
//     exported decl whose name matches nothing we can't see; v1
//     simply skips default resolution unless the source file has an
//     exported decl literally named \"default\"). In practice TS
//     projects use named exports; default is punted.
//   - Namespace imports (`import * as m from './a'`) bind \`m\` to a
//     synthetic \"namespace handle\" — the resolver does not rewrite
//     them (property-access on namespace imports is a v2 concern).
//
// Out of scope (v1): node_modules, tsconfig paths, barrel re-exports,
// `export { X } from './y'`, `export * from './z'`.
func resolveImportsTS(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Build a fast lookup: relPath -> *scope.Result. Only TS/JS files
	// participate; other languages are skipped.
	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isTSLike(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// Exports index per file: name -> DeclID of the first exported
	// same-name decl (value namespace preferred over type; other
	// namespaces also accepted). Built lazily.
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
		// Prefer value-namespace decls; fall back to type namespace;
		// then any namespace. Skip KindImport (re-exports are v1.5).
		// Two passes so value wins over type when both exist.
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
			if len(idx) > 0 && preferred == scope.NSValue {
				// Still run type pass to cover type-only exports.
				continue
			}
		}
		exportsByRel[rel] = idx
		return idx
	}

	// Per-file: for each KindImport decl with a Signature, compute the
	// target DeclID (or 0 if unresolved). We index by decl.ID so the
	// rewrite pass can look up each ref's import target in O(1).
	// ID-keyed to survive scope.MergeDuplicateDecls renaming.
	type importTarget struct {
		targetID scope.DeclID
	}
	targets := make(map[scope.DeclID]importTarget)

	for _, p := range parsed {
		if !isTSLike(p.rel) {
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
			if !isRelativeImport(path) {
				// External (node_modules / bare specifier). Leave
				// the ref bound to the local Import decl.
				continue
			}
			sourceRel := resolveRelative(p.rel, path, byRel)
			if sourceRel == "" {
				continue
			}
			if orig == "*" {
				// Namespace import. v1: no rewrite — the binding stays
				// on the local Import decl. Future: synthesize a
				// namespace handle so obj.Foo property-access resolves.
				continue
			}
			exports := getExports(sourceRel)
			if exports == nil {
				continue
			}
			if orig == "default" {
				// v1: only resolve when an exported decl named
				// "default" literally exists (rare). Most default
				// exports are `export default <expr>` which we don't
				// track. Punt.
				if tid, ok := exports["default"]; ok {
					targets[d.ID] = importTarget{targetID: tid}
				}
				continue
			}
			if tid, ok := exports[orig]; ok {
				targets[d.ID] = importTarget{targetID: tid}
			}
		}
	}

	if len(targets) == 0 {
		return
	}

	// Rewrite refs whose Binding.Decl points at a local Import decl
	// that now has a resolved target.
	for _, p := range parsed {
		if !isTSLike(p.rel) {
			continue
		}
		// Quick-exit: does this file have any Import decls with targets?
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

// resolveRelative resolves a relative module specifier from a source
// file to a repo-relative file path in byRel. Applies the v1 extension
// fallback list; returns "" if nothing matches.
//
// The fallbacks mirror TS's moduleResolution "node"/"bundler" for the
// common cases, minus conditional exports and package.json "main".
var extensionFallbacks = []string{
	"",
	".ts",
	".tsx",
	".d.ts",
	".js",
	".jsx",
	"/index.ts",
	"/index.tsx",
	"/index.d.ts",
	"/index.js",
	"/index.jsx",
}

func resolveRelative(fromRel, spec string, byRel map[string]*scope.Result) string {
	// Resolve the relative specifier against the importer's directory.
	dir := filepath.Dir(fromRel)
	joined := filepath.Join(dir, spec)
	// filepath.Join uses OS separators; for map-key consistency with
	// repo-relative rel paths (always forward slash in edr), convert.
	joined = filepath.ToSlash(joined)

	// Try each fallback. Strip ".ts"/".tsx"/".d.ts"/".js"/".jsx" from
	// the specifier if the user explicitly wrote one (uncommon but legal
	// in bundler mode for .js in some configs) — TS's "allowImportingTsExtensions"
	// and equivalent. We just try the path as-is first; if that matches,
	// great.
	for _, ext := range extensionFallbacks {
		cand := joined + ext
		// filepath.Join may have normalized ../ — that's fine, exact match.
		if _, ok := byRel[cand]; ok {
			return cand
		}
	}
	return ""
}

// isTSLike reports whether a file extension is one of the TS/JS
// extensions produced by the TS scope builder.
func isTSLike(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		return true
	}
	return false
}
