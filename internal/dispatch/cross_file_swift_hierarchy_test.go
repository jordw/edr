package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_Swift_HierarchySameFile: same-file class hierarchy.
// `class Bar: Foo` — renaming Foo.area must propagate to Bar.area.
func TestRename_Swift_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.swift": `class Shape {
    func area() -> Int { return 0 }
}

class Circle: Shape {
    override func area() -> Int { return 3 }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.swift:area"},
		map[string]any{"new_name": "computeArea", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.swift"))
	src := string(data)
	if strings.Count(src, "computeArea() -> Int") < 2 {
		t.Errorf("expected both methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "func area() -> Int") {
		t.Errorf("file still contains original area; got:\n%s", src)
	}
}

// TestRename_Swift_HierarchyCrossFileDownWalk: base in one file,
// subclass in another. Renaming the base method propagates to the
// override.
func TestRename_Swift_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.swift": `class Service {
    func run(_ input: String) -> String { return input }
}
`,
		"ServiceImpl.swift": `class ServiceImpl: Service {
    override func run(_ input: String) -> String { return input.uppercased() }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Service.swift:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.swift"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.swift"))
	if !strings.Contains(string(base), "func execute(_ input: String)") {
		t.Errorf("Service.swift did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "func execute(_ input: String)") {
		t.Errorf("ServiceImpl.swift override did not rename: %s", impl)
	}
}

// TestRename_Swift_HierarchyCrossFileUpWalk: rename on the
// subclass should rewrite the base class's method.
func TestRename_Swift_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.swift": `class Service {
    func run(_ input: String) -> String { return input }
}
`,
		"ServiceImpl.swift": `class ServiceImpl: Service {
    override func run(_ input: String) -> String { return input.uppercased() }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"ServiceImpl.swift:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.swift"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.swift"))
	if !strings.Contains(string(impl), "func execute(_ input: String)") {
		t.Errorf("ServiceImpl.swift did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "func execute(_ input: String)") {
		t.Errorf("Service.swift base method did not rename via up-walk: %s", base)
	}
}

// TestRename_Swift_HierarchyExtensionConformance: `extension Foo:
// ProtoA` in a separate file declares additional protocol
// conformance for Foo. Renaming a method on the protocol should
// propagate to Foo's method via the synthetic extension decl.
func TestRename_Swift_HierarchyExtensionConformance(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Cacheable.swift": `protocol Cacheable {
    func cache()
}
`,
		"Foo.swift": `class Foo {
    var data: Int = 0
}
`,
		"FooCacheable.swift": `extension Foo: Cacheable {
    func cache() {
        // implementation
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Cacheable.swift:cache"},
		map[string]any{"new_name": "store", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	proto, _ := os.ReadFile(filepath.Join(dir, "Cacheable.swift"))
	ext, _ := os.ReadFile(filepath.Join(dir, "FooCacheable.swift"))
	if !strings.Contains(string(proto), "func store()") {
		t.Errorf("Cacheable.swift target not renamed: %s", proto)
	}
	if !strings.Contains(string(ext), "func store()") {
		t.Errorf("FooCacheable.swift extension method did not rename via cross-file conformance: %s", ext)
	}
}

// TestRename_Swift_HierarchyMultipleProtocols: `class Foo: A, B`
// — renaming Foo.run propagates up to both A.run and B.run.
func TestRename_Swift_HierarchyMultipleProtocols(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.swift": `protocol A {
    func run()
}
`,
		"B.swift": `protocol B {
    func run()
}
`,
		"Foo.swift": `class Foo: A, B {
    func run() {}
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Foo.swift:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "A.swift"))
	b, _ := os.ReadFile(filepath.Join(dir, "B.swift"))
	foo, _ := os.ReadFile(filepath.Join(dir, "Foo.swift"))
	if !strings.Contains(string(a), "func execute()") {
		t.Errorf("A.swift not renamed via up-walk: %s", a)
	}
	if !strings.Contains(string(b), "func execute()") {
		t.Errorf("B.swift not renamed via up-walk: %s", b)
	}
	if !strings.Contains(string(foo), "func execute()") {
		t.Errorf("Foo.swift target not renamed: %s", foo)
	}
}

// TestRename_Swift_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_Swift_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.swift": `class Alpha {
    func process() -> Int { return 1 }
}
`,
		"b.swift": `class Beta {
    func process() -> Int { return 2 }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.swift:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.swift"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.swift"))
	if !strings.Contains(string(a), "func compute()") {
		t.Errorf("a.swift target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "func process()") {
		t.Errorf("b.swift unrelated class incorrectly rewritten: %s", b)
	}
}
