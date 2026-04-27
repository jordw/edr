package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_Kotlin_HierarchySameFile: same-file class hierarchy.
// `class Bar : Foo()` — renaming Foo.area must propagate to Bar.area.
func TestRename_Kotlin_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.kt": `package com.example

open class Shape {
    open fun area(): Int = 0
}

class Circle : Shape() {
    override fun area(): Int = 3
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.kt:area"},
		map[string]any{"new_name": "computeArea", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.kt"))
	src := string(data)
	if strings.Count(src, "computeArea(): Int") < 2 {
		t.Errorf("expected both methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "fun area(): Int") {
		t.Errorf("file still contains original area; got:\n%s", src)
	}
}

// TestRename_Kotlin_HierarchyCrossFileDownWalk: base class in one
// file, subclass in another. Renaming the base method must
// propagate to the override on the subclass.
func TestRename_Kotlin_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/Service.kt": `package com.example

open class Service {
    open fun run(input: String): String = input
}
`,
		"com/example/ServiceImpl.kt": `package com.example

class ServiceImpl : Service() {
    override fun run(input: String): String = input.uppercase()
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Service.kt:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "com/example/Service.kt"))
	impl, _ := os.ReadFile(filepath.Join(dir, "com/example/ServiceImpl.kt"))
	if !strings.Contains(string(base), "fun execute(input: String)") {
		t.Errorf("Service.kt did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "fun execute(input: String)") {
		t.Errorf("ServiceImpl.kt override did not rename: %s", impl)
	}
}

// TestRename_Kotlin_HierarchyCrossFileUpWalk: rename on the
// subclass should rewrite the base class's method.
func TestRename_Kotlin_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/Service.kt": `package com.example

open class Service {
    open fun run(input: String): String = input
}
`,
		"com/example/ServiceImpl.kt": `package com.example

class ServiceImpl : Service() {
    override fun run(input: String): String = input.uppercase()
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/ServiceImpl.kt:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "com/example/Service.kt"))
	impl, _ := os.ReadFile(filepath.Join(dir, "com/example/ServiceImpl.kt"))
	if !strings.Contains(string(impl), "fun execute(input: String)") {
		t.Errorf("ServiceImpl.kt did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "fun execute(input: String)") {
		t.Errorf("Service.kt base method did not rename via up-walk: %s", base)
	}
}

// TestRename_Kotlin_HierarchyMultipleInterfaces: `class Foo : IA, IB`
// — renaming a method on Foo propagates up to interfaces it
// implements when those interfaces declare same-named methods.
func TestRename_Kotlin_HierarchyMultipleInterfaces(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/IA.kt": `package com.example
interface IA {
    fun run(): Int
}
`,
		"com/example/IB.kt": `package com.example
interface IB {
    fun run(): Int
}
`,
		"com/example/Foo.kt": `package com.example

class Foo : IA, IB {
    override fun run(): Int = 1
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Foo.kt:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "com/example/IA.kt"))
	b, _ := os.ReadFile(filepath.Join(dir, "com/example/IB.kt"))
	foo, _ := os.ReadFile(filepath.Join(dir, "com/example/Foo.kt"))
	if !strings.Contains(string(a), "fun execute()") {
		t.Errorf("IA.kt.run not renamed via up-walk: %s", a)
	}
	if !strings.Contains(string(b), "fun execute()") {
		t.Errorf("IB.kt.run not renamed via up-walk: %s", b)
	}
	if !strings.Contains(string(foo), "fun execute()") {
		t.Errorf("Foo.kt target not renamed: %s", foo)
	}
}

// TestRename_Kotlin_HierarchyObjectExtendsBase: `object Singleton :
// Base()` — renaming a method on the singleton propagates to Base.
func TestRename_Kotlin_HierarchyObjectExtendsBase(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"app.kt": `package com.example

open class Base {
    open fun work(): Int = 0
}

object Singleton : Base() {
    override fun work(): Int = 42
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"app.kt:work"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "app.kt"))
	src := string(data)
	if strings.Count(src, "fun execute(): Int") < 2 {
		t.Errorf("expected both Base + Singleton renamed; got:\n%s", src)
	}
}

// TestRename_Kotlin_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_Kotlin_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/Alpha.kt": `package com.example
class Alpha {
    fun process(): Int = 1
}
`,
		"com/example/Beta.kt": `package com.example
class Beta {
    fun process(): Int = 2
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/Alpha.kt:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "com/example/Alpha.kt"))
	b, _ := os.ReadFile(filepath.Join(dir, "com/example/Beta.kt"))
	if !strings.Contains(string(a), "fun compute()") {
		t.Errorf("Alpha.kt target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "fun process()") {
		t.Errorf("Beta.kt unrelated class incorrectly rewritten: %s", b)
	}
}
