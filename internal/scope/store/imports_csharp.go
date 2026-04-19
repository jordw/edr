package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsCSharp is Phase 1 of the cross-file import graph for
// C#. After per-file parsing + cross-file decl reconciliation (partial
// classes, handled by reconcileResults), this pass rewrites Ref.Binding
// so refs into imported namespaces bind to the exported Decl in the
// source file.
//
// Signature convention (stamped by internal/scope/csharp/builder.go —
// see parseImportSignature in imports.go for the byte format):
//
//	using System;              -> "System\x00*"           (namespace widening)
//	using Foo.Bar;             -> "Foo.Bar\x00*"          (namespace widening)
//	using X = Foo.Bar;         -> "Foo\x00Bar"            (type alias)
//	using X = Foo.Bar.Baz;     -> "Foo.Bar\x00Baz"        (type alias)
//	using X = Foo;             -> "\x00Foo"               (alias, no path)
//	using static Foo.Util;     -> "static:Foo.Util\x00*"  (static member import)
//
// Namespace decls carry their full dotted name in Decl.Signature (e.g.
// "MyApp.Core.Services"); each top-level type's FQN is computed as
// "<namespaceFQN>.<typeName>".
//
// Scope (v1):
//   - Repo-internal namespaces only. Types under System.*, Microsoft.*
//     have no corresponding source file in-repo; those refs stay bound
//     to the local Import decl (the honest "external" answer).
//   - Exported = `public` top-level class/interface/enum. Internal,
//     protected, and private stay unexported. (Phase-1 approximation —
//     `internal` is assembly-wide in C#, but without assembly metadata
//     we can't prove visibility, so the conservative-correct answer is
//     to leave those refs bound to the local Import decl.)
//   - Namespace widening (`using Foo;`) rewrites refs whose name
//     matches an exported top-level type in Foo. Matches are scoped
//     per-file so a ref only rewrites when the importer actually
//     declared `using Foo;`.
//   - `using static Foo.Util;` is partially supported in v1: refs to
//     the type Util itself resolve via the alias path when Util is a
//     repo-internal type, but Util's static members are NOT exposed
//     as additional shorthand bindings (e.g. `Math.Sqrt(...)` still
//     works, but `Sqrt(...)` after `using static System.Math;` stays
//     unresolved). Exposing static members is Phase 1.5 work.
//   - Partial classes across files are already merged by reconcileResults
//     — the resolver does not re-merge.
func resolveImportsCSharp(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Step 1: Filter to *.cs files.
	type fileEntry struct {
		rel    string
		result *scope.Result
	}
	var files []fileEntry
	for _, p := range parsed {
		if !strings.EqualFold(filepath.Ext(p.rel), ".cs") {
			continue
		}
		files = append(files, fileEntry{rel: p.rel, result: p.result})
	}
	if len(files) == 0 {
		return
	}

	// Step 2: Build FQN index.
	//   - fqnToDecl: "<ns>.<Type>" -> target DeclID (exported top-level
	//     type). Types in the unnamed namespace (no enclosing
	//     `namespace` decl) key on the bare type name.
	//   - namespaceToTypes: "<ns>" -> list of (shortName, DeclID) for
	//     every exported top-level type declared in that namespace.
	//     Powers namespace widening.
	type nsType struct {
		name   string
		declID scope.DeclID
	}
	fqnToDecl := make(map[string]scope.DeclID)
	namespaceToTypes := make(map[string][]nsType)

	for _, fe := range files {
		r := fe.result
		// Map ScopeID -> namespace FQN for every ScopeNamespace scope
		// by pairing namespace decls with their owning scope based on
		// span proximity (decl span ends just before the scope span
		// starts, within a small byte window to accommodate
		// `namespace A.B.C { ` headers).
		nsFQN := make(map[scope.ScopeID]string)
		for i := range r.Decls {
			d := &r.Decls[i]
			if d.Kind != scope.KindNamespace {
				continue
			}
			fqn := d.Signature
			if fqn == "" {
				// Pre-Signature fallback: use the (first-component)
				// Name. Loses dotted accuracy but keeps the resolver
				// from crashing on results built by an older builder.
				fqn = d.Name
			}
			for j := range r.Scopes {
				s := &r.Scopes[j]
				if s.Kind != scope.ScopeNamespace {
					continue
				}
				if _, taken := nsFQN[s.ID]; taken {
					continue
				}
				// The namespace scope's body starts at or just after
				// the decl name's end byte. A 256-byte window tolerates
				// long dotted names (`namespace A.B.C.D.E { `).
				if d.Span.EndByte <= s.Span.StartByte+1 &&
					d.Span.EndByte+256 >= s.Span.StartByte {
					nsFQN[s.ID] = fqn
					break
				}
			}
		}

		// For every top-level type decl that's Exported, compute FQN
		// and add to the two indexes.
		for i := range r.Decls {
			d := &r.Decls[i]
			if !d.Exported {
				continue
			}
			switch d.Kind {
			case scope.KindClass, scope.KindInterface, scope.KindEnum:
			default:
				continue
			}
			ns := ""
			if fqn, ok := nsFQN[d.Scope]; ok {
				ns = fqn
			}
			fqn := d.Name
			if ns != "" {
				fqn = ns + "." + d.Name
			}
			// First-writer-wins; partial classes have already been
			// merged to a single canonical DeclID by reconcileResults,
			// so subsequent duplicates here would share that ID.
			if _, taken := fqnToDecl[fqn]; !taken {
				fqnToDecl[fqn] = d.ID
			}
			namespaceToTypes[ns] = append(namespaceToTypes[ns], nsType{
				name: d.Name, declID: d.ID,
			})
		}
	}

	if len(fqnToDecl) == 0 {
		return
	}

	// Step 3: Per-file rewrite.
	for _, fe := range files {
		r := fe.result

		// aliasTargets: Import decl.ID -> exported-type DeclID for
		// `using X = Foo.Bar;` forms that resolve to a repo-internal
		// type.
		aliasTargets := make(map[scope.DeclID]scope.DeclID)
		// widenedNames: short name -> DeclID for each public top-level
		// type visible via `using Foo;` in this file.
		widenedNames := make(map[string]scope.DeclID)

		for i := range r.Decls {
			d := &r.Decls[i]
			if d.Kind != scope.KindImport {
				continue
			}
			if d.Signature == "" {
				continue
			}
			path, orig := parseImportSignature(d.Signature)
			// "static:" prefix marks `using static Foo.Util;`. V1
			// behavior: treat Util as a type alias candidate so
			// refs to the type itself can still resolve; don't
			// enumerate Util's static members.
			isStatic := strings.HasPrefix(path, "static:")
			if isStatic {
				path = strings.TrimPrefix(path, "static:")
				if orig == "*" && path != "" {
					if dot := strings.LastIndexByte(path, '.'); dot >= 0 {
						orig = path[dot+1:]
						path = path[:dot]
					} else {
						orig = path
						path = ""
					}
				}
			}

			if orig == "*" && !isStatic {
				// Namespace widening. Enumerate every exported type
				// under this namespace and add to widenedNames by
				// short name. First occurrence wins on name clashes
				// (C# source would produce a compile error anyway).
				for _, t := range namespaceToTypes[path] {
					if _, taken := widenedNames[t.name]; !taken {
						widenedNames[t.name] = t.declID
					}
				}
				continue
			}

			// Aliased / named-type form: FQN = path + "." + orig (or
			// just orig when path is empty).
			fqn := orig
			if path != "" {
				fqn = path + "." + orig
			}
			if tgt, ok := fqnToDecl[fqn]; ok {
				aliasTargets[d.ID] = tgt
			}
		}

		if len(aliasTargets) == 0 && len(widenedNames) == 0 {
			continue
		}

		// Rewrite refs. Alias path rebinds resolved refs that
		// currently target a local Import decl; widening path
		// upgrades unresolved refs whose short name matches an
		// exported type visible via `using`.
		for i := range r.Refs {
			ref := &r.Refs[i]
			if ref.Binding.Kind == scope.BindResolved {
				if tgt, ok := aliasTargets[ref.Binding.Decl]; ok {
					ref.Binding.Decl = tgt
					ref.Binding.Reason = "import_export"
					continue
				}
			}
			if ref.Binding.Kind == scope.BindUnresolved ||
				ref.Binding.Decl == 0 {
				if tgt, ok := widenedNames[ref.Name]; ok {
					ref.Binding.Kind = scope.BindResolved
					ref.Binding.Decl = tgt
					ref.Binding.Reason = "import_export"
				}
			}
		}
	}
}
