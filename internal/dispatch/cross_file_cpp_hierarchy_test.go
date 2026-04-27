package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_Cpp_HierarchySameFile: same-file class hierarchy
// regression. `class Bar : public Foo` — renaming Foo::area must
// propagate to Bar::area.
func TestRename_Cpp_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.hpp": `class Shape {
public:
    int area() { return 0; }
};

class Circle : public Shape {
public:
    int area() { return 3; }
};
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.hpp:area"},
		map[string]any{"new_name": "computeArea", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.hpp"))
	src := string(data)
	if strings.Count(src, "computeArea()") < 2 {
		t.Errorf("expected both methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "int area()") {
		t.Errorf("file still contains original area; got:\n%s", src)
	}
}

// TestRename_Cpp_HierarchyCrossFileHeader: base class in Service.hpp,
// subclass in ServiceImpl.hpp (sibling header). Renaming the base
// method must propagate to the override on the subclass.
func TestRename_Cpp_HierarchyCrossFileHeader(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.hpp": `class Service {
public:
    int run() { return 0; }
};
`,
		"ServiceImpl.hpp": `#include "Service.hpp"

class ServiceImpl : public Service {
public:
    int run() { return 1; }
};
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Service.hpp:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.hpp"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.hpp"))
	if !strings.Contains(string(base), "execute()") {
		t.Errorf("Service.hpp did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "execute()") {
		t.Errorf("ServiceImpl.hpp override did not rename: %s", impl)
	}
}

// TestRename_Cpp_HierarchyCrossFileUpWalk: rename on the subclass
// propagates to the base class via the up-walk through the include
// graph.
func TestRename_Cpp_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.hpp": `class Service {
public:
    int run() { return 0; }
};
`,
		"ServiceImpl.hpp": `#include "Service.hpp"

class ServiceImpl : public Service {
public:
    int run() { return 1; }
};
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"ServiceImpl.hpp:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.hpp"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.hpp"))
	if !strings.Contains(string(impl), "execute()") {
		t.Errorf("ServiceImpl.hpp did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "execute()") {
		t.Errorf("Service.hpp base method did not rename via up-walk: %s", base)
	}
}

// TestRename_Cpp_HierarchyMultipleInheritance: `class Foo : public
// A, public B` — renaming Foo's method should propagate up to both
// A and B. The base classes live in sibling headers so the rename
// target unambiguously resolves to Foo's `run`.
func TestRename_Cpp_HierarchyMultipleInheritance(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.hpp": `class A {
public:
    int run() { return 0; }
};
`,
		"B.hpp": `class B {
public:
    int run() { return 1; }
};
`,
		"Foo.hpp": `#include "A.hpp"
#include "B.hpp"

class Foo : public A, public B {
public:
    int run() { return 2; }
};
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Foo.hpp:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "A.hpp"))
	b, _ := os.ReadFile(filepath.Join(dir, "B.hpp"))
	foo, _ := os.ReadFile(filepath.Join(dir, "Foo.hpp"))
	if !strings.Contains(string(a), "execute()") {
		t.Errorf("A.hpp.run not renamed via up-walk: %s", a)
	}
	if !strings.Contains(string(b), "execute()") {
		t.Errorf("B.hpp.run not renamed via up-walk: %s", b)
	}
	if !strings.Contains(string(foo), "execute()") {
		t.Errorf("Foo.hpp target not renamed: %s", foo)
	}
}

// TestRename_Cpp_HierarchyVirtualInheritance: `class Foo : virtual
// public Base` — virtual inheritance still creates a hierarchy
// edge (the virtual modifier is for diamond resolution, not for
// override semantics).
func TestRename_Cpp_HierarchyVirtualInheritance(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"app.hpp": `class Base {
public:
    int run() { return 0; }
};

class Foo : virtual public Base {
public:
    int run() { return 1; }
};
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"app.hpp:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "app.hpp"))
	src := string(data)
	if strings.Count(src, "execute()") < 2 {
		t.Errorf("expected Base + Foo renamed; got:\n%s", src)
	}
}

// TestRename_Cpp_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_Cpp_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.hpp": `class Alpha {
public:
    int process() { return 1; }
};
`,
		"b.hpp": `class Beta {
public:
    int process() { return 2; }
};
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.hpp:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.hpp"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.hpp"))
	if !strings.Contains(string(a), "compute()") {
		t.Errorf("a.hpp target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "process()") {
		t.Errorf("b.hpp unrelated class incorrectly rewritten: %s", b)
	}
}
