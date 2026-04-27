package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_Java_HierarchySameFile: same-file class hierarchy.
// `class Bar extends Foo` — renaming Foo.area must propagate to
// Bar.area.
func TestRename_Java_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Shapes.java": `package com.example;

public class Shape {
    public int area() { return 0; }
}

class Circle extends Shape {
    public int area() { return 3; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Shapes.java:area"},
		map[string]any{"new_name": "computeArea", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "Shapes.java"))
	src := string(data)
	if got := strings.Count(src, "computeArea()"); got != 2 {
		t.Errorf("expected exactly 2 methods renamed, got %d:\n%s", got, src)
	}
	if strings.Contains(src, "int area()") {
		t.Errorf("file still contains original area; got:\n%s", src)
	}
}

// TestRename_Java_HierarchyCrossFileDownWalk: base class in
// Service.java, subclass in ServiceImpl.java in the same package.
// Renaming the base method propagates to the override.
func TestRename_Java_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/Service.java": `package com.example;

public class Service {
    public String run(String input) { return input; }
}
`,
		"com/example/ServiceImpl.java": `package com.example;

public class ServiceImpl extends Service {
    @Override
    public String run(String input) { return input.toUpperCase(); }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Service.java:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "com/example/Service.java"))
	impl, _ := os.ReadFile(filepath.Join(dir, "com/example/ServiceImpl.java"))
	if !strings.Contains(string(base), "execute(String input)") {
		t.Errorf("Service.java did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "execute(String input)") {
		t.Errorf("ServiceImpl.java override did not rename: %s", impl)
	}
}

// TestRename_Java_HierarchyCrossFileUpWalk: rename on the subclass
// should rewrite the base class's method.
func TestRename_Java_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/Service.java": `package com.example;

public class Service {
    public String run(String input) { return input; }
}
`,
		"com/example/ServiceImpl.java": `package com.example;

public class ServiceImpl extends Service {
    @Override
    public String run(String input) { return input.toUpperCase(); }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/ServiceImpl.java:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "com/example/Service.java"))
	impl, _ := os.ReadFile(filepath.Join(dir, "com/example/ServiceImpl.java"))
	if !strings.Contains(string(impl), "execute(String input)") {
		t.Errorf("ServiceImpl.java did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "execute(String input)") {
		t.Errorf("Service.java base method did not rename via up-walk: %s", base)
	}
}

// TestRename_Java_HierarchyImplementsInterface: `class Foo
// implements IService` — renaming Foo.run propagates up to
// IService.run on the interface.
func TestRename_Java_HierarchyImplementsInterface(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/IService.java": `package com.example;

public interface IService {
    String run();
}
`,
		"com/example/Foo.java": `package com.example;

public class Foo implements IService {
    @Override
    public String run() { return "foo"; }
    public String configure() { return "cfg"; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Foo.java:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	iface, _ := os.ReadFile(filepath.Join(dir, "com/example/IService.java"))
	foo, _ := os.ReadFile(filepath.Join(dir, "com/example/Foo.java"))
	if !strings.Contains(string(iface), "String execute()") {
		t.Errorf("IService.java not renamed via up-walk: %s", iface)
	}
	if !strings.Contains(string(foo), "String execute()") {
		t.Errorf("Foo.java target not renamed: %s", foo)
	}
	if !strings.Contains(string(foo), "configure()") {
		t.Errorf("Foo.configure should not be renamed: %s", foo)
	}
}

// TestRename_Java_HierarchyMultipleInterfaces: `class Foo
// implements IA, IB` — renaming Foo.run propagates up to both
// IA.run and IB.run.
func TestRename_Java_HierarchyMultipleInterfaces(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/IA.java": `package com.example;
public interface IA {
    void run();
}
`,
		"com/example/IB.java": `package com.example;
public interface IB {
    void run();
}
`,
		"com/example/Foo.java": `package com.example;

public class Foo implements IA, IB {
    @Override
    public void run() {}
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Foo.java:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "com/example/IA.java"))
	b, _ := os.ReadFile(filepath.Join(dir, "com/example/IB.java"))
	foo, _ := os.ReadFile(filepath.Join(dir, "com/example/Foo.java"))
	if !strings.Contains(string(a), "void execute()") {
		t.Errorf("IA.java not renamed via up-walk: %s", a)
	}
	if !strings.Contains(string(b), "void execute()") {
		t.Errorf("IB.java not renamed via up-walk: %s", b)
	}
	if !strings.Contains(string(foo), "void execute()") {
		t.Errorf("Foo.java target not renamed: %s", foo)
	}
}

// TestRename_Java_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_Java_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/Alpha.java": `package com.example;
public class Alpha {
    public int process() { return 1; }
}
`,
		"com/example/Beta.java": `package com.example;
public class Beta {
    public int process() { return 2; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Alpha.java:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "com/example/Alpha.java"))
	b, _ := os.ReadFile(filepath.Join(dir, "com/example/Beta.java"))
	if !strings.Contains(string(a), "compute()") {
		t.Errorf("Alpha.java target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "process()") {
		t.Errorf("Beta.java unrelated class incorrectly rewritten: %s", b)
	}
}
