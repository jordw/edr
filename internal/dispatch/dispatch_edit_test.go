package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// editFixture writes files into a temp dir and returns (db, root).
func editFixture(t *testing.T, files map[string]string) (index.SymbolStore, string) {
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

// TestEdit_HashMismatch_RejectsByDefault confirms the TOCTOU guard:
// editing with a stale --expect-hash must fail loudly, not silently
// rewrite a different file version.
func TestEdit_HashMismatch_RejectsByDefault(t *testing.T) {
	db, dir := editFixture(t, map[string]string{
		"a.go": "package a\nfunc Foo() {}\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "edit",
		[]string{filepath.Join(dir, "a.go")},
		map[string]any{
			"old_text":    "Foo",
			"new_text":    "Bar",
			"expect_hash": "deadbeef0000",
		})
	if err == nil {
		t.Fatal("expected hash-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("expected 'hash mismatch' in error, got: %v", err)
	}
	// File must be untouched.
	body, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(body), "Foo") {
		t.Errorf("file should be unchanged after hash-mismatch reject; got:\n%s", body)
	}
}

// TestEdit_HashMismatch_RefreshHashRetry covers the recovery path:
// --refresh-hash on a stale-hash failure should re-stat the file,
// inject the current hash, and retry the edit.
func TestEdit_HashMismatch_RefreshHashRetry(t *testing.T) {
	db, dir := editFixture(t, map[string]string{
		"a.go": "package a\nfunc Foo() {}\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "edit",
		[]string{filepath.Join(dir, "a.go")},
		map[string]any{
			"old_text":     "Foo",
			"new_text":     "Bar",
			"expect_hash":  "deadbeef0000", // stale
			"refresh_hash": true,
		})
	if err != nil {
		t.Fatalf("--refresh-hash retry should succeed; got: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(body), "Bar") {
		t.Errorf("retry should have applied the edit; got:\n%s", body)
	}
	if strings.Contains(string(body), "Foo()") {
		t.Errorf("old name should be replaced; got:\n%s", body)
	}
}

// TestEdit_HashMatch_AppliesEdit pins down the happy path so the
// refresh-retry test isn't proving a tautology (i.e., that the edit
// works only because expect_hash is somehow ignored).
func TestEdit_HashMatch_AppliesEdit(t *testing.T) {
	db, dir := editFixture(t, map[string]string{
		"a.go": "package a\nfunc Foo() {}\n",
	})
	abs := filepath.Join(dir, "a.go")
	hash, err := edit.FileHash(abs)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dispatch.Dispatch(context.Background(), db, "edit",
		[]string{abs},
		map[string]any{
			"old_text":    "Foo",
			"new_text":    "Bar",
			"expect_hash": hash,
		})
	if err != nil {
		t.Fatalf("matching hash should pass; got: %v", err)
	}
	body, _ := os.ReadFile(abs)
	if !strings.Contains(string(body), "Bar") {
		t.Errorf("expected Bar in result, got:\n%s", body)
	}
}

// TestEdit_HashMismatch_NoRefreshHashFalseDoesNotRetry confirms the
// retry is opt-in: callers that haven't asked for it must see the
// error so they can re-read instead of silently overwriting.
func TestEdit_HashMismatch_NoRefreshHashFalseDoesNotRetry(t *testing.T) {
	db, dir := editFixture(t, map[string]string{
		"a.go": "package a\nfunc Foo() {}\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "edit",
		[]string{filepath.Join(dir, "a.go")},
		map[string]any{
			"old_text":     "Foo",
			"new_text":     "Bar",
			"expect_hash":  "deadbeef0000",
			"refresh_hash": false,
		})
	if err == nil {
		t.Fatal("refresh_hash=false must propagate hash-mismatch error")
	}
	body, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if strings.Contains(string(body), "Bar") {
		t.Errorf("file must not be modified when retry is disabled; got:\n%s", body)
	}
}

// TestEdit_HashMismatch_FileSymbolStripsSuffix covers an easy
// regression: the retry path's file:Symbol parsing must hash the
// FILE, not try to hash a path with a colon.
func TestEdit_HashMismatch_FileSymbolStripsSuffix(t *testing.T) {
	db, dir := editFixture(t, map[string]string{
		"a.go": "package a\n\nfunc Foo() { _ = 1 }\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "edit",
		[]string{filepath.Join(dir, "a.go") + ":Foo"},
		map[string]any{
			"new_text":     "func Foo() { _ = 2 }",
			"expect_hash":  "deadbeef0000",
			"refresh_hash": true,
		})
	if err != nil {
		t.Fatalf("file:Symbol retry should succeed; got: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(body), "_ = 2") {
		t.Errorf("symbol-scoped edit should apply on retry; got:\n%s", body)
	}
}
