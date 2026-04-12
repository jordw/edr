package index

import "testing"

func TestParseZig_Fixture(t *testing.T) {
	src := []byte(`// Zig fixture

const std = @import("std");
const mem = @import("mem.zig");

// Top-level struct
const Point = struct {
    x: f64,
    y: f64,

    pub fn distance(self: Point) f64 {
        return @sqrt(self.x * self.x + self.y * self.y);
    }
};

// Enum
const Color = enum {
    red,
    green,
    blue,
};

// Union
const Value = union(enum) {
    int: i64,
    float: f64,
};

// Error set
const ParseError = error {
    InvalidInput,
    Overflow,
};

// Constant
const max_size: usize = 1024;

// Top-level functions
pub fn add(a: i32, b: i32) i32 {
    return a + b;
}

fn internal(x: u8) bool {
    return x > 0;
}

// Test block
test "add works" {
    const result = add(1, 2);
    _ = result;
}

test "internal check" {
    _ = internal(5);
}

// String with fake symbol
const fake_str = "fn FakeFunc() void {}";
`)
	r := ParseZig(src)
	for i, sym := range r.Symbols {
		t.Logf("sym[%d] %-9s %-20s L%d-%d parent=%d", i, sym.Type, sym.Name, sym.StartLine, sym.EndLine, sym.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	wantSyms := []struct{ typ, name string }{
		{"constant", "std"},
		{"constant", "mem"},
		{"struct", "Point"},
		{"enum", "Color"},
		{"type", "Value"},
		{"type", "ParseError"},
		{"constant", "max_size"},
		{"function", "add"},
		{"function", "internal"},
		{"function", "add works"},
		{"function", "internal check"},
		{"constant", "fake_str"},
	}
	if len(r.Symbols) != len(wantSyms) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(wantSyms))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q", i, s.Type, s.Name)
		}
	}
	for i, w := range wantSyms {
		if i >= len(r.Symbols) {
			t.Errorf("symbol %d missing: want %s %q", i, w.typ, w.name)
			continue
		}
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("symbol %d: got %s %q, want %s %q",
				i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}

	// No spurious symbols from string literals
	for _, s := range r.Symbols {
		if s.Name == "FakeFunc" {
			t.Errorf("spurious symbol from string literal: %+v", s)
		}
	}

	// Verify imports
	wantImps := []string{"std", "mem.zig"}
	if len(r.Imports) != len(wantImps) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantImps))
	}
	for i, want := range wantImps {
		if i >= len(r.Imports) {
			t.Errorf("import %d missing: want %q", i, want)
			continue
		}
		if r.Imports[i].Path != want {
			t.Errorf("import %d: got %q, want %q", i, r.Imports[i].Path, want)
		}
	}

	// Verify EndLines set for containers
	for _, s := range r.Symbols {
		if s.EndLine == 0 {
			t.Errorf("symbol %q (%s) has EndLine=0", s.Name, s.Type)
		}
	}
}

func TestParseZig_PubFn(t *testing.T) {
	src := []byte(`pub fn greet(name: []const u8) void {
    _ = name;
}

fn private() u32 {
    return 42;
}
`)
	r := ParseZig(src)
	if len(r.Symbols) != 2 {
		t.Errorf("got %d symbols, want 2", len(r.Symbols))
		return
	}
	if r.Symbols[0].Name != "greet" || r.Symbols[0].Type != "function" {
		t.Errorf("symbol 0: got %s %q, want function greet", r.Symbols[0].Type, r.Symbols[0].Name)
	}
	if r.Symbols[1].Name != "private" || r.Symbols[1].Type != "function" {
		t.Errorf("symbol 1: got %s %q, want function private", r.Symbols[1].Type, r.Symbols[1].Name)
	}
}

func TestParseZig_Imports(t *testing.T) {
	src := []byte(`const std = @import("std");
const fs = @import("std").fs;
const zlib = @import("zlib.zig");
`)
	r := ParseZig(src)
	// Should have imports for std and zlib.zig
	// std appears twice but @import("std") only once from standalone, once more from .fs expression
	found := map[string]bool{}
	for _, imp := range r.Imports {
		found[imp.Path] = true
	}
	if !found["std"] {
		t.Errorf("missing import std")
	}
	if !found["zlib.zig"] {
		t.Errorf("missing import zlib.zig")
	}
}

func TestParseZig_NestedFnNotRecorded(t *testing.T) {
	src := []byte(`pub fn outer() void {
    const inner = fn() void {};
    _ = inner;
}
`)
	r := ParseZig(src)
	// Only outer should be recorded
	if len(r.Symbols) != 1 {
		t.Errorf("got %d symbols, want 1", len(r.Symbols))
		for _, s := range r.Symbols {
			t.Logf("  %s %q", s.Type, s.Name)
		}
		return
	}
	if r.Symbols[0].Name != "outer" {
		t.Errorf("expected outer, got %q", r.Symbols[0].Name)
	}
}
