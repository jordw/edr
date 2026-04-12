package index

import "testing"

func TestParseRust_Fixture(t *testing.T) {
	src := []byte(`// test.rs
use std::collections::HashMap;
use std::io::{self, Read, Write};
use crate::utils::*;

/// Doc comment with fn fake() inside
/* block comment with struct Fake {} */

pub struct Widget<T: Clone> {
    name: String,
    items: Vec<T>,
}

pub struct Point(f64, f64);

pub enum Color {
    Red,
    Green,
    Blue,
    Custom(u8, u8, u8),
}

pub trait Drawable {
    fn draw(&self);
    fn bounds(&self) -> (f64, f64);
}

impl<T: Clone> Widget<T> {
    pub fn new(name: String) -> Self {
        Widget { name, items: Vec::new() }
    }

    pub fn add(&mut self, item: T) {
        self.items.push(item);
    }

    pub async fn fetch(&self) -> Result<(), io::Error> {
        Ok(())
    }
}

impl Drawable for Widget<String> {
    fn draw(&self) {
        let s = "fn fake_in_string()";
        let r = r#"struct Fake {}"#;
        println!("{}", s);
    }

    fn bounds(&self) -> (f64, f64) {
        (0.0, 0.0)
    }
}

pub const MAX_SIZE: usize = 1024;

pub static GLOBAL: &str = "hello";

pub type WidgetId = u64;

pub mod submodule {
    pub fn helper() -> bool {
        true
    }
}

mod external;

macro_rules! my_macro {
    ($x:expr) => { $x * 2 };
}

pub unsafe fn dangerous() {}
pub const fn compile_time() -> i32 { 42 }
`)
	r := ParseRust(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"struct", "Widget"},
		{"struct", "Point"},
		{"enum", "Color"},
		{"interface", "Drawable"},
		{"method", "draw"},
		{"method", "bounds"},
		{"method", "new"},
		{"method", "add"},
		{"method", "fetch"},
		{"method", "draw"},
		{"method", "bounds"},
		{"constant", "MAX_SIZE"},
		{"variable", "GLOBAL"},
		{"type", "WidgetId"},
		{"class", "submodule"},
		{"function", "helper"},
		{"class", "external"},
		{"macro", "my_macro"},
		{"function", "dangerous"},
		{"function", "compile_time"},
	}
	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if i >= len(r.Symbols) {
			break
		}
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("symbol %d: got %s %q, want %s %q",
				i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}

	for _, s := range r.Symbols {
		if s.Name == "fake" || s.Name == "Fake" || s.Name == "fake_in_string" {
			t.Errorf("spurious symbol: %+v", s)
		}
	}

	wantImps := []string{
		"std::collections::HashMap",
		"std::io::",
		"crate::utils::*",
	}
	if len(r.Imports) != len(wantImps) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantImps))
	}
	for i, w := range wantImps {
		if i >= len(r.Imports) {
			break
		}
		if r.Imports[i].Path != w {
			t.Errorf("import %d: got %q want %q", i, r.Imports[i].Path, w)
		}
	}
}