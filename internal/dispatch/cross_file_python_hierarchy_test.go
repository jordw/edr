package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_Python_HierarchySameFile: same-file class hierarchy
// regression. `class Foo: def m()`, `class Bar(Foo): def m()` —
// rename Foo.m must propagate to Bar.m via the same-file down-walk.
func TestRename_Python_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.py": `class Shape:
    def area(self):
        return 0


class Circle(Shape):
    def area(self):
        return 3.14
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.py:area"},
		map[string]any{"new_name": "compute_area", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.py"))
	src := string(data)
	if strings.Count(src, "def compute_area(self)") != 2 {
		t.Errorf("expected both methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "def area(self)") {
		t.Errorf("file still contains original area; got:\n%s", src)
	}
}

// TestRename_Python_HierarchyCrossFileDownWalk: base class in
// base.py, subclass in impl.py. Renaming the method on the base
// must propagate to the override on the subclass via the
// reverse-reference candidate path.
func TestRename_Python_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"base.py": `class Service:
    def run(self, input):
        return input
`,
		"impl.py": `from base import Service


class ServiceImpl(Service):
    def run(self, input):
        return input.upper()
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"base.py:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "base.py"))
	impl, _ := os.ReadFile(filepath.Join(dir, "impl.py"))
	if !strings.Contains(string(base), "def execute(self, input)") {
		t.Errorf("base.py did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "def execute(self, input)") {
		t.Errorf("impl.py override did not rename: %s", impl)
	}
	if strings.Contains(string(impl), "def run(self") {
		t.Errorf("impl.py still contains old run(): %s", impl)
	}
}

// TestRename_Python_HierarchyCrossFileUpWalk: rename on the subclass
// should rewrite the base class's method too via the up-walk through
// the import graph.
func TestRename_Python_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"base.py": `class Service:
    def run(self, input):
        return input
`,
		"impl.py": `from base import Service


class ServiceImpl(Service):
    def run(self, input):
        return input.upper()
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"impl.py:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "base.py"))
	impl, _ := os.ReadFile(filepath.Join(dir, "impl.py"))
	if !strings.Contains(string(impl), "def execute(self, input)") {
		t.Errorf("impl.py did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "def execute(self, input)") {
		t.Errorf("base.py base method did not rename via up-walk: %s", base)
	}
}

// TestRename_Python_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target rewrites.
func TestRename_Python_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.py": `class Alpha:
    def process(self):
        return 1
`,
		"b.py": `class Beta:
    def process(self):
        return 2
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.py:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.py"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.py"))
	if !strings.Contains(string(a), "def compute(self)") {
		t.Errorf("a.py target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "def process(self)") {
		t.Errorf("b.py unrelated class incorrectly rewritten: %s", b)
	}
}

// TestRename_Python_HierarchyMetaclassNotABase: the `metaclass=Meta`
// kwarg in the class header should NOT be treated as a base —
// renaming a method on Meta must not touch Foo's method.
func TestRename_Python_HierarchyMetaclassNotABase(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.py": `class Meta:
    def configure(self):
        return None


class Foo(metaclass=Meta):
    def configure(self):
        return 1
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.py:configure"},
		map[string]any{"new_name": "setup", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.py"))
	src := string(data)
	// Only Meta.configure should be renamed (the target).
	// Foo.configure must remain because metaclass= is NOT a base
	// relation; the hierarchy walker must skip it.
	if !strings.Contains(src, "def setup(self)") {
		t.Errorf("Meta.configure not renamed: %s", src)
	}
	if !strings.Contains(src, "def configure(self):\n        return 1") {
		t.Errorf("Foo.configure should NOT be renamed (metaclass != base); got:\n%s", src)
	}
}
