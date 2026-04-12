package index

import "testing"

func TestParseJava_Fixture(t *testing.T) {
	src := []byte(`package com.example.app;

import java.util.List;
import java.util.Map;
import static java.util.Collections.emptyList;
import java.util.*;

// class Fake — should not match
/* class AlsoFake */

public class Widget<T extends Comparable<T>> extends Base implements Serializable {

    private static final int MAX = 100;
    private String name;

    static { System.out.println("init"); }

    public Widget(String name) {
        this.name = name;
    }

    public void doSomething(int x, int y) {
        String s = "class Fake { def trap() }";
        System.out.println(s);
    }

    @Override
    public String toString() {
        return name;
    }

    public static <U> List<U> wrap(U item) {
        return List.of(item);
    }

    public abstract void abstractMethod();

    void throwsMethod() throws IOException { }

    public interface Callback {
        void onEvent(String event);
        default void defaultMethod() { System.out.println("default"); }
    }

    // @interface (annotation type declaration) — correctly recorded as symbol
    @interface MyAnnotation { String value(); }

    public enum Status {
        ACTIVE, INACTIVE;

        public String label() { return name().toLowerCase(); }
    }

    public record Point(double x, double y) {
        public double distance() {
            return Math.sqrt(x * x + y * y);
        }
    }

    private class Inner {
        void helper() {}
    }
}
`)
	r := ParseJava(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"class", "Widget"},
		{"method", "Widget"},        // constructor
		{"method", "doSomething"},
		{"method", "toString"},
		{"method", "wrap"},
		{"method", "abstractMethod"},
		{"method", "throwsMethod"},  // throws clause — parser handles since recent fix
		{"interface", "Callback"},
		{"method", "onEvent"},
		{"method", "defaultMethod"}, // Java 8 default method in interface
		{"interface", "MyAnnotation"}, // @interface annotation type declaration
		{"method", "value"},           // annotation method inside MyAnnotation
		{"enum", "Status"},
		{"method", "label"},
		{"class", "Point"},          // record
		{"method", "distance"},
		{"class", "Inner"},
		{"method", "helper"},
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
		if s.Name == "Fake" || s.Name == "AlsoFake" || s.Name == "trap" || s.Name == "MAX" || s.Name == "name" {
			t.Errorf("spurious symbol: %+v", s)
		}
		// static initializer blocks must not produce symbols
		if s.Type == "method" && s.Name == "static" {
			t.Errorf("static initializer block recorded as symbol: %+v", s)
		}
	}

	// Wildcard import java.util.* should be captured as-is
	wantImps := []string{"java.util.List", "java.util.Map", "java.util.Collections.emptyList", "java.util.*"}
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