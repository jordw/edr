package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
)

// TestRename_Ruby_HierarchySameFile: same-file class hierarchy
// regression with `class Bar < Foo`. Renaming Foo#process must
// propagate to Bar#process.
func TestRename_Ruby_HierarchySameFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"shapes.rb": `class Shape
  def area
    0
  end
end

class Circle < Shape
  def area
    3.14
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"shapes.rb:area"},
		map[string]any{"new_name": "compute_area", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shapes.rb"))
	src := string(data)
	if strings.Count(src, "def compute_area") != 2 {
		t.Errorf("expected both methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "def area\n") {
		t.Errorf("file still contains original area; got:\n%s", src)
	}
}

// TestRename_Ruby_HierarchyCrossFileDownWalk: base class in
// base.rb, subclass in impl.rb. Renaming the method on the base
// must propagate to the override on the subclass via the
// reverse-reference candidate path.
func TestRename_Ruby_HierarchyCrossFileDownWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"base.rb": `class Service
  def run(input)
    input
  end
end
`,
		"impl.rb": `require_relative 'base'

class ServiceImpl < Service
  def run(input)
    input.upcase
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"base.rb:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "base.rb"))
	impl, _ := os.ReadFile(filepath.Join(dir, "impl.rb"))
	if !strings.Contains(string(base), "def execute(input)") {
		t.Errorf("base.rb did not rename: %s", base)
	}
	if !strings.Contains(string(impl), "def execute(input)") {
		t.Errorf("impl.rb override did not rename: %s", impl)
	}
}

// TestRename_Ruby_HierarchyCrossFileUpWalk: rename on the subclass
// should rewrite the base class's method too via the up-walk through
// the require/require_relative graph.
func TestRename_Ruby_HierarchyCrossFileUpWalk(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"base.rb": `class Service
  def run(input)
    input
  end
end
`,
		"impl.rb": `require_relative 'base'

class ServiceImpl < Service
  def run(input)
    input.upcase
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"impl.rb:run"},
		map[string]any{"new_name": "execute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	base, _ := os.ReadFile(filepath.Join(dir, "base.rb"))
	impl, _ := os.ReadFile(filepath.Join(dir, "impl.rb"))
	if !strings.Contains(string(impl), "def execute(input)") {
		t.Errorf("impl.rb did not rename: %s", impl)
	}
	if !strings.Contains(string(base), "def execute(input)") {
		t.Errorf("base.rb base method did not rename via up-walk: %s", base)
	}
}

// TestRename_Ruby_HierarchyMixinInclude: `include Mod` should make
// Mod a hierarchy edge for the including class — renaming the
// module's method propagates to the includer's same-named method.
func TestRename_Ruby_HierarchyMixinInclude(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"mixin.rb": `module Greeter
  def hello
    "hi"
  end
end

class App
  include Greeter

  def hello
    "App says hi"
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"mixin.rb:hello"},
		map[string]any{"new_name": "greet", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "mixin.rb"))
	src := string(data)
	if strings.Count(src, "def greet") != 2 {
		t.Errorf("expected both module and class methods renamed; got:\n%s", src)
	}
	if strings.Contains(src, "def hello\n") {
		t.Errorf("file still contains original hello; got:\n%s", src)
	}
}

// TestRename_Ruby_HierarchyUnrelatedClassNotRewritten: two
// unrelated classes with same-named methods — only the target
// rewrites.
func TestRename_Ruby_HierarchyUnrelatedClassNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.rb": `class Alpha
  def process
    1
  end
end
`,
		"b.rb": `class Beta
  def process
    2
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.rb:process"},
		map[string]any{"new_name": "compute", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "a.rb"))
	b, _ := os.ReadFile(filepath.Join(dir, "b.rb"))
	if !strings.Contains(string(a), "def compute") {
		t.Errorf("a.rb target not renamed: %s", a)
	}
	if !strings.Contains(string(b), "def process") {
		t.Errorf("b.rb unrelated class incorrectly rewritten: %s", b)
	}
}
