package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_CSharp_HierarchySameFile: same-file class hierarchy.
// `class Bar : Foo` — renaming Foo.Area must propagate to Bar.Area.
func TestRename_CSharp_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Shapes.cs": `public class Shape {
    public int Area() { return 0; }
}

public class Circle : Shape {
    public int Area() { return 3; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Shapes.cs:Area"},
		map[string]any{"new_name": "ComputeArea", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "Shapes.cs"))
	src := string(data)
	if got := strings.Count(src, "ComputeArea()"); got != 2 {
		t.Errorf("expected exactly 2 methods renamed, got %d:\n%s", got, src)
	}
	if strings.Contains(src, "int Area()") {
		t.Errorf("file still contains original Area; got:\n%s", src)
	}
}

// TestRename_CSharp_HierarchyCrossFileDownWalk: base class in
// Service.cs, subclass in ServiceImpl.cs. Renaming the base
// method must rewrite the override on the subclass via the
// reverse-reference candidate path.
func TestRename_CSharp_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.cs": `public class Service {
    public string Run(string input) { return input; }
}
`,
		"ServiceImpl.cs": `public class ServiceImpl : Service {
    public string Run(string input) { return input.ToUpper(); }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Service.cs:Run"},
		map[string]any{"new_name": "Execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.cs"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.cs"))
	if !strings.Contains(string(base), "Execute(string input)") {
		t.Errorf("Service.cs did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "Execute(string input)") {
		t.Errorf("ServiceImpl.cs override did not rename: %s", impl)
	}
}

// TestRename_CSharp_HierarchyCrossFileUpWalk: rename on the
// subclass should rewrite the base class's method.
func TestRename_CSharp_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Service.cs": `public class Service {
    public string Run(string input) { return input; }
}
`,
		"ServiceImpl.cs": `public class ServiceImpl : Service {
    public string Run(string input) { return input.ToUpper(); }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"ServiceImpl.cs:Run"},
		map[string]any{"new_name": "Execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Service.cs"))
	impl, _ := os.ReadFile(filepath.Join(dir, "ServiceImpl.cs"))
	if !strings.Contains(string(impl), "Execute(string input)") {
		t.Errorf("ServiceImpl.cs did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "Execute(string input)") {
		t.Errorf("Service.cs base method did not rename via up-walk: %s", base)
	}
}

// TestRename_CSharp_HierarchyPartialClass: `partial class Foo`
// declarations in two files. Each part can declare its own bases
// (must be consistent in real C#). Renaming a method on one part
// should propagate to the supertype declared in the other part.
func TestRename_CSharp_HierarchyPartialClass(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Base.cs": `public class Base {
    public string Process() { return "base"; }
}
`,
		"PartialA.cs": `public partial class App : Base {
    public string Process() { return "app"; }
}
`,
		"PartialB.cs": `public partial class App {
    public string Helper() { return "helper"; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"PartialA.cs:Process"},
		map[string]any{"new_name": "Run", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "Base.cs"))
	a, _ := os.ReadFile(filepath.Join(dir, "PartialA.cs"))
	b, _ := os.ReadFile(filepath.Join(dir, "PartialB.cs"))
	if !strings.Contains(string(base), "Run()") {
		t.Errorf("Base.cs did not rename via up-walk: %s", base)
	}
	if !strings.Contains(string(a), "Run()") {
		t.Errorf("PartialA.cs target not renamed: %s", a)
	}
	// Helper in PartialB.cs is unrelated and must NOT be touched.
	if !strings.Contains(string(b), "Helper()") {
		t.Errorf("PartialB.cs Helper() incorrectly rewritten: %s", b)
	}
}

// TestRename_CSharp_HierarchyImplementsInterface: `class Foo :
// IService` — renaming Foo.Run propagates up to IService.Run.
func TestRename_CSharp_HierarchyImplementsInterface(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"App.cs": `public interface IService {
    string Run();
}

public class Foo : IService {
    public string Run() { return "foo"; }
    public string Configure() { return "cfg"; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"App.cs:Run"},
		map[string]any{"new_name": "Execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "App.cs"))
	src := string(data)
	if got := strings.Count(src, "Execute()"); got != 2 {
		t.Errorf("expected exactly 2 (IService.Run + Foo.Run) renamed, got %d:\n%s", got, src)
	}
	if !strings.Contains(src, "Configure()") {
		t.Errorf("Configure() should not be renamed: %s", src)
	}
}

// TestRename_CSharp_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_CSharp_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.cs": `public class Alpha {
    public int Process() { return 1; }
}
`,
		"B.cs": `public class Beta {
    public int Process() { return 2; }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"A.cs:Process"},
		map[string]any{"new_name": "Compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "A.cs"))
	b, _ := os.ReadFile(filepath.Join(dir, "B.cs"))
	if !strings.Contains(string(a), "Compute()") {
		t.Errorf("A.cs target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "Process()") {
		t.Errorf("B.cs unrelated class incorrectly rewritten: %s", b)
	}
}
