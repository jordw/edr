package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_PHP_CrossFileClass: `use Foo\Bar;` resolves to the
// `class Bar` decl in the file that declares `namespace Foo;`.
func TestImportGraph_PHP_CrossFileClass(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"Bar.php": `<?php
namespace Foo;
class Bar {
    public function hello() {}
}
?>`,
		"main.php": `<?php
use Foo\Bar;
$x = new Bar();
?>`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "Bar.php")
	rb := idx.ResultFor(root, "main.php")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: Bar.php=%v main.php=%v", ra, rb)
	}
	// Find the class Bar decl (NSValue variant, the one not in NSType).
	var aBar *scope.Decl
	for i := range ra.Decls {
		d := &ra.Decls[i]
		if d.Name == "Bar" && d.Kind == scope.KindClass && d.Namespace == scope.NSValue {
			aBar = d
			break
		}
	}
	if aBar == nil {
		t.Fatalf("Bar.php: no class Bar (value namespace); decls=%v",
			declKindNames(ra))
	}
	if !aBar.Exported {
		t.Errorf("Bar.php class Bar: Exported=false, want true")
	}
	// Find the local Import decl for Bar in main.php.
	var bImport *scope.Decl
	for i := range rb.Decls {
		d := &rb.Decls[i]
		if d.Name == "Bar" && d.Kind == scope.KindImport {
			bImport = d
			break
		}
	}
	if bImport == nil {
		t.Fatalf("main.php: no Bar KindImport decl; decls=%v",
			declKindNames(rb))
	}
	// `new Bar()` ref should rewrite to Bar.php's class Bar.
	refs := refsByName(rb, "Bar")
	if len(refs) == 0 {
		t.Fatal("main.php: no refs to Bar")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aBar.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("main.php Bar ref reason = %q, want \"import_export\"",
					r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("main.php: no ref to Bar rewrote to Bar.php's class Bar (ID %d); bindings=%v",
			aBar.ID, bindingsFor(refs))
	}
}

// TestImportGraph_PHP_AliasedUse: `use Foo\Bar as B;` rewrites refs to
// `B` against the class Bar's DeclID.
func TestImportGraph_PHP_AliasedUse(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"Bar.php": `<?php
namespace Foo;
class Bar {}
?>`,
		"main.php": `<?php
use Foo\Bar as B;
$x = new B();
?>`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "Bar.php")
	rb := idx.ResultFor(root, "main.php")
	var aBar *scope.Decl
	for i := range ra.Decls {
		d := &ra.Decls[i]
		if d.Name == "Bar" && d.Kind == scope.KindClass && d.Namespace == scope.NSValue {
			aBar = d
			break
		}
	}
	if aBar == nil {
		t.Fatalf("Bar.php: no class Bar; decls=%v", declKindNames(ra))
	}
	refs := refsByName(rb, "B")
	if len(refs) == 0 {
		t.Fatal("main.php: no refs to B")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aBar.ID &&
			r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("main.php: alias B did not resolve to Bar.php class Bar (ID %d); bindings=%v",
			aBar.ID, bindingsFor(refs))
	}
}

// TestImportGraph_PHP_NamespaceCaptured: a file with `namespace A\B;`
// indexes its `class Foo` at FQN "A\\B\\Foo", so an importer with
// `use A\B\Foo;` resolves to it.
func TestImportGraph_PHP_NamespaceCaptured(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"Foo.php": `<?php
namespace A\B;
class Foo {}
?>`,
		"main.php": `<?php
use A\B\Foo;
$x = new Foo();
?>`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "Foo.php")
	rb := idx.ResultFor(root, "main.php")
	var aFoo *scope.Decl
	for i := range ra.Decls {
		d := &ra.Decls[i]
		if d.Name == "Foo" && d.Kind == scope.KindClass && d.Namespace == scope.NSValue {
			aFoo = d
			break
		}
	}
	if aFoo == nil {
		t.Fatalf("Foo.php: no class Foo; decls=%v", declKindNames(ra))
	}
	refs := refsByName(rb, "Foo")
	if len(refs) == 0 {
		t.Fatal("main.php: no refs to Foo")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID &&
			r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("main.php: Foo ref did not resolve to A\\B\\Foo (ID %d); bindings=%v",
			aFoo.ID, bindingsFor(refs))
	}
}

// TestImportGraph_PHP_ExternalNamespaceStaysLocal: `use Vendor\Library\X;`
// with no repo-internal file declaring that namespace; the ref must stay
// bound to the local Import decl (honest "external" answer).
func TestImportGraph_PHP_ExternalNamespaceStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"main.php": `<?php
use Vendor\Library\X;
$x = new X();
?>`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rb := idx.ResultFor(root, "main.php")
	var bImport *scope.Decl
	for i := range rb.Decls {
		d := &rb.Decls[i]
		if d.Name == "X" && d.Kind == scope.KindImport {
			bImport = d
			break
		}
	}
	if bImport == nil {
		t.Fatalf("main.php: no X KindImport decl; decls=%v", declKindNames(rb))
	}
	refs := refsByName(rb, "X")
	if len(refs) == 0 {
		t.Fatal("main.php: no refs to X")
	}
	for _, r := range refs {
		if r.Binding.Kind != scope.BindResolved {
			continue
		}
		if r.Binding.Decl != bImport.ID {
			t.Errorf("external import X was rewritten: binding=%+v, want decl=%d (local import)",
				r.Binding, bImport.ID)
		}
		if r.Binding.Reason == "import_export" {
			t.Errorf("external import X must not have reason=import_export: %+v", r.Binding)
		}
	}
}

// declKindNames formats decls for test error messages.
func declKindNames(r *scope.Result) []string {
	out := make([]string, 0, len(r.Decls))
	for _, d := range r.Decls {
		out = append(out, string(d.Kind)+":"+d.Name+"/"+string(d.Namespace))
	}
	return out
}
