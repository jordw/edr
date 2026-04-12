package index

import "testing"

func TestParseJava_Fixture(t *testing.T) {
	src := []byte(`package com.example.app;

import java.util.List;
import java.util.Map;
import static java.util.Collections.emptyList;

// class Fake — should not match
/* class AlsoFake */

public class Widget<T extends Comparable<T>> extends Base implements Serializable {

    private static final int MAX = 100;
    private String name;

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

    public interface Callback {
        void onEvent(String event);
    }

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
		{"method", "Widget"},       // constructor
		{"method", "doSomething"},
		{"method", "toString"},
		{"method", "wrap"},
		{"method", "abstractMethod"},
		{"interface", "Callback"},
		{"method", "onEvent"},
		{"enum", "Status"},
		{"method", "label"},
		{"class", "Point"},         // record
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
	}

	wantImps := []string{"java.util.List", "java.util.Map", "java.util.Collections.emptyList"}
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