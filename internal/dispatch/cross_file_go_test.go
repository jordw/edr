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

// goModuleFixture writes a tiny Go module under a temp dir and
// returns (db, root). Always includes a go.mod so the namespace
// resolver finds the canonical path; without it the rename falls
// back to the same-package walker which has weaker disambiguation.
func goModuleFixture(t *testing.T, files map[string]string) (index.SymbolStore, string) {
	t.Helper()
	tmp := t.TempDir()
	files["go.mod"] = "module example.com/m\n\ngo 1.21\n"
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

// TestRename_GoAliasedImportRewritten verifies the import-gateway
// disambiguation accepts `alias.Rel` when `alias` is an alias for
// our target's package, AND skips an unrelated `filepath.Rel` call
// in the same file.
func TestRename_GoAliasedImportRewritten(t *testing.T) {
	db, dir := goModuleFixture(t, map[string]string{
		"output/output.go": "package output\n\nfunc Rel(p string) string { return p }\n",
		"caller/caller.go": `package caller

import (
	"path/filepath"

	alias "example.com/m/output"
)

func A() string { return alias.Rel("/x") }
func B() string { x, _ := filepath.Rel("/a", "/b"); return x }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "output/output.go") + ":Rel"},
		map[string]any{"new_name": "Calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "caller/caller.go"))
	got := string(body)
	if !strings.Contains(got, "alias.Calculate(") {
		t.Errorf("aliased call should be rewritten to alias.Calculate; got:\n%s", got)
	}
	if strings.Contains(got, "filepath.Calculate") {
		t.Errorf("unrelated filepath.Rel must NOT be rewritten; got:\n%s", got)
	}
	if !strings.Contains(got, "filepath.Rel(") {
		t.Errorf("filepath.Rel should remain intact; got:\n%s", got)
	}
}

// TestRename_GoTwoSameNameImports verifies that when a file imports
// two local packages each defining a same-named function, only the
// one matching the rename target's package is rewritten.
func TestRename_GoTwoSameNameImports(t *testing.T) {
	db, dir := goModuleFixture(t, map[string]string{
		"output/output.go": "package output\n\nfunc Rel(p string) string { return p }\n",
		"other/other.go":   "package other\n\nfunc Rel(p string) string { return p + p }\n",
		"caller/caller.go": `package caller

import (
	"example.com/m/other"
	"example.com/m/output"
)

func A() string { return output.Rel("/x") }
func B() string { return other.Rel("/y") }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{filepath.Join(dir, "output/output.go") + ":Rel"},
		map[string]any{"new_name": "Calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "caller/caller.go"))
	got := string(body)
	if !strings.Contains(got, "output.Calculate(") {
		t.Errorf("output.Rel should become output.Calculate; got:\n%s", got)
	}
	if strings.Contains(got, "other.Calculate") {
		t.Errorf("other.Rel must NOT be rewritten; got:\n%s", got)
	}
	if !strings.Contains(got, "other.Rel(") {
		t.Errorf("other.Rel should remain intact; got:\n%s", got)
	}
}
