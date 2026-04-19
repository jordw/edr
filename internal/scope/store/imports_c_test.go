package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_C_HeaderIncludeResolves: `#include "foo.h"` in a.c
// and foo.h declares `int bar(...)`; a ref to bar in a.c that would
// otherwise be BindUnresolved resolves to foo.h's bar decl with
// Reason="include_resolution".
func TestImportGraph_C_HeaderIncludeResolves(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.h": "int bar(int x);\n",
		"a.c": `#include "foo.h"
void use(void) {
	bar(1);
}
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rFoo := idx.ResultFor(root, "foo.h")
	rA := idx.ResultFor(root, "a.c")
	if rFoo == nil || rA == nil {
		t.Fatalf("ResultFor nil: foo.h=%v a.c=%v", rFoo, rA)
	}
	fooBar := findDecl(rFoo, "bar")
	if fooBar == nil {
		t.Fatalf("foo.h: no bar decl")
	}
	if !fooBar.Exported {
		t.Errorf("foo.h bar: Exported=false, want true")
	}
	refs := refsByName(rA, "bar")
	if len(refs) == 0 {
		t.Fatal("a.c: no refs to bar")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == fooBar.ID {
			found = true
			if r.Binding.Reason != "include_resolution" {
				t.Errorf("a.c bar ref reason = %q, want \"include_resolution\"", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("a.c: no ref to bar resolved to foo.h's DeclID %d; got bindings=%v",
			fooBar.ID, bindingsFor(refs))
	}
}

// TestImportGraph_C_StaticStaysLocal: a `static` function in a.c is
// not Exported, so when b.c `#include`s a.h (which doesn't declare
// the static helper), refs to that helper in b.c remain unresolved
// — they MUST NOT accidentally bind across files.
func TestImportGraph_C_StaticStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.h": "/* no decl for helper */\n",
		"a.c": `#include "a.h"
static int helper(int x) { return x + 1; }
int use(void) { return helper(2); }
`,
		"b.c": `#include "a.h"
int other(void) { return helper(3); }
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rA := idx.ResultFor(root, "a.c")
	rB := idx.ResultFor(root, "b.c")
	if rA == nil || rB == nil {
		t.Fatalf("ResultFor nil: a.c=%v b.c=%v", rA, rB)
	}

	// (1) a.c's helper is not Exported.
	aHelper := findDecl(rA, "helper")
	if aHelper == nil {
		t.Fatalf("a.c: no helper decl")
	}
	if aHelper.Exported {
		t.Errorf("a.c static helper: Exported=true, want false")
	}

	// (2) In b.c, refs to `helper` must NOT be resolved to a.c's
	// helper decl via include_resolution (a.h doesn't re-declare it
	// and helper is static). The binding stays BindUnresolved.
	refs := refsByName(rB, "helper")
	if len(refs) == 0 {
		t.Fatal("b.c: no refs to helper")
	}
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved &&
			r.Binding.Reason == "include_resolution" {
			t.Errorf("b.c helper ref resolved via include_resolution to decl %d; want BindUnresolved (static helper is internal to a.c)",
				r.Binding.Decl)
		}
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aHelper.ID {
			t.Errorf("b.c helper ref bound to a.c's static helper (DeclID %d); static decls must not leak across translation units",
				aHelper.ID)
		}
	}
}

// TestImportGraph_C_SystemIncludeStaysLocal: `#include <stdio.h>` in
// a.c does not resolve against any header in the repo (even if a
// stdio.h happens to exist), so calls like printf stay unresolved.
// The resolver must skip angle-bracket includes.
func TestImportGraph_C_SystemIncludeStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		// A repo-local stdio.h with a decl for printf — this is an
		// attempt to "trick" a naive resolver into binding across
		// system-include boundaries.
		"stdio.h": "int printf(const char *fmt);\n",
		"a.c": `#include <stdio.h>
void use(void) {
	printf("hi");
}
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rA := idx.ResultFor(root, "a.c")
	if rA == nil {
		t.Fatalf("ResultFor nil: a.c")
	}
	refs := refsByName(rA, "printf")
	if len(refs) == 0 {
		t.Fatal("a.c: no refs to printf")
	}
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved &&
			r.Binding.Reason == "include_resolution" {
			t.Errorf("a.c printf ref resolved via include_resolution; want unresolved (system include must not pull from repo)")
		}
	}
}
