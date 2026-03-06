package gather

import (
	"testing"

	"github.com/jordw/edr/internal/index"
)

func TestIsTestFunction(t *testing.T) {
	tests := []struct {
		name string
		file string
		want bool
	}{
		// Go
		{"TestFoo", "foo_test.go", true},
		{"BenchmarkFoo", "foo_test.go", true},
		{"ExampleFoo", "foo_test.go", true},
		{"helperFunc", "foo_test.go", false},
		{"setUp", "foo_test.go", false},
		// Python
		{"test_parse", "test_parser.py", true},
		{"TestParser", "test_parser.py", true},
		{"helper", "test_parser.py", false},
		// JS/TS
		{"test", "app.test.js", true},
		{"testLogin", "app.test.ts", true},
		{"describe", "app.spec.tsx", true},
		{"renderComponent", "app.test.jsx", false},
		// Java
		{"testCreate", "FooTest.java", true},
		{"setUp", "FooTest.java", false},
		// Ruby
		{"test_create", "foo_test.rb", true},
		{"helper_method", "foo_test.rb", false},
	}
	for _, tt := range tests {
		got := isTestFunction(tt.name, tt.file)
		if got != tt.want {
			t.Errorf("isTestFunction(%q, %q) = %v, want %v", tt.name, tt.file, got, tt.want)
		}
	}
}

func TestPartitionTests(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{"TestFoo", "foo_test.go"},
		{"helperSetup", "foo_test.go"},
		{"TestBar", "foo_test.go"},
		{"buildFixture", "foo_test.go"},
	}

	var syms []index.SymbolInfo
	for _, tt := range tests {
		syms = append(syms, index.SymbolInfo{Name: tt.name, File: tt.file})
	}

	funcs, helpers := partitionTests(syms)
	if len(funcs) != 2 {
		t.Errorf("expected 2 test functions, got %d", len(funcs))
	}
	if len(helpers) != 2 {
		t.Errorf("expected 2 helpers, got %d", len(helpers))
	}
	if funcs[0].Name != "TestFoo" {
		t.Errorf("expected first test func to be TestFoo, got %s", funcs[0].Name)
	}
}
