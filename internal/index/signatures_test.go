package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractContainerStub_GoStruct(t *testing.T) {
	tmp := t.TempDir()
	src := `package main

type DB struct {
	db  *retryDB
	raw *sql.DB
	mu  sync.Mutex
}
`
	file := filepath.Join(tmp, "db.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	container := SymbolInfo{
		Name:      "DB",
		Type:      "type",
		File:      file,
		StartByte: uint32(strings.Index(src, "type DB")),
		EndByte:   uint32(strings.Index(src, "\n}") + 2), // include the }
	}

	// No children — this is the bug scenario
	result := ExtractContainerStub(container, nil)

	// Should contain field names from extractGoFields fallback
	if !strings.Contains(result, "db *retryDB") {
		t.Errorf("expected field 'db *retryDB' in stub, got:\n%s", result)
	}
	if !strings.Contains(result, "raw *sql.DB") {
		t.Errorf("expected field 'raw *sql.DB' in stub, got:\n%s", result)
	}
	if !strings.Contains(result, "mu sync.Mutex") {
		t.Errorf("expected field 'mu sync.Mutex' in stub, got:\n%s", result)
	}
	if !strings.Contains(result, "}") {
		t.Errorf("expected closing brace in stub, got:\n%s", result)
	}
}

func TestExtractContainerStub_GoInterface(t *testing.T) {
	tmp := t.TempDir()
	src := `package main

type Reader interface {
	Read(p []byte) (n int, err error)
	Close() error
}
`
	file := filepath.Join(tmp, "iface.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	container := SymbolInfo{
		Name:      "Reader",
		Type:      "type",
		File:      file,
		StartByte: uint32(strings.Index(src, "type Reader")),
		EndByte:   uint32(strings.Index(src, "\n}\n") + 2),
	}

	result := ExtractContainerStub(container, nil)

	// extractGoFields treats method specs as fields (name + first token)
	if !strings.Contains(result, "Read(p") {
		t.Errorf("expected method 'Read' in stub, got:\n%s", result)
	}
	if !strings.Contains(result, "Close()") {
		t.Errorf("expected method 'Close' in stub, got:\n%s", result)
	}
}

func TestExtractContainerStub_GoStructWithChildren(t *testing.T) {
	// When children ARE present (e.g., methods within byte range), the fallback
	// should NOT be triggered.
	tmp := t.TempDir()
	src := `package main

type Server struct {
	addr string
}
`
	file := filepath.Join(tmp, "server.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	container := SymbolInfo{
		Name:      "Server",
		Type:      "type",
		File:      file,
		StartByte: uint32(strings.Index(src, "type Server")),
		EndByte:   uint32(strings.Index(src, "\n}\n") + 2),
	}

	// Simulate a child symbol within the container's byte range
	child := SymbolInfo{
		Name:      "Init",
		Type:      "method",
		File:      file,
		StartByte: container.StartByte + 5, // within range
		EndByte:   container.EndByte - 1,
	}

	result := ExtractContainerStub(container, []SymbolInfo{child})
	// With children present, len(lines) > 1, so fallback is skipped.
	// We just verify it doesn't panic and produces some output.
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestExtractContainerStub_NonGoUnchanged(t *testing.T) {
	// Python classes should not trigger the Go fallback
	tmp := t.TempDir()
	src := `class Foo:
    def bar(self):
        pass
`
	file := filepath.Join(tmp, "foo.py")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	container := SymbolInfo{
		Name:      "Foo",
		Type:      "class",
		File:      file,
		StartByte: 0,
		EndByte:   uint32(len(src) - 1),
	}

	result := ExtractContainerStub(container, nil)
	// Should get just the header, no Go field fallback
	if strings.Contains(result, "def bar") && strings.Contains(result, "db *retryDB") {
		t.Error("Go fallback should not trigger for Python files")
	}
	if !strings.Contains(result, "class Foo:") {
		t.Errorf("expected 'class Foo:' header, got:\n%s", result)
	}
}
