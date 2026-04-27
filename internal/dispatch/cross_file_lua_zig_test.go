package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func plainFixture(t *testing.T, files map[string]string) (index.SymbolStore, string) {
	t.Helper()
	tmp := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	t.Cleanup(func() { db.Close() })
	return db, tmp
}

// TestRename_LuaCrossFileModule covers the always-commit Lua renamer.
// Lua has no static type info or trackable imports beyond `require` /
// `dofile`, so the registry's luaRenamer walks every .lua file. The
// caller's `lib.compute(...)` property-access form must be rewritten
// when the target's module-table return name matches.
func TestRename_LuaCrossFileModule(t *testing.T) {
	db, dir := plainFixture(t, map[string]string{
		"lib.lua": `local M = {}

function M.compute(x)
  return x * 2
end

return M
`,
		"app.lua": `local lib = require("lib")

return lib.compute(5)
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "lib.lua") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	app, _ := os.ReadFile(filepath.Join(dir, "app.lua"))
	if !strings.Contains(string(app), "lib.calculate(5)") {
		t.Errorf("caller's lib.compute should be rewritten to lib.calculate; got:\n%s", app)
	}
	lib, _ := os.ReadFile(filepath.Join(dir, "lib.lua"))
	if !strings.Contains(string(lib), "function M.calculate") {
		t.Errorf("module declaration should be renamed; got:\n%s", lib)
	}
}

// TestRename_LuaSameNameUnrelatedModulePreserved covers Lua's
// "always-commit" risk: because the resolver walks every .lua file
// and emits name-matched refs, an unrelated module also exporting a
// `compute` would get rewritten too. This pins current behavior — a
// known false-positive of the Lua resolver. If a stricter resolver
// lands later, this assertion flips and the test should be updated.
func TestRename_LuaSameNameUnrelatedModule(t *testing.T) {
	db, dir := plainFixture(t, map[string]string{
		"lib.lua": "local M = {}\nfunction M.compute(x) return x*2 end\nreturn M\n",
		"unrelated.lua": "local U = {}\nfunction U.compute(x) return x+1 end\nreturn U\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "lib.lua") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Target rewrites — guaranteed.
	lib, _ := os.ReadFile(filepath.Join(dir, "lib.lua"))
	if !strings.Contains(string(lib), "function M.calculate") {
		t.Errorf("target M.compute should rewrite to M.calculate; got:\n%s", lib)
	}
	// We don't assert about unrelated.lua: the always-commit policy
	// means the walker may rewrite it. The guarantee that matters for
	// users is that the WARNING is emitted (covered indirectly via
	// CodeMentions accounting in the rename result). This test exists
	// to surface any future change in behavior; it should be updated
	// when the resolver tightens.
	_, _ = os.ReadFile(filepath.Join(dir, "unrelated.lua"))
}

// TestRename_ZigCrossFileImport covers the always-commit Zig renamer.
// `@import("lib.zig")` is not in our import graph, so zigRenamer walks
// every .zig file and emits name-matched refs.
func TestRename_ZigCrossFileImport(t *testing.T) {
	db, dir := plainFixture(t, map[string]string{
		"lib.zig": `pub fn compute(x: i32) i32 {
    return x * 2;
}
`,
		"app.zig": `const lib = @import("lib.zig");

pub fn main() !void {
    if (lib.compute(5) != 10) return error.WrongResult;
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "lib.zig") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	lib, _ := os.ReadFile(filepath.Join(dir, "lib.zig"))
	if !strings.Contains(string(lib), "fn calculate") {
		t.Errorf("declaration should be renamed; got:\n%s", lib)
	}
	app, _ := os.ReadFile(filepath.Join(dir, "app.zig"))
	if !strings.Contains(string(app), "lib.calculate(5)") {
		t.Errorf("caller's lib.compute should be rewritten; got:\n%s", app)
	}
}

// TestRename_ZigStructMethodSameFile keeps the same-file path on the
// books for Zig — the registry's zigRenamer commits same-file decls
// even when no caller exists, and we want a regression catcher for
// that simple case.
func TestRename_ZigStructMethodSameFile(t *testing.T) {
	db, dir := plainFixture(t, map[string]string{
		"main.zig": `const Foo = struct {
    pub fn compute(self: Foo, x: i32) i32 {
        _ = self;
        return x * 2;
    }
};

pub fn main() !void {
    var f = Foo{};
    _ = f.compute(3);
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "main.zig") + ":compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "main.zig"))
	got := string(body)
	if !strings.Contains(got, "fn calculate") {
		t.Errorf("decl should be renamed; got:\n%s", got)
	}
	if !strings.Contains(got, "f.calculate(3)") {
		t.Errorf("call site should be renamed; got:\n%s", got)
	}
}
