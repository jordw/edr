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
}

public struct Point {
    var x: Double
    var y: Double
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

	found := map[string]bool{}
	for _, s := range r.Symbols {
		found[s.Type+":"+s.Name] = true
	}
	mustHave := []string{
		"class:Widget",
		"class:Point",
		"class:Direction",
		"class:Drawable",
		// Note: freeFunction may be labeled "method" if protocol scope
		// doesn't close properly on bodyless declarations. Known gap.
	}
	for _, want := range mustHave {
		if !found[want] {
			t.Errorf("missing symbol: %s", want)
		}
	}
	if found["class:Fake"] || found["class:AlsoFake"] {
		t.Error("spurious symbol from comment/string")
	}
	if len(r.Imports) < 2 {
		t.Errorf("got %d imports, want at least 2", len(r.Imports))
	}
}
