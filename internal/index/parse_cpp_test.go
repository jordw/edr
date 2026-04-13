package index

import "testing"

func TestParseCpp_Fixture(t *testing.T) {
	src := []byte(`// test.cpp
#include <iostream>
#include "myheader.h"

#define MACRO(x) x * 2
// class Fake — should not match inside comment

namespace outer {
namespace inner {

class Base {
public:
    virtual ~Base() {}
    virtual void method() = 0;
};

class Widget : public Base {
public:
    Widget(int x) : value_(x) {}

    void method() override {
        std::cout << "hello" << std::endl;
    }

    static int count() { return 0; }

    int getValue() const noexcept;

    // operator overloading — known gap: operator keyword causes parser to skip
    // the declaration via skipOperatorName(); no symbol is recorded
    Widget operator+(const Widget& other) const { return *this; }

    // friend declaration — friend is treated as modifier; helperFunc is
    // recorded as a method symbol (known limitation: should be excluded)
    friend void helperFunc(Widget& w);

    // constexpr function inside class — constexpr is a modifier keyword
    constexpr int square(int x) { return x * x; }

private:
    int value_;
};

// multiple inheritance — colon and base list skipped until {
class Multi : public Base, public Interface {
public:
    void multiMethod() {}
};

// forward declaration — no { so recorded as class with EndLine set, no body
class Forward;

template<typename T>
class Container {
public:
    void add(const T& item) {}
    T get(int index) const { return T(); }
};

struct Point {
    double x, y;
};

enum Color { Red, Green, Blue };

enum class Direction : int {
    North = 0,
    South,
    East,
    West
};

using IntVec = std::vector<int>;

typedef void (*Callback)(int, int);

int freeFunction(int a, int b) {
    return a + b;
}

template<typename T>
T genericFunc(T a, T b) {
    return a + b;
}

// constexpr free function
constexpr int squareFree(int x) { return x * x; }

// C++17 nested namespace shorthand
namespace outer::middle::inner { }

const char* rawStr = R"delim(
    this has "quotes" and
    class Fake inside
)delim";

// struct used as return type — should NOT create a struct symbol
struct task_struct *fork_idle(int cpu) {
    struct task_struct *task;
    return task;
}

// struct used as variable declaration
struct rq *rq = get_rq(cpu);

} // namespace inner
} // namespace outer
`)
	r := ParseCpp(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"namespace", "outer"},
		{"namespace", "inner"},
		{"class", "Base"},
		{"method", "~Base"},
		{"method", "method"},
		{"class", "Widget"},
		{"method", "Widget"},
		{"method", "method"},
		{"method", "count"},
		{"method", "getValue"},
		// operator+ is a known gap: skipOperatorName() skips it, no symbol recorded
		{"method", "helperFunc"}, // friend decl: friend is a modifier; helperFunc treated as method declaration
		{"method", "square"},     // constexpr int square — constexpr is modifier keyword
		{"class", "Multi"},       // multiple inheritance: colon + base list skipped until {
		{"method", "multiMethod"},
		{"class", "Forward"}, // forward declaration: no {, symbol recorded with EndLine set
		{"class", "Container"},
		{"method", "add"},
		{"method", "get"},
		{"struct", "Point"},
		{"enum", "Color"},
		{"enum", "Direction"},
		{"type", "IntVec"},
		{"type", "void"},         // typedef void (*Callback)(int, int) — name inside parens, known gap
		{"function", "freeFunction"},
		{"function", "genericFunc"},
		{"function", "squareFree"},             // constexpr free function
		{"namespace", "outer::middle::inner"},  // C++17 nested namespace shorthand
		{"function", "fork_idle"},              // struct return type — not a struct definition
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
		if s.Name == "Fake" || s.Name == "MACRO" || s.Name == "value_" || s.Name == "task_struct" || (s.Name == "rq" && s.Type == "struct") {
			t.Errorf("spurious symbol: %+v", s)
		}
	}

	wantImps := []string{"iostream", "myheader.h"}
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