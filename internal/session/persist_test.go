package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sessions", "test.json")

	// Create a session and populate state.
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Responses["key1"] = "hash1"
	s1.Diffs["file.go:func"] = "--- a\n+++ b\n"
	s1.FileContent["file.go::"] = ContentEntry{Hash: "abc", Content: "hello", Order: 1}
	s1.SymbolContent["file.go:Foo:"] = ContentEntry{Hash: "def", Content: "world", Order: 2}
	s1.ContentOrder = 2
	s1.SeenBodies["file.go:Foo"] = "bodyhash"

	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}

	// Re-open and verify all state round-tripped.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if s2.Responses["key1"] != "hash1" {
		t.Errorf("Responses: got %q, want %q", s2.Responses["key1"], "hash1")
	}
	if s2.Diffs["file.go:func"] != "--- a\n+++ b\n" {
		t.Error("Diffs not preserved")
	}
	if s2.FileContent["file.go::"].Hash != "abc" {
		t.Error("FileContent not preserved")
	}
	if s2.SymbolContent["file.go:Foo:"].Hash != "def" {
		t.Error("SymbolContent not preserved")
	}
	if s2.ContentOrder != 2 {
		t.Errorf("ContentOrder: got %d, want 2", s2.ContentOrder)
	}
	if s2.SeenBodies["file.go:Foo"] != "bodyhash" {
		t.Error("SeenBodies not preserved")
	}
}

func TestPersistLRUEviction(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Fill beyond MaxContentEntries.
	for i := 0; i < MaxContentEntries+50; i++ {
		key := filepath.Join("file", string(rune('A'+i%26)), ".go")
		s.FileContent[key] = ContentEntry{Hash: "h", Content: "c", Order: i}
	}

	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(s2.FileContent) > MaxContentEntries {
		t.Errorf("LRU eviction failed: got %d entries, want <= %d", len(s2.FileContent), MaxContentEntries)
	}
}

func TestPersistCorruptFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "corrupt.json")

	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte("{invalid json"), 0644)

	s, err := Open(path)
	if err != nil {
		t.Fatal("should not error on corrupt file, got:", err)
	}
	// Should get a fresh session.
	if len(s.Responses) != 0 {
		t.Error("corrupt file should yield empty session")
	}
	// Should still persist on Close (filePath preserved).
	s.Responses["recovered"] = "yes"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Responses["recovered"] != "yes" {
		t.Error("corrupt recovery should preserve filePath for future saves")
	}
}

func TestSessionGC(t *testing.T) {
	tmp := t.TempDir()
	sessDir := SessionDir(tmp)
	os.MkdirAll(sessDir, 0755)

	// Create a session with a dead PID.
	os.WriteFile(filepath.Join(sessDir, "999999.json"), []byte("{}"), 0644)
	// Create a session with a non-numeric token (should be skipped).
	os.WriteFile(filepath.Join(sessDir, "mytoken.json"), []byte("{}"), 0644)

	cleared, err := GCSessions(tmp)
	if err != nil {
		t.Fatal(err)
	}

	// The dead PID session should be cleared.
	if len(cleared) != 1 || cleared[0] != "999999" {
		t.Errorf("expected [999999] cleared, got %v", cleared)
	}

	// Non-numeric token should survive.
	tokens, _ := ListSessions(tmp)
	found := false
	for _, tok := range tokens {
		if tok == "mytoken" {
			found = true
		}
	}
	if !found {
		t.Error("non-numeric token should survive GC")
	}
}

func TestSessionPathRejectsTraversal(t *testing.T) {
	for _, tok := range []string{"../../etc/passwd", "../foo", "a/b", "a\\b", "..", ""} {
		_, err := SessionPath("/tmp/edr", tok)
		if err == nil {
			t.Errorf("SessionPath(%q) should fail, got nil error", tok)
		}
	}
	// Valid tokens should work.
	for _, tok := range []string{"12345", "my-session", "test_123"} {
		path, err := SessionPath("/tmp/edr", tok)
		if err != nil {
			t.Errorf("SessionPath(%q) should succeed, got %v", tok, err)
		}
		if path == "" {
			t.Errorf("SessionPath(%q) returned empty path", tok)
		}
	}
}

func TestInMemorySessionCloseIsNoop(t *testing.T) {
	s := New()
	s.Responses["key"] = "val"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Should not create any files.
}
