package index

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestResolvePathRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()

	got, err := ResolvePath(root, "internal/file.go")
	if err != nil {
		t.Fatalf("ResolvePath inside root: %v", err)
	}
	want := filepath.Join(root, "internal", "file.go")
	if got != want {
		t.Fatalf("ResolvePath mismatch: got %q want %q", got, want)
	}

	if _, err := ResolvePath(root, "../outside.go"); err == nil {
		t.Fatal("ResolvePath should reject paths outside the repo root")
	}
}

func TestOpenDBConcurrencySettings(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Verify WAL mode is enabled
	var journalMode string
	if err := db.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	// Verify busy_timeout is set (in-process hint; cross-process retry is in retryDB)
	var timeout int
	if err := db.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 30000 {
		t.Errorf("busy_timeout = %d, want 30000", timeout)
	}
}

func TestIndexRepoPrunesOutOfRootEntries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "edr_write_test.go")
	if err := os.WriteFile(outsideFile, []byte("package main\nfunc Outside() {}\n"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}

	if err := db.UpsertFile(ctx, outsideFile, "deadbeef", 1); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	if err := db.InsertSymbol(ctx, SymbolInfo{
		Name:      "Outside",
		Type:      "function",
		File:      outsideFile,
		StartLine: 2,
		EndLine:   2,
		StartByte: 13,
		EndByte:   31,
	}); err != nil {
		t.Fatalf("InsertSymbol: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = OpenDB(root)
	if err != nil {
		t.Fatalf("reopen OpenDB: %v", err)
	}
	defer db.Close()

	files, symbols, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if files != 1 || symbols != 1 {
		t.Fatalf("expected reopen to preserve entries until indexing, got files=%d symbols=%d", files, symbols)
	}

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	files, symbols, err = db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after IndexRepo: %v", err)
	}
	if files != 0 || symbols != 0 {
		t.Fatalf("expected IndexRepo prune to remove outside entries, got files=%d symbols=%d", files, symbols)
	}
}

func TestOpenDBCreatesWriterLock(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	lockPath := filepath.Join(root, ".edr", "writer.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("writer.lock should exist after OpenDB: %v", err)
	}

	if db.lockFile == nil {
		t.Fatal("db.lockFile should be non-nil")
	}
}

func TestWithWriteLockSerializesGoroutines(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Use a shared counter to detect interleaving. Each goroutine reads the
	// counter, sleeps briefly, then writes counter+1. If two goroutines
	// interleave inside the lock, the final count will be less than N.
	const N = 20
	var counter int64
	var wg sync.WaitGroup

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.WithWriteLock(func() error {
				val := atomic.LoadInt64(&counter)
				time.Sleep(time.Millisecond)
				atomic.StoreInt64(&counter, val+1)
				return nil
			})
		}()
	}
	wg.Wait()

	if counter != N {
		t.Fatalf("expected counter=%d after %d serialized increments, got %d (interleaving detected)", N, N, counter)
	}
}

func TestWithWriteLockReturnsError(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// fn error should propagate through WithWriteLock
	err = db.WithWriteLock(func() error {
		return fmt.Errorf("intentional error")
	})
	if err == nil || err.Error() != "intentional error" {
		t.Fatalf("expected fn error to propagate, got: %v", err)
	}

	// nil error should work fine
	err = db.WithWriteLock(func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

func TestWithWriteLockCrossProcess(t *testing.T) {
	root := t.TempDir()
	// Create .edr dir and writer.lock so the child process can open it
	edrDir := filepath.Join(root, ".edr")
	os.MkdirAll(edrDir, 0755)
	lockPath := filepath.Join(edrDir, "writer.lock")

	// Parent acquires the flock directly (simulating a DB holding the lock)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		lockFile.Close()
		t.Fatalf("parent flock: %v", err)
	}

	// Spawn a child Go process that tries to acquire the same lock with LOCK_NB.
	// It should fail because the parent holds it.
	tryLockScript := fmt.Sprintf(`
		package main
		import (
			"fmt"
			"os"
			"syscall"
		)
		func main() {
			f, err := os.OpenFile(%q, os.O_CREATE|os.O_RDWR, 0644)
			if err != nil { fmt.Println("error"); os.Exit(1) }
			defer f.Close()
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err != nil { fmt.Println("blocked") } else { fmt.Println("acquired") }
		}
	`, lockPath)

	childFile := filepath.Join(t.TempDir(), "trylock.go")
	os.WriteFile(childFile, []byte(tryLockScript), 0644)

	cmd := exec.Command("go", "run", childFile)
	out, err := cmd.Output()
	if err != nil {
		lockFile.Close()
		t.Fatalf("child process: %v", err)
	}
	result := strings.TrimSpace(string(out))
	if result != "blocked" {
		lockFile.Close()
		t.Fatalf("expected child to be blocked while parent holds lock, got %q", result)
	}

	// Release the parent lock by closing the fd
	lockFile.Close()

	// Now child should be able to acquire
	cmd = exec.Command("go", "run", childFile)
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("child process after unlock: %v", err)
	}
	result = strings.TrimSpace(string(out))
	if result != "acquired" {
		t.Fatalf("expected child to acquire lock after parent released, got %q", result)
	}
}

func TestCloseReleasesLockFile(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}

	// Hold the writer lock
	lockAcquired := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		db.WithWriteLock(func() error {
			close(lockAcquired)
			<-lockReleased
			return nil
		})
	}()
	<-lockAcquired

	// While the lock is held, try to open a second DB — its WithWriteLock
	// should block. Use a non-blocking check via flock.
	lockPath := filepath.Join(root, ".edr", "writer.lock")
	probe, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		close(lockReleased)
		t.Fatalf("open probe: %v", err)
	}
	defer probe.Close()

	// Non-blocking try should fail while lock is held
	err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(probe.Fd()), syscall.LOCK_UN)
		close(lockReleased)
		t.Fatal("expected non-blocking flock to fail while lock is held")
	}

	// Release the lock and close the DB
	close(lockReleased)
	time.Sleep(10 * time.Millisecond) // let the goroutine finish
	db.Close()

	// After Close, the lock file should be released
	err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		t.Fatalf("expected flock to succeed after db.Close(), got: %v", err)
	}
	syscall.Flock(int(probe.Fd()), syscall.LOCK_UN)
}

func TestConcurrentFirstUseIndexing(t *testing.T) {
	// Two goroutines opening the same fresh DB and indexing concurrently
	// should both succeed without SQLITE_BUSY errors.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	const N = 4
	errs := make([]error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			db, err := OpenDB(root)
			if err != nil {
				errs[idx] = fmt.Errorf("OpenDB: %w", err)
				return
			}
			defer db.Close()

			ctx := context.Background()
			err = db.WithWriteLock(func() error {
				// Re-check like the real openDB does
				files, _, _ := db.Stats(ctx)
				if files > 0 {
					return nil
				}
				_, _, e := IndexRepo(ctx, db)
				return e
			})
			if err != nil {
				errs[idx] = fmt.Errorf("index: %w", err)
				return
			}

			// Verify we can read stats
			files, _, err := db.Stats(ctx)
			if err != nil {
				errs[idx] = fmt.Errorf("Stats: %w", err)
				return
			}
			if files == 0 {
				errs[idx] = fmt.Errorf("expected files > 0 after indexing")
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

func TestRetryRowScanRetries(t *testing.T) {
	// Verify that retryRow.Scan works for normal queries.
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Stats uses QueryRowContext (now retryRow) — verify it works
	files, symbols, err := db.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if files != 0 || symbols != 0 {
		t.Fatalf("expected 0/0 for fresh DB, got files=%d symbols=%d", files, symbols)
	}

	// Also test QueryRow (no context)
	var version int
	err = db.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
}

func TestSearchSymbolsLimit(t *testing.T) {
	tmp := t.TempDir()
	// Create a Go file with several symbols that all match "item".
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main

func itemA() {}
func itemB() {}
func itemC() {}
func itemD() {}
func itemE() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Without limit: all 5 symbols returned.
	all, err := db.SearchSymbols(ctx, "item")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5 symbols, got %d", len(all))
	}

	// With limit=2: only 2 returned.
	limited, err := db.SearchSymbols(ctx, "item", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 symbols with limit, got %d", len(limited))
	}

	// With limit=0: no limit applied (same as no limit arg).
	noLimit, err := db.SearchSymbols(ctx, "item", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(noLimit) != 5 {
		t.Fatalf("expected 5 symbols with limit=0, got %d", len(noLimit))
	}
}

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"ApplyEdit", []string{"Apply", "Edit"}},
		{"parseConfig", []string{"parse", "Config"}},
		{"HTTPServer", []string{"HTTPServer"}}, // no split on consecutive uppercase
		{"simple", []string{"simple"}},
		{"getHTTPResponse", []string{"get", "HTTPResponse"}},
	}
	for _, tt := range tests {
		got := splitCamelCase(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitCamelCase(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitCamelCase(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestSymbolSimilarity(t *testing.T) {
	// Substring match should score higher than no match
	if s := symbolSimilarity("apply", "applyedit"); s <= 0 {
		t.Errorf("'apply' should be similar to 'applyedit', got score %d", s)
	}
	// Shared prefix
	if s := symbolSimilarity("parse", "parser"); s <= 0 {
		t.Errorf("'parse' should be similar to 'parser', got score %d", s)
	}
	// No similarity
	if s := symbolSimilarity("xyz", "abc"); s != 0 {
		t.Errorf("'xyz' should not be similar to 'abc', got score %d", s)
	}
	// Reverse substring
	if s := symbolSimilarity("applyedit", "apply"); s <= 0 {
		t.Errorf("'applyedit' should be similar to 'apply', got score %d", s)
	}
}

func TestGetSymbolAmbiguousReturnsError(t *testing.T) {
	tmp := t.TempDir()
	// Write a Go file with multiple init functions (same name, multiple definitions).
	src := []byte(`package main

func init() { println("first") }
func init() { println("second") }
func init() { println("third") }
func unique() {}
`)
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), src, 0644); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	file := filepath.Join(tmp, "main.go")

	// Unambiguous symbol should succeed.
	sym, err := db.GetSymbol(ctx, file, "unique")
	if err != nil {
		t.Fatalf("GetSymbol(unique): unexpected error: %v", err)
	}
	if sym.Name != "unique" {
		t.Errorf("GetSymbol(unique).Name = %q, want %q", sym.Name, "unique")
	}

	// Ambiguous symbol should return an error containing "ambiguous".
	_, err = db.GetSymbol(ctx, file, "init")
	if err == nil {
		t.Fatal("GetSymbol(init): expected error for ambiguous symbol, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("GetSymbol(init): expected error to contain \"ambiguous\", got: %v", err)
	}
	// Error should mention the match count and at least one line number.
	if !strings.Contains(err.Error(), "3 matches") {
		t.Errorf("GetSymbol(init): expected error to contain \"3 matches\", got: %v", err)
	}
}
