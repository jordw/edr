package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Ruby_PuntedModel documents — via assertion — the
// reason Ruby's Phase-1 import resolver is a deliberate no-op.
//
// The Ruby builder emits KindImport decls for `require` /
// `require_relative` / `load`, but the decl's Name is the PATH
// STRING (e.g. "foo", "./bar") — not an identifier user code can
// reference. So even though a KindImport decl exists, no Ref in any
// Ruby file can ever bind to it by name. The TS-style "rewrite
// local-import-bound refs to point at the source file's decl"
// approach has nothing to rewrite.
//
// Concretely, this test asserts:
//  1. The KindImport decl for `require_relative './a'` is emitted
//     (Name == "./a").
//  2. The identifier `Foo` used in b.rb is NOT bound to any
//     KindImport decl — it either binds to a.rb's Foo (via
//     reconcileResults' open-class merging, which happens to also
//     unify file-scope refs) or remains unresolved. Either way, the
//     resolver didn't pretend there was an "import of Foo" to
//     rewrite.
//
// If this test ever fails because `Foo` starts binding to a
// KindImport decl, the Ruby builder's emission policy has changed
// and resolveImportsRuby should be re-evaluated.
func TestImportGraph_Ruby_PuntedModel(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.rb": `class Foo
  def bar; end
end
`,
		"b.rb": `require_relative './a'
Foo.new.bar
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.rb")
	rb := idx.ResultFor(root, "b.rb")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}

	// (1) The KindImport decl is emitted with the path as its Name.
	var pathImport *scope.Decl
	for i := range rb.Decls {
		d := &rb.Decls[i]
		if d.Kind == scope.KindImport && d.Name == "./a" {
			pathImport = d
			break
		}
	}
	if pathImport == nil {
		var got []string
		for _, d := range rb.Decls {
			if d.Kind == scope.KindImport {
				got = append(got, d.Name)
			}
		}
		t.Fatalf("b.rb: expected KindImport decl named \"./a\"; got %v", got)
	}

	// (2) There is NO KindImport decl named "Foo" — because
	// require_relative doesn't bind names.
	for _, d := range rb.Decls {
		if d.Kind == scope.KindImport && d.Name == "Foo" {
			t.Fatalf("b.rb: unexpected KindImport decl named \"Foo\"; require_relative should not bind names")
		}
	}

	// (3) No ref in b.rb binds to any KindImport decl. Build a lookup
	// of KindImport DeclIDs and check every ref.
	importIDs := map[scope.DeclID]string{}
	for _, d := range rb.Decls {
		if d.Kind == scope.KindImport {
			importIDs[d.ID] = d.Name
		}
	}
	for _, ref := range rb.Refs {
		if name, ok := importIDs[ref.Binding.Decl]; ok {
			t.Errorf("b.rb: ref %q bound to KindImport decl %q (ID=%d); Ruby require doesn't bind names",
				ref.Name, name, ref.Binding.Decl)
		}
	}
}

// TestImportGraph_Ruby_ReconcileMergesDeclIDs verifies the mechanism
// that DOES wire Ruby files together: reconcileResults' same-name
// class merging. Two files each containing `class Foo` must end up
// with identical DeclIDs — so symbol lookups, refs-to, and rename
// treat them as one symbol. This is what the Phase-1 import resolver
// would otherwise be responsible for duplicating; since reconcile
// already does it for Ruby, resolveImportsRuby is a no-op.
//
// Note: this does NOT test that a bare `Foo` ref inside a method
// body resolves to the merged DeclID — Ruby's per-file resolver
// currently emits bare-ident refs in NSValue, while class decls
// live in NSConstant, so they don't unify in Ruby's resolveRefs.
// That's a separate Ruby-builder concern, out of scope for the
// Phase-1 import graph work. We assert only what this layer owns:
// cross-file DeclID merging.
func TestImportGraph_Ruby_ReconcileMergesDeclIDs(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.rb": `class Foo
  def bar; 1; end
end
`,
		"b.rb": `class Foo
  def baz; 2; end
end
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.rb")
	rb := idx.ResultFor(root, "b.rb")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}

	aFoo := findDecl(ra, "Foo")
	bFoo := findDecl(rb, "Foo")
	if aFoo == nil || bFoo == nil {
		t.Fatalf("missing Foo decl: a=%v b=%v", aFoo, bFoo)
	}
	if aFoo.Kind != scope.KindClass || bFoo.Kind != scope.KindClass {
		t.Fatalf("Foo decls should be KindClass; a=%v b=%v", aFoo.Kind, bFoo.Kind)
	}
	if aFoo.ID != bFoo.ID {
		t.Fatalf("class Foo reopening not merged by reconcileResults: a=%d b=%d", aFoo.ID, bFoo.ID)
	}
}

// TestImportGraph_Ruby_ModuleReopenCrossFile is the module analogue of
// the class reopen test: same-name modules across files share a DeclID
// after reconcile. This extends existing store_test.go coverage, which
// only checks classes.
func TestImportGraph_Ruby_ModuleReopenCrossFile(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.rb": `module Helpers
  def one; end
end
`,
		"b.rb": `module Helpers
  def two; end
end
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.rb")
	rb := idx.ResultFor(root, "b.rb")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	aH := findDecl(ra, "Helpers")
	bH := findDecl(rb, "Helpers")
	if aH == nil || bH == nil {
		t.Fatalf("missing Helpers decl: a=%v b=%v", aH, bH)
	}
	if aH.ID != bH.ID {
		t.Errorf("module Helpers reopen not merged: a=%d b=%d", aH.ID, bH.ID)
	}
}
