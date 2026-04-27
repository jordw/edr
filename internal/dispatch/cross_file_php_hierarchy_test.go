package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_PHP_HierarchySameFile: same-file class hierarchy
// regression. Renaming Foo.area must propagate to Bar.area when
// `class Bar extends Foo` lives in the same file.
func TestRename_PHP_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.php": `<?php

class Shape {
    public function area() {
        return 0;
    }
}

class Circle extends Shape {
    public function area() {
        return 3.14;
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.php:area"},
		map[string]any{"new_name": "computeArea", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.php"))
	src := string(data)
	if strings.Count(src, "function computeArea") != 2 {
		t.Errorf("expected both methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "function area(") {
		t.Errorf("file still contains original area(); got:\n%s", src)
	}
}

// TestRename_PHP_HierarchyCrossFileDownWalk: base class in
// Service.php, subclass in ServiceImpl.php. Renaming the base
// method must propagate to the override on the subclass via the
// reverse-reference candidate path.
func TestRename_PHP_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.php": `<?php

class Service {
    public function run($input) {
        return $input;
    }
}
`,
		"ServiceImpl.php": `<?php

require_once 'Service.php';

class ServiceImpl extends Service {
    public function run($input) {
        return strtoupper($input);
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Service.php:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.php"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.php"))
	if !strings.Contains(string(base), "function execute(") {
		t.Errorf("Service.php did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "function execute(") {
		t.Errorf("ServiceImpl.php override did not rename: %s", impl)
	}
}

// TestRename_PHP_HierarchyCrossFileUpWalk: rename on the subclass
// should rewrite the base class's method too via the up-walk.
func TestRename_PHP_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.php": `<?php

class Service {
    public function run($input) {
        return $input;
    }
}
`,
		"ServiceImpl.php": `<?php

require_once 'Service.php';

class ServiceImpl extends Service {
    public function run($input) {
        return strtoupper($input);
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"ServiceImpl.php:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.php"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.php"))
	if !strings.Contains(string(impl), "function execute(") {
		t.Errorf("ServiceImpl.php did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "function execute(") {
		t.Errorf("Service.php base method did not rename via up-walk: %s", base)
	}
}

// TestRename_PHP_HierarchyTraitMixin: `use TraitName;` in a class
// body adds the trait as a hierarchy edge. Renaming a trait method
// propagates to the using-class's override of the same method.
func TestRename_PHP_HierarchyTraitMixin(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"app.php": `<?php

trait Greeter {
    public function hello() {
        return 'hi';
    }
}

class App {
    use Greeter;

    public function hello() {
        return 'App says hi';
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"app.php:hello"},
		map[string]any{"new_name": "greet", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "app.php"))
	src := string(data)
	if strings.Count(src, "function greet(") != 2 {
		t.Errorf("expected both trait and class methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "function hello(") {
		t.Errorf("file still contains original hello(); got:\n%s", src)
	}
}

// TestRename_PHP_HierarchyImplementsInterface: `class Foo
// implements I1` — renaming Foo.run propagates up to I1.run on
// the interface (one hop). Transitive walks (interface → multiple
// implementers in a single rename) are deferred per the
// EmitOverrideSpans v1 contract.
func TestRename_PHP_HierarchyImplementsInterface(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"app.php": `<?php

interface Service {
    public function run();
}

class Foo implements Service {
    public function run() {
        return 1;
    }
    public function configure() {
        return 2;
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"app.php:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "app.php"))
	src := string(data)
	// Service.run + Foo.run → both renamed.
	if strings.Count(src, "function execute(") != 2 {
		t.Errorf("expected 2 execute() methods (Service + Foo); got:\n%s", src)
	}
	// configure() is unrelated — must NOT be touched.
	if !strings.Contains(src, "function configure(") {
		t.Errorf("configure() should not be renamed: %s", src)
	}
}

// TestRename_PHP_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_PHP_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.php": `<?php

class Alpha {
    public function process() {
        return 1;
    }
}
`,
		"b.php": `<?php

class Beta {
    public function process() {
        return 2;
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.php:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.php"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.php"))
	if !strings.Contains(string(a), "function compute(") {
		t.Errorf("a.php target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "function process(") {
		t.Errorf("b.php unrelated class incorrectly rewritten: %s", b)
	}
}
