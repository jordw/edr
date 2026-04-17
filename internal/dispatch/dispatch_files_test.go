package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func setupGlobRepo(t *testing.T) (index.SymbolStore, string) {
	t.Helper()
	tmp := t.TempDir()
	files := map[string]string{
		"main.go":         "package main\n\nfunc hello() {}\n",
		"cmd/sub.go":      "package cmd\n\nfunc hello() {}\n",
		"docs/readme.md":  "hello world\n",
		"internal/x.py":   "def hello():\n    pass\n",
	}
	for rel, body := range files {
		path := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	t.Cleanup(func() { db.Close() })
	return db, tmp
}

func runFilesQuery(t *testing.T, db index.SymbolStore, flags map[string]any) []string {
	t.Helper()
	res, err := dispatch.Dispatch(context.Background(), db, "files", []string{"hello"}, flags)
	if err != nil {
		t.Fatalf("dispatch files: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	raw, ok := m["files"].([]string)
	if !ok {
		// Result may store as []any depending on renderer path.
		if any, ok2 := m["files"].([]any); ok2 {
			out := make([]string, len(any))
			for i, v := range any {
				out[i], _ = v.(string)
			}
			return out
		}
		return nil
	}
	return raw
}

func TestFilesGlob_RestrictsByExtension(t *testing.T) {
	db, _ := setupGlobRepo(t)

	got := runFilesQuery(t, db, map[string]any{"glob": "**/*.go"})
	// Expect both .go files, not the .md or .py.
	has := func(s string) bool {
		for _, g := range got {
			if g == s {
				return true
			}
		}
		return false
	}
	if !has("main.go") || !has("cmd/sub.go") {
		t.Errorf("glob **/*.go missed go files: %v", got)
	}
	if has("docs/readme.md") || has("internal/x.py") {
		t.Errorf("glob **/*.go leaked non-go files: %v", got)
	}
}

func TestFilesGlob_RestrictsByDirectory(t *testing.T) {
	db, _ := setupGlobRepo(t)

	got := runFilesQuery(t, db, map[string]any{"glob": "cmd/*"})
	if len(got) != 1 || got[0] != "cmd/sub.go" {
		t.Errorf("glob cmd/* = %v, want [cmd/sub.go]", got)
	}
}

func TestFilesGlob_EmptyGlobIsNoop(t *testing.T) {
	db, _ := setupGlobRepo(t)

	unfiltered := runFilesQuery(t, db, map[string]any{})
	withEmpty := runFilesQuery(t, db, map[string]any{"glob": ""})
	if len(unfiltered) != len(withEmpty) {
		t.Errorf("empty glob changed result: %v vs %v", unfiltered, withEmpty)
	}
}

func TestIndexRebuildFlagAccepted(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"),
		[]byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	defer db.Close()

	// --rebuild is a no-op alias; it must not error and should still build.
	res, err := dispatch.Dispatch(context.Background(), db, "index",
		nil, map[string]any{"rebuild": true})
	if err != nil {
		t.Fatalf("index --rebuild: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	if m["status"] != "built" {
		t.Errorf("status = %v, want \"built\" (got full result: %v)", m["status"], m)
	}
}
