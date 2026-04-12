package index

import "testing"

func TestParsePython_Fixture(t *testing.T) {
	src := []byte(`# header
"""module docstring
with class Fake and def trap inside
"""

import os
import sys as system
import collections.abc
from typing import List, Dict, Optional
from . import local_mod
from ..pkg.sub import util

TOP = 1

def free_function(x, y=1):
    """docstring with def inside"""
    return x + y

async def async_func(url: str) -> str:
    return await fetch(url)

class Widget:
    DEFAULT = 42
    
    def __init__(self, name):
        self.name = name
    
    def method(self, y):
        s = "class Fake:"  # string containing class
        r = r'def trap():'  # raw string
        f = f"hello {name}"
        return s + r
    
    @staticmethod
    def static_method(x):
        return x
    
    @classmethod
    async def cls_method(cls):
        pass

class Child(Widget, metaclass=Meta):
    def override(self):
        def nested_helper():
            return 1
        return nested_helper()

def outer():
    def inner():
        pass
    return inner

class Container:
    # @property decorator — the decorator line is skipped and the def is
    # recorded as a method under its declared name.
    @property
    def value(self):
        return self._value

    # Nested class inside a class — recorded as a class with Container as
    # parent.
    class Inner:
        def inner_method(self):
            pass

def with_match(x):
    # match/case statement (Python 3.10+) — negative test. 'match' and
    # 'case' are soft keywords in Python; the parser must not produce
    # symbols for them or for identifiers in case patterns.
    match x:
        case 1:
            pass
        case 2:
            pass
`)
	r := ParsePython(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"function", "free_function"},
		{"function", "async_func"},
		{"class", "Widget"},
		{"method", "__init__"},
		{"method", "method"},
		{"method", "static_method"},
		{"method", "cls_method"},
		{"class", "Child"},
		{"method", "override"},
		{"method", "nested_helper"},
		{"function", "outer"},
		{"method", "inner"},
		{"class", "Container"},
		// @property decorator: the decorator is skipped, method is recorded normally.
		{"method", "value"},
		// Nested class: recorded as a class with Container as parent.
		{"class", "Inner"},
		{"method", "inner_method"},
		// match/case: does not produce symbols (match/case are soft keywords).
		{"function", "with_match"},
	}
	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q parent=%d", i, s.Type, s.Name, s.Parent)
		}
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

	// String and docstring contents must not produce symbols.
	for _, s := range r.Symbols {
		if s.Name == "Fake" || s.Name == "trap" {
			t.Errorf("spurious symbol from string/docstring: %+v", s)
		}
	}

	wantPaths := []string{"os", "sys", "collections.abc", "typing", ".", "..pkg.sub"}
	if len(r.Imports) != len(wantPaths) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantPaths))
	}
	for i, wp := range wantPaths {
		if i >= len(r.Imports) {
			break
		}
		if r.Imports[i].Path != wp {
			t.Errorf("import %d: got %q want %q", i, r.Imports[i].Path, wp)
		}
	}
}