package index

import "testing"

func TestParseSwift_Fixture(t *testing.T) {
	src := []byte(`import Foundation
import UIKit

// class Fake
/* struct AlsoFake */

public class Widget<T: Comparable>: Base, Codable {
    private let name: String

    public init(name: String) {
        self.name = name
    }

    deinit {
        print("bye")
    }

    public func doWork(x: Int) -> String {
        let s = "class Fake {}"
        return s
    }

    static func create(name: String) -> Widget {
        return Widget(name: name)
    }

    public override func description() -> String {
        return name
    }

    // subscript — correctly recorded as symbol
    subscript(index: Int) -> T {
        return items[index]
    }
}

public struct Point {
    var x: Double
    var y: Double

    // computed property (known gap: not recorded as symbol)
    var area: Double { return x * y }
}

public enum Direction {
    case north, south, east, west

    func label() -> String {
        return String(describing: self)
    }
}

public protocol Drawable {
    func draw()
}

extension Widget: Drawable {
    func draw() {}
}

func freeFunction() -> Int {
    return 42
}
`)
	r := ParseSwift(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	// Strict ordered assertion — every symbol is verified.
	// Known gaps documented inline:
	//   - subscript inside Widget: not recorded
	//   - computed property (var area) inside Point: not recorded
	want := []struct{ typ, name string }{
		{"class", "Widget"},
		{"method", "init"},
		{"method", "deinit"},
		{"function", "doWork"},
		{"function", "create"},
		{"function", "description"},
		{"function", "subscript"},     // subscript declaration
		{"class", "Point"},
		// var area — known gap: not recorded
		{"class", "Direction"},
		{"function", "label"},
		{"class", "Drawable"},
		{"function", "draw"},          // protocol requirement (bodyless — terminates at newline)
		{"impl", "Widget"},            // extension Widget: Drawable
		{"function", "draw"},          // extension method
		{"function", "freeFunction"},  // top-level, parent=-1
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
		if s.Name == "Fake" || s.Name == "AlsoFake" {
			t.Errorf("spurious symbol from comment/string: %+v", s)
		}
	}

	if len(r.Imports) != 2 {
		t.Errorf("got %d imports, want 2", len(r.Imports))
		for _, imp := range r.Imports {
			t.Logf("  import %q", imp.Path)
		}
	}
	wantImps := []string{"Foundation", "UIKit"}
	for i, w := range wantImps {
		if i >= len(r.Imports) {
			break
		}
		if r.Imports[i].Path != w {
			t.Errorf("import %d: got %q want %q", i, r.Imports[i].Path, w)
		}
	}
}
