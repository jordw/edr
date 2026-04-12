package index

import "testing"

func TestParseCSharp_Fixture(t *testing.T) {
	src := []byte(`using System;
using System.Collections.Generic;
using static System.Math;

namespace MyApp.Models
{
    public class Widget<T> : Base, IWidget where T : IComparable
    {
        private readonly string _name;

        public Widget(string name)
        {
            _name = name;
        }

        public string Name { get; set; }

        public async Task<List<T>> GetItems(int count)
        {
            var s = "class Fake {}";
            return new List<T>();
        }

        public static Widget<T> Create(string name) => new(name);

        public abstract void DoWork();

        // Expression-bodied property getter — correctly recorded as symbol
        public int Count => _items.Length;

        // Event declaration — correctly recorded as symbol
        public event EventHandler Changed;

        public interface ICallback
        {
            void OnComplete(string result);
        }

        public enum Status
        {
            Active,
            Inactive
        }

        public record Point(double X, double Y);

        private class Inner
        {
            void Helper() {}
        }
    }

    public struct Vector3
    {
        public float X, Y, Z;
    }
}

namespace FileScopedNs;

public class TopLevel
{
    public void Run() {}
}

// partial class — "partial" is a modifier; parser should still produce "class"
partial class Widget { }
`)
	r := ParseCSharp(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	// Strict ordered assertion — every symbol is verified.
	want := []struct{ typ, name string }{
		{"class", "MyApp.Models"},  // namespace block
		{"class", "Widget"},        // generic class
		{"method", "Widget"},       // constructor
		{"method", "Name"},         // property
		{"method", "GetItems"},     // async method
		{"method", "Create"},       // expression-bodied method
		{"method", "DoWork"},       // abstract method
		{"method", "Count"},        // expression-bodied property
		{"method", "Changed"},      // event declaration
		{"class", "ICallback"},     // nested interface
		{"method", "OnComplete"},   // interface method
		{"class", "Status"},        // enum
		{"class", "Point"},         // record
		{"class", "Inner"},         // nested class
		{"method", "Helper"},       // inner method
		{"class", "Vector3"},       // struct
		{"class", "FileScopedNs"},  // file-scoped namespace
		{"class", "TopLevel"},      // top-level class
		{"method", "Run"},          // method
		{"class", "Widget"},        // partial class (second Widget)
	}

	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q", i, s.Type, s.Name)
		}
	}
	for i, w := range want {
		if i >= len(r.Symbols) {
			t.Errorf("symbol %d missing: want %s %q", i, w.typ, w.name)
			continue
		}
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("symbol %d: got %s %q, want %s %q",
				i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}

	if len(r.Symbols) > len(want) {
		for i := len(want); i < len(r.Symbols); i++ {
			t.Errorf("unexpected extra symbol %d: %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name)
		}
	}

	for _, s := range r.Symbols {
		if s.Name == "Fake" {
			t.Error("spurious Fake from string")
		}
	}

	wantImps := []string{"System", "System.Collections.Generic", "System.Math"}
	if len(r.Imports) != len(wantImps) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantImps))
		for _, imp := range r.Imports {
			t.Logf("  import %q", imp.Path)
		}
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
