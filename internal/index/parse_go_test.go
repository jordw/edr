package index

import "testing"

func TestParseGo_Fixture(t *testing.T) {
	src := []byte(`// file comment
package foo

import (
	"fmt"
	_ "embed"
	alias "some/path"
	. "dotimport"
)

import "single/import"

type Foo struct {
	Name string
	Age  int
}

type (
	Bar int
	Baz string
)

type Generic[T comparable] struct {
	Value T
}

type Aliased = Foo

const Version = "1.0"

const (
	A = iota
	B
	C
)

var globalMap = map[string]int{
	"a": 1,
	"b": 2,
}

var (
	X int
	Y string
)

func Free(x int) (int, error) {
	// func Shadow() // this should not count
	s := "func Shadow() {}"
	_ = s
	return x + 1, nil
}

func (f *Foo) Method(y string) error {
	return nil
}

func (b Bar) Other() {}

func FnGeneric[T any](x T) T {
	return x
}
`)
	r := ParseGo(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-15s recv=%-5s L%d-%d", i, s.Type, s.Name, s.Receiver, s.StartLine, s.EndLine)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] alias=%q path=%q L%d", i, imp.Alias, imp.Path, imp.Line)
	}

	want := []struct{ typ, name, recv string }{
		{"struct", "Foo", ""},
		{"type", "Bar", ""},
		{"type", "Baz", ""},
		{"struct", "Generic", ""},
		{"type", "Aliased", ""},
		{"constant", "Version", ""},
		{"constant", "A", ""},
		{"constant", "B", ""},
		{"constant", "C", ""},
		{"variable", "globalMap", ""},
		{"variable", "X", ""},
		{"variable", "Y", ""},
		{"function", "Free", ""},
		{"method", "Method", "Foo"},
		{"method", "Other", "Bar"},
		{"function", "FnGeneric", ""},
	}
	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if i >= len(r.Symbols) {
			break
		}
		got := r.Symbols[i]
		if got.Type != w.typ || got.Name != w.name || got.Receiver != w.recv {
			t.Errorf("symbol %d: got {%s %s recv=%q}, want {%s %s recv=%q}",
				i, got.Type, got.Name, got.Receiver, w.typ, w.name, w.recv)
		}
	}

	// The "func Shadow()" strings inside the function body must not produce symbols.
	for _, s := range r.Symbols {
		if s.Name == "Shadow" {
			t.Errorf("spurious Shadow symbol: %+v", s)
		}
	}

	wantImps := []struct{ alias, path string }{
		{"", "fmt"},
		{"_", "embed"},
		{"alias", "some/path"},
		{".", "dotimport"},
		{"", "single/import"},
	}
	if len(r.Imports) != len(wantImps) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantImps))
	}
	for i, w := range wantImps {
		if i >= len(r.Imports) {
			break
		}
		got := r.Imports[i]
		if got.Alias != w.alias || got.Path != w.path {
			t.Errorf("import %d: got {%q %q}, want {%q %q}", i, got.Alias, got.Path, w.alias, w.path)
		}
	}
}

