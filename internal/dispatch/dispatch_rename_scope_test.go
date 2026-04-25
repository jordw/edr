package dispatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/index"
)

// TestScopeAwareSameFileSpans_Shadowing confirms that a local variable
// shadowing the target name is NOT emitted as a rename span.
func TestScopeAwareSameFileSpans_Shadowing(t *testing.T) {
	tmp := t.TempDir()
	src := []byte("package main\n\n" +
		"func Foo() int {\n\treturn 1\n}\n\n" +
		"func Bar() int {\n\tFoo := 42\n\treturn Foo\n}\n\n" +
		"func Use() int {\n\treturn Foo()\n}\n")
	path := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(path, src, 0644); err != nil {
		t.Fatal(err)
	}
	// Find the first "Foo" — that is the function declaration.
	fooIdx := -1
	for i := 0; i < len(src)-2; i++ {
		if string(src[i:i+3]) == "Foo" {
			fooIdx = i
			break
		}
	}
	if fooIdx < 0 {
		t.Fatal("could not locate Foo in fixture")
	}

	sym := &index.SymbolInfo{
		Name:      "Foo",
		File:      path,
		StartByte: uint32(fooIdx),
		EndByte:   uint32(fooIdx + 3),
	}
	spans, ok := scopeAwareSameFileSpans(sym)
	if !ok {
		t.Fatal("scopeAwareSameFileSpans returned ok=false; scope path did not engage")
	}
	// Expect: 1 decl span (at Foo function) + 1 ref span (Foo() in Use).
	// MUST NOT contain spans covering "Foo := 42" or "return Foo" (those are
	// bound to the local decl, not the target).
	type expect struct {
		start, end int
	}
	// Compute expected spans by finding byte offsets in src.
	useFoo := -1
	localFoo := -1
	retFoo := -1
	idx := 0
	for i := 0; i < len(src)-2; i++ {
		if string(src[i:i+3]) != "Foo" {
			continue
		}
		idx++
		switch idx {
		case 1: // def, handled
		case 2: // Foo := 42
			localFoo = i
		case 3: // return Foo
			retFoo = i
		case 4: // Foo() in Use
			useFoo = i
		}
	}
	if useFoo < 0 || localFoo < 0 || retFoo < 0 {
		t.Fatalf("could not find expected offsets: use=%d local=%d ret=%d", useFoo, localFoo, retFoo)
	}
	t.Logf("offsets: def=%d local=%d ret=%d use=%d", fooIdx, localFoo, retFoo, useFoo)
	t.Logf("got %d spans:", len(spans))
	for _, s := range spans {
		t.Logf("  [%d..%d] : %q", s.start, s.end, string(src[s.start:s.end]))
	}

	for _, s := range spans {
		// Any span that overlaps the local or retFoo offsets is a bug.
		if int(s.start) <= localFoo && localFoo < int(s.end) {
			t.Errorf("span [%d..%d] covers shadowing local at %d", s.start, s.end, localFoo)
		}
		if int(s.start) <= retFoo && retFoo < int(s.end) {
			t.Errorf("span [%d..%d] covers reference to shadowing local at %d", s.start, s.end, retFoo)
		}
	}
}
