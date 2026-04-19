package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsKotlin is Phase 1 of the cross-file import graph for
// Kotlin. After per-file parsing + within/cross-file decl merging, it
// rewrites Ref.Binding so refs to local KindImport decls point at the
// actual exported Decl in the source file.
//
// Kotlin model:
//   - Top-level decls (class / interface / object / enum / typealias /
//     top-level fun / top-level val / top-level var) are importable by
//     their fully-qualified name: `<package>.<declName>`. A single .kt
//     file can host multiple top-level decls of any kind, all of which
//     are individually importable — distinct from Java, which limits
//     public types to one-per-file.
//   - `import com.foo.Bar` binds local name `Bar`; the builder stamps
//     Signature="com.foo\x00Bar".
//   - `import com.foo.Bar as X` binds local name `X`; Signature stays
//     "com.foo\x00Bar" so the resolver still looks up Bar.
//   - `import com.foo.*` wildcard: v1 PUNTS. The builder emits no
//     KindImport decl for the wildcard, and the resolver does not
//     enumerate com.foo's exports onto the importer's file. Wildcards
//     are rare in practice and require a whole-package walk that is
//     future work.
//
// Out of scope (v1): static members of Java types, companion-object
// members, extension-function imports, typealias resolution chains,
// external (stdlib / Maven dep) imports — all left with the binding
// on the local KindImport decl, which is the honest "external" answer.
func resolveImportsKotlin(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Filter to .kt / .kts files.
	ktFiles := make([]parsedFile, 0, len(parsed))
	for _, p := range parsed {
		if isKotlinLike(p.rel) {
			ktFiles = append(ktFiles, p)
		}
	}
	if len(ktFiles) == 0 {
		return
	}

	// Build a global FQN -> DeclID index. FQN is "<package>.<name>" for
	// each exported, top-level, non-import, non-namespace decl. Files
	// with no package clause contribute "<name>" (empty package prefix).
	//
	// If two files export the same FQN (e.g., same name, same package,
	// different files), the first wins. Kotlin allows this across files
	// via `public` modifier but it's a namespace clash — picking one
	// deterministically is acceptable for v1.
	fqnIndex := make(map[string]scope.DeclID)
	for _, p := range ktFiles {
		pkg := packagePathFor(p.result)
		for i := range p.result.Decls {
			d := &p.result.Decls[i]
			if !d.Exported {
				continue
			}
			if d.Kind == scope.KindImport || d.Kind == scope.KindNamespace {
				continue
			}
			// Only file-scope decls are importable by FQN.
			if !isFileScope(p.result, d.Scope) {
				continue
			}
			fqn := d.Name
			if pkg != "" {
				fqn = pkg + "." + d.Name
			}
			if _, exists := fqnIndex[fqn]; !exists {
				fqnIndex[fqn] = d.ID
			}
		}
	}
	if len(fqnIndex) == 0 {
		return
	}

	// Per-file: for each KindImport decl with a Signature, look up the
	// FQN in the global index. Map by decl.ID so the rewrite pass can
	// look up each ref's import target in O(1). ID-keyed to survive
	// scope.MergeDuplicateDecls renaming.
	type importTarget struct {
		targetID scope.DeclID
	}
	targets := make(map[scope.DeclID]importTarget)

	for _, p := range ktFiles {
		for i := range p.result.Decls {
			d := &p.result.Decls[i]
			if d.Kind != scope.KindImport {
				continue
			}
			if d.Signature == "" {
				continue
			}
			path, orig := parseImportSignature(d.Signature)
			if path == "" || orig == "" {
				continue
			}
			if orig == "*" {
				// Wildcard: v1 punt — the builder shouldn't even emit a
				// decl for this, but be defensive.
				continue
			}
			fqn := path + "." + orig
			if tid, ok := fqnIndex[fqn]; ok {
				targets[d.ID] = importTarget{targetID: tid}
			}
		}
	}

	if len(targets) == 0 {
		return
	}

	// Rewrite refs whose Binding.Decl points at a local Import decl
	// that now has a resolved target.
	for _, p := range ktFiles {
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

// isKotlinLike reports whether a file extension is one of the Kotlin
// extensions produced by the Kotlin scope builder.
func isKotlinLike(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".kt", ".kts":
		return true
	}
	return false
}

// packagePathFor returns the package dotted path for a Kotlin file, or
// "" if no `package` clause was parsed. The Kotlin builder encodes this
// as a file-scope KindNamespace decl whose Name is the full package.
func packagePathFor(r *scope.Result) string {
	if r == nil {
		return ""
	}
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Kind != scope.KindNamespace {
			continue
		}
		if !isFileScope(r, d.Scope) {
			continue
		}
		return d.Name
	}
	return ""
}

// isFileScope reports whether the given scope ID is the file-scope
// Scope for this Result (the scope whose Parent is zero and Kind is
// ScopeFile).
func isFileScope(r *scope.Result, sid scope.ScopeID) bool {
	if r == nil {
		return false
	}
	for i := range r.Scopes {
		if r.Scopes[i].ID == sid {
			return r.Scopes[i].Parent == 0 &&
				r.Scopes[i].Kind == scope.ScopeFile
		}
	}
	return false
}
