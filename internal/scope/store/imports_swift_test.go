package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Swift_SameModuleCrossFileClass: `class Foo` in A.swift,
// ref to `Foo` in B.swift → the ref resolves cross-file to A's class,
// reason "same_module". This is the repo-as-one-module v1 behavior.
func TestImportGraph_Swift_SameModuleCrossFileClass(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "class Foo {}\n",
		"B.swift": "func use() { let x = Foo() }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "A.swift")
	rb := idx.ResultFor(root, "B.swift")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: A=%v B=%v", ra, rb)
	}
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil {
		t.Fatalf("A.swift: no Foo decl")
	}
	if !aFoo.Exported {
		t.Errorf("A.swift Foo: Exported=false, want true (Swift default is internal)")
	}
	refs := refsByName(rb, "Foo")
	if len(refs) == 0 {
		t.Fatal("B.swift: no refs to Foo")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID {
			found = true
			if r.Binding.Reason != "same_module" {
				t.Errorf("B.swift Foo ref reason = %q, want \"same_module\"",
					r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("B.swift: no ref to Foo resolved to A.swift's DeclID %d; bindings=%v",
			aFoo.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Swift_PublicCrossFile: an explicit `public class Foo`
// behaves identically to default (internal) — both are exported.
func TestImportGraph_Swift_PublicCrossFile(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "public class Foo {}\n",
		"B.swift": "func use() { let x = Foo() }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "A.swift")
	rb := idx.ResultFor(root, "B.swift")
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil || !aFoo.Exported {
		t.Fatalf("A.swift: Foo missing or not exported: %+v", aFoo)
	}
	refs := refsByName(rb, "Foo")
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved &&
			r.Binding.Decl == aFoo.ID &&
			r.Binding.Reason == "same_module" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("B.swift: public Foo not cross-resolved; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Swift_PrivateStaysLocal: `private class Foo` in A.swift
// must NOT be visible from B.swift. The ref stays unresolved.
func TestImportGraph_Swift_PrivateStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "private class Foo {}\n",
		"B.swift": "func use() { let x = Foo() }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "A.swift")
	rb := idx.ResultFor(root, "B.swift")
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil {
		t.Fatalf("A.swift: Foo decl missing")
	}
	if aFoo.Exported {
		t.Errorf("A.swift private Foo: Exported=true, want false")
	}
	refs := refsByName(rb, "Foo")
	if len(refs) == 0 {
		t.Fatal("B.swift: no refs to Foo")
	}
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID {
			t.Errorf("B.swift: ref to private Foo cross-resolved; binding=%+v",
				r.Binding)
		}
		if r.Binding.Reason == "same_module" {
			t.Errorf("B.swift: ref has same_module reason for private decl; binding=%+v",
				r.Binding)
		}
	}
}

// TestImportGraph_Swift_FileprivateStaysLocal: `fileprivate` is
// treated the same as `private` for export purposes.
func TestImportGraph_Swift_FileprivateStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "fileprivate class Foo {}\n",
		"B.swift": "func use() { let x = Foo() }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "A.swift")
	rb := idx.ResultFor(root, "B.swift")
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil {
		t.Fatalf("A.swift: Foo decl missing")
	}
	if aFoo.Exported {
		t.Errorf("A.swift fileprivate Foo: Exported=true, want false")
	}
	refs := refsByName(rb, "Foo")
	for _, r := range refs {
		if r.Binding.Reason == "same_module" {
			t.Errorf("B.swift: fileprivate decl cross-resolved; binding=%+v",
				r.Binding)
		}
	}
}

// TestImportGraph_Swift_AmbiguousNameNotRewritten: two files each
// export the same bare name → refs from a third file stay unresolved.
// We refuse to guess.
func TestImportGraph_Swift_AmbiguousNameNotRewritten(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "class Foo {}\n",
		"B.swift": "struct Foo {}\n",
		"C.swift": "func use() { let x = Foo() }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rc := idx.ResultFor(root, "C.swift")
	refs := refsByName(rc, "Foo")
	if len(refs) == 0 {
		t.Fatal("C.swift: no refs to Foo")
	}
	for _, r := range refs {
		if r.Binding.Reason == "same_module" {
			t.Errorf("C.swift: ambiguous Foo was rewritten; binding=%+v",
				r.Binding)
		}
		if r.Binding.Kind == scope.BindResolved &&
			r.Binding.Reason != "builtin" &&
			r.Binding.Reason != "direct_scope" {
			t.Errorf("C.swift: ambiguous Foo resolved to unexpected decl; binding=%+v",
				r.Binding)
		}
	}
}

// TestImportGraph_Swift_LocalResolutionPreferred: a ref that already
// resolves via the local scope chain must NOT be rewritten by the
// same-module resolver — even if another file exports a matching name.
func TestImportGraph_Swift_LocalResolutionPreferred(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "class Foo {}\n",
		"B.swift": "class Foo {}\nfunc use() { let x = Foo() }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rb := idx.ResultFor(root, "B.swift")
	bFoo := findDecl(rb, "Foo")
	if bFoo == nil {
		t.Fatalf("B.swift: local Foo decl missing")
	}
	refs := refsByName(rb, "Foo")
	if len(refs) == 0 {
		t.Fatal("B.swift: no refs to Foo")
	}
	for _, r := range refs {
		if r.Binding.Kind != scope.BindResolved {
			t.Errorf("B.swift Foo: unresolved binding; %+v", r.Binding)
		}
		if r.Binding.Decl != bFoo.ID {
			t.Errorf("B.swift Foo: bound to %d, want local Foo %d; reason=%q",
				r.Binding.Decl, bFoo.ID, r.Binding.Reason)
		}
		if r.Binding.Reason == "same_module" {
			t.Errorf("B.swift Foo: local binding rewritten to same_module; %+v",
				r.Binding)
		}
	}
}

// TestImportGraph_Swift_ExternalImportLeftAlone: `import Foundation`
// doesn't correspond to any .swift file in the repo. Refs to Foundation
// stay bound to the local Import decl; no same_module rewrite.
func TestImportGraph_Swift_ExternalImportLeftAlone(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"A.swift": "import Foundation\nlet x = Foundation.self\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "A.swift")
	imp := findDecl(ra, "Foundation")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("A.swift: Foundation import decl missing; got %+v", imp)
	}
	if imp.Exported {
		t.Errorf("A.swift Foundation import: Exported=true, want false")
	}
	refs := refsByName(ra, "Foundation")
	if len(refs) == 0 {
		t.Fatal("A.swift: no refs to Foundation")
	}
	for _, r := range refs {
		if r.Binding.Reason == "same_module" {
			t.Errorf("A.swift: external Foundation ref rewritten; %+v", r.Binding)
		}
	}
}
