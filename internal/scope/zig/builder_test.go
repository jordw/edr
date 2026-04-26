package zig

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

func find(decls []scope.Decl, name string) *scope.Decl {
	for i := range decls {
		if decls[i].Name == name {
			return &decls[i]
		}
	}
	return nil
}

func TestParse_TopLevelFn(t *testing.T) {
	src := []byte(`pub fn add(a: i32, b: i32) i32 {
    return a + b;
}

fn caller() i32 {
    return add(1, 2);
}
`)
	r := Parse("a.zig", src)
	if find(r.Decls, "add") == nil {
		t.Fatalf("expected decl `add`; got %+v", r.Decls)
	}
	if find(r.Decls, "caller") == nil {
		t.Fatalf("expected decl `caller`; got %+v", r.Decls)
	}
	// Params a, b should also be decls.
	if find(r.Decls, "a") == nil || find(r.Decls, "b") == nil {
		t.Fatalf("expected param decls a, b; got %+v", r.Decls)
	}
	// Ref `add` inside caller should resolve to file-scope add.
	resolved := false
	for _, ref := range r.Refs {
		if ref.Name == "add" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "direct_scope" {
			resolved = true
		}
	}
	if !resolved {
		t.Errorf("expected `add` ref to resolve via scope; refs=%+v", r.Refs)
	}
}

func TestParse_ConstAndVar(t *testing.T) {
	src := []byte(`pub const MAX: u32 = 100;
var counter: u32 = 0;

pub fn bump() void {
    counter += 1;
}
`)
	r := Parse("a.zig", src)
	d := find(r.Decls, "MAX")
	if d == nil || d.Kind != scope.KindConst {
		t.Errorf("expected MAX as KindConst; got %+v", d)
	}
	d = find(r.Decls, "counter")
	if d == nil || d.Kind != scope.KindVar {
		t.Errorf("expected counter as KindVar; got %+v", d)
	}
}

func TestParse_StructDecl(t *testing.T) {
	src := []byte(`pub const Point = struct {
    x: f32,
    y: f32,

    pub fn magnitude(self: Point) f32 {
        return self.x * self.x + self.y * self.y;
    }
};
`)
	r := Parse("a.zig", src)
	d := find(r.Decls, "Point")
	if d == nil {
		t.Fatalf("expected Point decl; got %+v", r.Decls)
	}
	if d.Kind != scope.KindClass {
		t.Errorf("expected Point.Kind == class; got %v", d.Kind)
	}
	// `magnitude` should be a KindMethod (defined inside a struct body).
	m := find(r.Decls, "magnitude")
	if m == nil {
		t.Fatalf("expected `magnitude` decl inside struct; got %+v", r.Decls)
	}
	if m.Kind != scope.KindMethod {
		t.Errorf("expected magnitude.Kind == method; got %v", m.Kind)
	}
}

func TestParse_EnumDecl(t *testing.T) {
	src := []byte(`pub const Color = enum {
    red,
    green,
    blue,
};
`)
	r := Parse("a.zig", src)
	d := find(r.Decls, "Color")
	if d == nil || d.Kind != scope.KindEnum {
		t.Errorf("expected Color as KindEnum; got %+v", d)
	}
}

func TestParse_BlockShadow(t *testing.T) {
	src := []byte(`pub fn run() void {
    const x: u32 = 1;
    {
        const x: u32 = 2;
        _ = x;
    }
    _ = x;
}
`)
	r := Parse("a.zig", src)
	count := 0
	for _, d := range r.Decls {
		if d.Name == "x" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 decls of x (outer + inner block), got %d", count)
	}
}

func TestParse_PropertyAccess(t *testing.T) {
	src := []byte(`const std = @import("std");

pub fn main() void {
    std.debug.print("hi\n", .{});
}
`)
	r := Parse("a.zig", src)
	// `debug` and `print` should be property_access refs.
	for _, want := range []string{"debug", "print"} {
		var found bool
		for _, ref := range r.Refs {
			if ref.Name == want && ref.Binding.Reason == "property_access" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected property_access ref for %q; refs=%+v", want, r.Refs)
		}
	}
}

func TestParse_BuiltinResolution(t *testing.T) {
	src := []byte(`pub fn x(n: u32) u32 {
    return n;
}
`)
	r := Parse("a.zig", src)
	// `u32` ref should resolve as builtin.
	var found bool
	for _, ref := range r.Refs {
		if ref.Name == "u32" && ref.Binding.Reason == "builtin" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected u32 ref to resolve as builtin; refs=%+v", r.Refs)
	}
}

func TestParse_ForLoopCapture(t *testing.T) {
	src := []byte(`pub fn iter(xs: []u8) void {
    for (xs) |item| {
        _ = item;
    }
}
`)
	r := Parse("a.zig", src)
	// `item` should be a decl, scoped to the for-block.
	d := find(r.Decls, "item")
	if d == nil {
		t.Fatalf("expected `item` capture decl; got %+v", r.Decls)
	}
	// The `_ = item` ref should resolve.
	var resolved bool
	for _, ref := range r.Refs {
		if ref.Name == "item" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "direct_scope" {
			resolved = true
		}
	}
	if !resolved {
		t.Errorf("expected item ref to resolve to capture; refs=%+v", r.Refs)
	}
}
