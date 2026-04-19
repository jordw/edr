package store

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsJava is Phase 1 of the cross-file import graph for
// Java. After per-file parsing + within/cross-file decl merging, it
// rewrites Ref.Binding so that refs to local KindImport decls point
// at the actual exported Decl in the source file.
//
// How it works:
//
//  1. Build a fully-qualified-name (FQN) -> DeclID index across all
//     *.java files in the repo. A top-level public class/interface/
//     enum named "Foo" declared in a file with `package com.acme;`
//     is keyed as "com.acme.Foo".
//  2. Also build a per-package -> file-list index so wildcard
//     imports (`import com.acme.*;`) can enumerate targets.
//  3. For each file's KindImport decls with Signature ==
//     "<path>\x00<origName>":
//       - If <origName> == "*"  → wildcard import. v1: enumerate every
//         public top-level decl in the package and, IF the same-named
//         local Import decl hasn't been explicitly bound, record a
//         target. We emit no synthetic decls; ref rewriting is the
//         only side-effect (and wildcards only affect refs that
//         already bound to a wildcard's "*" Import decl — which is
//         never, since no source code references "*"). So in practice
//         the wildcard branch is mostly a structural no-op in v1:
//         refs bound to an explicit ident's Import decl resolve via
//         the explicit path below; wildcards don't supply a target
//         for those idents.  (See doc comment on the v1 limitation.)
//       - Otherwise build FQN = <path> + "." + <origName>; look it up
//         in the FQN index. If found, target the resolved decl.
//  4. Rewrite refs whose Binding.Decl points at a local Import decl
//     with a resolved target.
//
// Scope / v1 limitations:
//   - Only top-level public classes/interfaces/enums are indexed as
//     exportable. Package-private (default), protected, private are
//     NOT exported and will not resolve across files.
//   - Nested types are not indexed (v1.5).
//   - Static imports: decl Signature prefix is `"static:<FQN>"`; the
//     resolver currently does NOT resolve these (the target would be
//     a method/field on a class that we don't index by FQN + member
//     name yet). The Signature shape is preserved for future work.
//   - Wildcard imports (`import com.acme.*;`) v1 punt: they bind the
//     "*" name locally (nothing references "*"), and they do NOT
//     widen arbitrary unqualified refs to the package's public
//     types. Cross-file binding via wildcard is future work.
//   - java.lang / stdlib / external jars are NOT in the repo and
//     stay bound to the local Import decl.
//
// See imports_ts.go for the reference implementation this mirrors.
func resolveImportsJava(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Collect Java files.
	type javaFile struct {
		p    *parsedFile
		pkg  string // dotted package name ("" for default package)
	}
	var jfiles []javaFile
	for i := range parsed {
		p := &parsed[i]
		if !isJavaLike(p.rel) {
			continue
		}
		pkg := packageOf(p.result)
		jfiles = append(jfiles, javaFile{p: p, pkg: pkg})
	}
	if len(jfiles) == 0 {
		return
	}

	// Build FQN index: "com.acme.Foo" -> DeclID of the top-level
	// public class/interface/enum named Foo declared in that file.
	// Also index public fields/methods as "com.acme.Foo.bar" for
	// future static-import resolution (not consumed by v1 but cheap).
	//
	// A top-level decl is one whose enclosing scope is the file's
	// root scope. For Java, that's Scope.Parent == 0 AND the scope's
	// Kind is ScopeFile. We approximate by checking whether the
	// decl's Scope matches the file-root scope ID.
	fqnToDecl := make(map[string]scope.DeclID)
	for _, jf := range jfiles {
		rootID := fileRootScopeID(jf.p.result)
		for i := range jf.p.result.Decls {
			d := &jf.p.result.Decls[i]
			if !d.Exported {
				continue
			}
			if d.Scope != rootID {
				continue
			}
			if d.Kind != scope.KindClass && d.Kind != scope.KindInterface &&
				d.Kind != scope.KindEnum {
				continue
			}
			key := d.Name
			if jf.pkg != "" {
				key = jf.pkg + "." + d.Name
			}
			if _, exists := fqnToDecl[key]; !exists {
				fqnToDecl[key] = d.ID
			}
		}
	}

	// Per-package -> file index (for wildcard handling should we
	// extend it). Currently unused, but built cheaply so v1.5 can
	// flip the wildcard branch on without re-iterating.
	// (Elided for now to keep the resolver small.)

	// For each file, resolve its KindImport decls.
	type importTarget struct {
		targetID scope.DeclID
	}
	targets := make(map[scope.DeclID]importTarget)

	for _, jf := range jfiles {
		for i := range jf.p.result.Decls {
			d := &jf.p.result.Decls[i]
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
			// Static imports — v1 punt. Signature shape is "static:<FQN>"
			// in the path segment; leave binding on the local Import.
			if strings.HasPrefix(path, "static:") {
				continue
			}
			if orig == "*" {
				// Wildcard — v1 punt. See doc comment above.
				continue
			}
			fqn := path + "." + orig
			if tid, ok := fqnToDecl[fqn]; ok {
				targets[d.ID] = importTarget{targetID: tid}
			}
		}
	}

	if len(targets) == 0 {
		return
	}

	// Rewrite refs whose Binding.Decl points at a local Import decl
	// with a resolved target.
	for _, jf := range jfiles {
		hasAny := false
		for i := range jf.p.result.Decls {
			if jf.p.result.Decls[i].Kind != scope.KindImport {
				continue
			}
			if _, ok := targets[jf.p.result.Decls[i].ID]; ok {
				hasAny = true
				break
			}
		}
		if !hasAny {
			continue
		}
		for i := range jf.p.result.Refs {
			ref := &jf.p.result.Refs[i]
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

// isJavaLike reports whether a file extension is the one the Java
// scope builder emits for.
func isJavaLike(rel string) bool {
	return strings.HasSuffix(rel, ".java")
}

// packageOf returns the dotted package name of a Java file's
// Result, or "" for the default package. The Java builder emits
// the package as a KindNamespace decl at file scope whose Name is
// the full dotted path (e.g. "com.acme").
func packageOf(r *scope.Result) string {
	if r == nil {
		return ""
	}
	for i := range r.Decls {
		if r.Decls[i].Kind == scope.KindNamespace {
			return r.Decls[i].Name
		}
	}
	return ""
}

// fileRootScopeID returns the ScopeID of the file-root scope in r.
// Falls back to 0 if no ScopeFile entry exists (older builds).
func fileRootScopeID(r *scope.Result) scope.ScopeID {
	if r == nil {
		return 0
	}
	for i := range r.Scopes {
		if r.Scopes[i].Kind == scope.ScopeFile {
			return r.Scopes[i].ID
		}
	}
	return 0
}
