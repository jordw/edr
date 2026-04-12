package index

import "testing"

func TestParseScala_Fixture(t *testing.T) {
	src := []byte(`package com.example

import scala.collection.mutable
import scala.util.{Try, Success, Failure}

// class Fake — should not match
/* trait AlsoFake */

sealed trait Result[+T] {
  def isSuccess: Boolean
}

case class Success[T](value: T) extends Result[T] {
  def isSuccess: Boolean = true
  def get: T = value
}

case object Empty extends Result[Nothing] {
  def isSuccess: Boolean = false
}

abstract class Base {
  def abstractMethod: String
}

class Widget[T](name: String) extends Base with Result[T] {
  override def abstractMethod: String = name

  def process(items: List[T]): Unit = {
    val s = "def fake() class Fake"
    println(s)
  }

  def isSuccess: Boolean = false
}

object Widget {
  def apply[T](name: String): Widget[T] = new Widget[T](name)
}

trait Serializable {
  def serialize(): Array[Byte]
}

type Callback = String => Unit

def freeFunction(x: Int): Int = x + 1
`)
	r := ParseScala(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"interface", "Result"},
		{"function", "isSuccess"},
		{"class", "Success"},
		{"function", "isSuccess"},
		{"function", "get"},
		{"class", "Empty"},
		{"function", "isSuccess"},
		{"class", "Base"},
		{"function", "abstractMethod"},
		{"class", "Widget"},
		{"function", "abstractMethod"},
		{"function", "process"},
		{"function", "isSuccess"},
		{"class", "Widget"},       // companion object
		{"function", "apply"},
		{"interface", "Serializable"},
		{"function", "serialize"},
		{"type", "Callback"},
		{"function", "freeFunction"},
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

	for _, s := range r.Symbols {
		if s.Name == "Fake" || s.Name == "AlsoFake" || s.Name == "fake" {
			t.Errorf("spurious symbol: %+v", s)
		}
	}

	wantImps := []string{"scala.collection.mutable", "scala.util."}
	if len(r.Imports) < 2 {
		t.Errorf("got %d imports, want at least 2", len(r.Imports))
	}
	for i, w := range wantImps {
		if i >= len(r.Imports) {
			break
		}
		if len(r.Imports[i].Path) < len(w) || r.Imports[i].Path[:len(w)] != w {
			t.Errorf("import %d: got %q, want prefix %q", i, r.Imports[i].Path, w)
		}
	}
}
