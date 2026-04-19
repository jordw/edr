package atomic

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func TestWriteFile_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	body := []byte("hello world")

	if err := WriteFile(path, body); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
	// No stray temp files left behind.
	assertNoTempRemnants(t, dir, "out.bin")
}

func TestWriteFile_CreatesParentDir(t *testing.T) {
	root := t.TempDir()
	// Parent dir doesn't exist yet — two levels down.
	path := filepath.Join(root, "a", "b", "out.bin")
	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

func TestWriteVia_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "scope.bin")

	err := WriteVia(path, func(w io.Writer) error {
		if _, err := w.Write([]byte("prefix")); err != nil {
			return err
		}
		if _, err := w.Write([]byte("body")); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteVia: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "prefixbody" {
		t.Fatalf("got %q, want %q", got, "prefixbody")
	}
	assertNoTempRemnants(t, dir, "scope.bin")
}

func TestWriteVia_CallbackErrorCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")

	sentinel := errors.New("boom")
	err := WriteVia(path, func(w io.Writer) error {
		// Write some bytes, then fail. The partial temp file
		// must not appear at the final path.
		_, _ = w.Write([]byte("partial"))
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("final path should not exist after failure, stat err=%v", statErr)
	}
	assertNoTempRemnants(t, dir, "out.bin")
}

func TestWriteVia_DoesNotTouchExistingOnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := WriteVia(path, func(w io.Writer) error {
		_, _ = w.Write([]byte("new"))
		return errors.New("nope")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("original file was mutated: got %q", got)
	}
	assertNoTempRemnants(t, dir, "out.bin")
}

func TestWriteFile_ConcurrentWritesDoNotInterleave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")

	// Two distinct payloads, each 256 KiB. If the implementation
	// shared a ".tmp" path between writers, we'd see interleaving.
	a := bytes.Repeat([]byte("A"), 256<<10)
	b := bytes.Repeat([]byte("B"), 256<<10)

	const iters = 20
	var wg sync.WaitGroup
	for i := 0; i < iters; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := WriteFile(path, a); err != nil {
				t.Errorf("A write: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := WriteFile(path, b); err != nil {
				t.Errorf("B write: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, a) && !bytes.Equal(got, b) {
		t.Fatalf("final file is a mix of both writes (len=%d, first byte=%q)",
			len(got), got[0])
	}
	assertNoTempRemnants(t, dir, "out.bin")
}

// assertNoTempRemnants fails the test if any files besides `final` are
// present in dir. Useful to confirm temp files are always cleaned up.
func assertNoTempRemnants(t *testing.T, dir, final string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stray []string
	for _, e := range entries {
		if e.Name() == final {
			continue
		}
		stray = append(stray, e.Name())
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		t.Fatalf("unexpected leftover files in %s: %v", dir, stray)
	}
}

// Ensure we can construct realistic paths including spaces.
func TestWriteFile_PathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "has spaces.bin")
	if err := WriteFile(path, []byte("ok")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
}

// Sanity-check that WriteVia uses the file's io.Writer; a caller that
// wraps it (e.g. gzip.Writer) should see no surprises.
func TestWriteVia_WriterSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.bin")

	var written int
	err := WriteVia(path, func(w io.Writer) error {
		for i := 0; i < 10; i++ {
			n, err := fmt.Fprintf(w, "line %d\n", i)
			if err != nil {
				return err
			}
			written += n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteVia: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if int(info.Size()) != written {
		t.Fatalf("size mismatch: file=%d, written=%d", info.Size(), written)
	}
}
