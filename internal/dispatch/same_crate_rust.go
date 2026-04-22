package dispatch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/rust"
)

// rustIdentRef is a precise identifier reference in another .rs file
// that plausibly refers to the rename target. Mirrors the Java/Kotlin
// supplements in purpose but has no package-clause filter — Rust
// modules are defined by file layout, not a `package` declaration.
type rustIdentRef struct {
	file      string
	startByte uint32
	endByte   uint32
}

// rustSkipDirs lists directories that should never be walked for cross-
// file rename candidates. Keeps the walk bounded on large corpora.
var rustSkipDirs = map[string]bool{
	".git":         true,
	".edr":         true,
	"target":       true,
	"node_modules": true,
}

// rustSameCrateRefs walks every .rs file under root and returns refs
// whose name matches. Needed because Rust has no `package x;` clause
// and because `mod foo;` is not currently emitted as an import, so
// FindSemanticReferences (which relies on the import graph) returns
// nothing for cross-file Rust.
//
// Safety heuristic: if ANY sibling .rs file declares a symbol with the
// target name (at any scope), return nil to abort the walk. Rationale:
// we cannot extractively distinguish `other_mod::spawn(...)` from
// `our_mod::spawn(...)` without path resolution, so a crate with multiple
// same-name decls produces unsafe cross-file rewrites. Aborting lets the
// caller fall back to the regex + symbol-index path, which is narrower
// for Rust (the import graph is empty so it mostly finds same-file refs).
// Converting false positives into false negatives is the right trade for
// write safety.
// Returns (refs, ok). ok=false signals ambiguous crate — caller should
// fall back to the regex + symbol-index path rather than trust a
// narrower same-file-only result.
func rustSameCrateRefs(root, originFile, name string) ([]rustIdentRef, bool) {
	// First pass: any sibling file with a same-name decl disqualifies
	// the walker. This catches `fn name`, `impl X { fn name }`,
	// `struct name`, etc. — anything the Rust builder emits as a Decl.
	ambiguous := false
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || ambiguous {
			return nil
		}
		if info.IsDir() {
			if rustSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile || strings.ToLower(filepath.Ext(path)) != ".rs" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(src), name) {
			return nil
		}
		r := rust.Parse(path, src)
		if r == nil {
			return nil
		}
		for _, d := range r.Decls {
			if d.Name != name {
				continue
			}
			// Only definitions that can be called/referenced cross-file
			// count as ambiguity. Locals (var/let/const/param/field) are
			// shadowed by scope rules and don't mix with cross-file refs.
			switch d.Kind {
			case scope.KindFunction, scope.KindMethod, scope.KindClass,
				scope.KindInterface, scope.KindType, scope.KindEnum:
				ambiguous = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if ambiguous {
		return nil, false
	}

	var out []rustIdentRef
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if rustSkipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile {
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".rs" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Fast check: skip files that do not mention the name at all.
		if !strings.Contains(string(src), name) {
			return nil
		}
		r := rust.Parse(path, src)
		if r == nil {
			return nil
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(r.Decls))
		for i := range r.Decls {
			declByID[r.Decls[i].ID] = &r.Decls[i]
		}
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			// Rust decls are file-local. A ref resolved to a same-name
			// decl in THIS file is binding to that file's own decl — not
			// our cross-file target — unless the decl is a `use` import
			// (which names an external symbol by path). Skip every
			// BindResolved ref except import-resolved ones. This differs
			// from the Java/Kotlin branch, where the scope-store's main
			// loop preserves file-scope decls because they represent
			// imported cross-file bindings.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == name {
					if local.Kind != scope.KindImport {
						continue
					}
				}
			}
			// Rust symbol extraction never sets Receiver, so rename
			// always uses the bare-name regex. That means method calls
			// `obj.name(...)` against an arbitrary receiver are false
			// positives for a free-function rename — we cannot tell
			// whether `obj` is of a type whose method we renamed. Skip
			// them. `::` path refs (`mod::name(...)`) and bare calls
			// (after a `use` import) are kept.
			startByte := ref.Span.StartByte
			if ref.Binding.Reason == "property_access" && startByte > 0 && src[startByte-1] == '.' {
				continue
			}
			out = append(out, rustIdentRef{
				file:      path,
				startByte: startByte,
				endByte:   ref.Span.EndByte,
			})
		}
		return nil
	})
	return out, true
}
