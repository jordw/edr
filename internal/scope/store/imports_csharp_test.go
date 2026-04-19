package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_CSharp_UsingAlias: `using X = Foo.Bar;` in b.cs
// rewrites refs to X so they bind to Bar's DeclID in a.cs.
func TestImportGraph_CSharp_UsingAlias(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.cs": `namespace Foo {
    public class Bar {}
}
`,
		"b.cs": `using X = Foo.Bar;

namespace App {
    public class Use {
        X item;
    }
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
	ra := idx.ResultFor(root, "a.cs")
	rb := idx.ResultFor(root, "b.cs")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	// a.cs: the exported Foo.Bar decl.
	aBar := findDeclKindCS(ra, "Bar", scope.KindClass)
	if aBar == nil {
		t.Fatalf("a.cs: no Bar class decl; decls=%v", declNamesCS(ra))
	}
	if !aBar.Exported {
		t.Errorf("a.cs Bar: Exported=false, want true")
	}
	// b.cs: refs named X should resolve to aBar.ID.
	refs := refsByNameCS(rb, "X")
	if len(refs) == 0 {
		t.Fatal("b.cs: no refs to X")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aBar.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("X ref reason=%q, want import_export", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("b.cs: X not rewritten to a.cs Bar (ID %d); bindings=%v",
			aBar.ID, bindingsForCS(refs))
	}
}

// TestImportGraph_CSharp_UsingNamespace: `using Foo;` in b.cs
// resolves refs to Bar (public type in Foo) without full
// qualification.
func TestImportGraph_CSharp_UsingNamespace(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.cs": `namespace Foo {
    public class Bar {}
}
`,
		"b.cs": `using Foo;

namespace App {
    public class Use {
        Bar item;
    }
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
	ra := idx.ResultFor(root, "a.cs")
	rb := idx.ResultFor(root, "b.cs")
	aBar := findDeclKindCS(ra, "Bar", scope.KindClass)
	if aBar == nil {
		t.Fatalf("a.cs: no Bar class decl")
	}

	refs := refsByNameCS(rb, "Bar")
	if len(refs) == 0 {
		t.Fatal("b.cs: no refs to Bar")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aBar.ID &&
			r.Binding.Reason == "import_export" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("b.cs: Bar not rewritten to a.cs Foo.Bar (ID %d); bindings=%v",
			aBar.ID, bindingsForCS(refs))
	}
}

// TestImportGraph_CSharp_InternalStaysUnexported: Phase-1 treats
// `internal` as not-exported at repo scope. A ref to an internal type
// via `using Foo;` stays unresolved by the import-graph (the
// conservative-correct answer — we can't prove the target is visible
// without assembly analysis). Documents the behavior.
func TestImportGraph_CSharp_InternalStaysUnexported(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.cs": `namespace Foo {
    internal class Secret {}
}
`,
		"b.cs": `using Foo;

namespace App {
    public class Use {
        Secret item;
    }
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
	ra := idx.ResultFor(root, "a.cs")
	rb := idx.ResultFor(root, "b.cs")
	aSecret := findDeclKindCS(ra, "Secret", scope.KindClass)
	if aSecret == nil {
		t.Fatalf("a.cs: no Secret decl")
	}
	if aSecret.Exported {
		t.Errorf("a.cs Secret: Exported=true, want false (internal)")
	}
	// Phase-1: refs to Secret in b.cs do NOT resolve to aSecret.ID via
	// the import graph. They may still resolve via other mechanisms
	// (e.g. none applicable here); what we assert is that nothing
	// incorrectly claimed "import_export" as the reason.
	for _, r := range refsByNameCS(rb, "Secret") {
		if r.Binding.Reason == "import_export" && r.Binding.Decl == aSecret.ID {
			t.Errorf("b.cs: internal Secret should not be rewritten by import graph; got binding %+v", r.Binding)
		}
	}
}

// TestImportGraph_CSharp_PrivateStaysLocal: a `private` nested class
// in a.cs is never visible to b.cs via imports. Refs in b.cs to a
// same-named type stay unresolved (or resolve to whatever local
// shadow exists).
func TestImportGraph_CSharp_PrivateStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.cs": `namespace Foo {
    public class Outer {
        private class Hidden {}
    }
}
`,
		"b.cs": `using Foo;

namespace App {
    public class Use {
        Hidden item;
    }
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
	ra := idx.ResultFor(root, "a.cs")
	rb := idx.ResultFor(root, "b.cs")
	if ra == nil || rb == nil {
		t.Fatal("missing results")
	}
	// Hidden is nested (not top-level), so it's never in the FQN
	// index regardless of modifier. b.cs's ref to Hidden must not
	// bind via import_export.
	for _, r := range refsByNameCS(rb, "Hidden") {
		if r.Binding.Reason == "import_export" {
			t.Errorf("b.cs: nested private Hidden must not be rewritten; got %+v", r.Binding)
		}
	}
}

// TestImportGraph_CSharp_ExternalLeftAsIs: `using System;` references
// a namespace with no repo-internal source file. Refs like `Console`
// stay unresolved (honest external answer) — the resolver does not
// invent bindings for them.
func TestImportGraph_CSharp_ExternalLeftAsIs(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.cs": `using System;

namespace App {
    public class Prog {
        public void M() {
            Console.WriteLine("hi");
        }
    }
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
	ra := idx.ResultFor(root, "a.cs")
	if ra == nil {
		t.Fatal("missing result")
	}
	for _, r := range refsByNameCS(ra, "Console") {
		if r.Binding.Reason == "import_export" {
			t.Errorf("Console: must not be rewritten (external); got %+v", r.Binding)
		}
	}
}

// --- helpers (kept local to avoid clashing with the TS-focused
//     helpers in imports_test.go while making failure output readable).

func findDeclKindCS(r *scope.Result, name string, kind scope.DeclKind) *scope.Decl {
	for i := range r.Decls {
		if r.Decls[i].Name == name && r.Decls[i].Kind == kind {
			return &r.Decls[i]
		}
	}
	return nil
}

func refsByNameCS(r *scope.Result, name string) []scope.Ref {
	var out []scope.Ref
	for _, ref := range r.Refs {
		if ref.Name == name {
			out = append(out, ref)
		}
	}
	return out
}

func declNamesCS(r *scope.Result) []string {
	out := make([]string, 0, len(r.Decls))
	for _, d := range r.Decls {
		out = append(out, d.Name)
	}
	return out
}

func bindingsForCS(refs []scope.Ref) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Binding.Reason)
	}
	return out
}
