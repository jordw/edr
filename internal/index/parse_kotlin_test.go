package index

import "testing"

func TestParseKotlin_Fixture(t *testing.T) {
	src := []byte(`package com.example.app

import kotlin.collections.List
import kotlin.collections.Map
import java.io.File as JFile

// class Fake — should not match
/* class AlsoFake */

@SomeAnnotation
data class Widget<T : Comparable<T>>(
    val name: String,
    val value: Int
) : Serializable {

    fun doSomething(x: Int, y: Int) {
        val s = "class Fake { fun trap() }"
        println(s)
        val tmpl = "Hello $name world ${x + y}"
        println(tmpl)
    }

    override fun toString(): String = name

    companion object {
        fun create(name: String): Widget<String> = Widget(name, 0)
    }

    interface Callback {
        fun onEvent(event: String)
    }

    enum class Status {
        ACTIVE, INACTIVE;

        fun label(): String = name.lowercase()
    }

    private class Inner {
        fun helper() {}
    }

    sealed class Result {
        data class Success(val data: String) : Result()
        data class Failure(val error: String) : Result()
    }
}

// Top-level function
fun topLevel(x: Int): Int = x * 2

// Extension function
fun String.shout(): String = uppercase()
`)
	r := ParseKotlin(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"class", "Widget"},
		{"function", "doSomething"},
		{"function", "toString"},
		{"class", "Companion"},
		{"function", "create"},
		{"interface", "Callback"},
		{"function", "onEvent"},
		{"class", "Status"},
		{"function", "label"},
		{"class", "Inner"},
		{"function", "helper"},
		{"class", "Result"},
		{"class", "Success"},
		{"class", "Failure"},
		{"function", "topLevel"},
		{"function", "shout"},
	}
	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q", i, s.Type, s.Name)
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

	for _, s := range r.Symbols {
		if s.Name == "Fake" || s.Name == "AlsoFake" || s.Name == "trap" {
			t.Errorf("spurious symbol: %+v", s)
		}
	}

	wantImps := []string{
		"kotlin.collections.List",
		"kotlin.collections.Map",
		"java.io.File",
	}
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

func TestParseKotlin_RawString(t *testing.T) {
	// Raw strings should not confuse the parser.
	src := []byte(`package test

fun example(): String {
    val raw = """
        class FakeInString {
            fun trapFun() {}
        }
    """
    return raw
}
`)
	r := ParseKotlin(src)
	for _, s := range r.Symbols {
		if s.Name == "FakeInString" || s.Name == "trapFun" {
			t.Errorf("symbol inside raw string leaked: %+v", s)
		}
	}
	found := false
	for _, s := range r.Symbols {
		if s.Name == "example" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected symbol 'example' not found; got %+v", r.Symbols)
	}
}

func TestParseKotlin_SealedInterface(t *testing.T) {
	src := []byte(`package test

sealed interface Shape {
    data class Circle(val radius: Double) : Shape
    data class Rectangle(val w: Double, val h: Double) : Shape
}
`)
	r := ParseKotlin(src)
	names := make(map[string]string)
	for _, s := range r.Symbols {
		names[s.Name] = s.Type
	}
	if names["Shape"] != "class" {
		t.Errorf("Shape: want class, got %q", names["Shape"])
	}
	if names["Circle"] != "class" {
		t.Errorf("Circle: want class, got %q", names["Circle"])
	}
	if names["Rectangle"] != "class" {
		t.Errorf("Rectangle: want class, got %q", names["Rectangle"])
	}
}
