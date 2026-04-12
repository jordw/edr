package lexkit

import (
	"bytes"
	"testing"
)

func TestScanner_PeekAndAdvance(t *testing.T) {
	s := New([]byte("abc"))
	if s.Peek() != 'a' {
		t.Error("Peek")
	}
	if s.PeekAt(1) != 'b' {
		t.Error("PeekAt(1)")
	}
	if s.PeekAt(5) != 0 {
		t.Error("PeekAt out of bounds")
	}
	if s.EOF() {
		t.Error("premature EOF")
	}
	s.Next()
	s.Next()
	s.Next()
	if !s.EOF() {
		t.Error("EOF after advance")
	}
	if s.Peek() != 0 {
		t.Error("Peek at EOF")
	}
}

func TestScanner_LineCounting(t *testing.T) {
	s := New([]byte("a\nb\nc"))
	if s.Line != 1 {
		t.Error("initial line")
	}
	s.Advance(2) // "a\n"
	if s.Line != 2 {
		t.Errorf("after a\\n: %d", s.Line)
	}
	s.Advance(2) // "b\n"
	if s.Line != 3 {
		t.Errorf("after b\\n: %d", s.Line)
	}
}

func TestScanner_StartsWith(t *testing.T) {
	s := New([]byte("foo bar"))
	if !s.StartsWith("foo") {
		t.Error("StartsWith foo")
	}
	if s.StartsWith("bar") {
		t.Error("StartsWith bar should be false")
	}
	s.Advance(4)
	if !s.StartsWith("bar") {
		t.Error("StartsWith bar after advance")
	}
}

func TestScanner_SkipLineComment(t *testing.T) {
	s := New([]byte("// hello\nnext"))
	s.Advance(2)
	s.SkipLineComment()
	if s.Peek() != '\n' {
		t.Errorf("stopped at %c", s.Peek())
	}
	if s.Line != 1 {
		t.Errorf("line moved: %d", s.Line)
	}
}

func TestScanner_SkipBlockComment(t *testing.T) {
	s := New([]byte("/* line1\nline2 */after"))
	s.Advance(2)
	s.SkipBlockComment("*/")
	if !bytes.Equal(s.Src[s.Pos:], []byte("after")) {
		t.Errorf("stopped at: %q", s.Src[s.Pos:])
	}
	if s.Line != 2 {
		t.Errorf("line: %d", s.Line)
	}
}

func TestScanner_ScanSimpleString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		rem  string
	}{
		{"basic", `"hello"rest`, `rest`},
		{"escaped_quote", `"a\"b"rest`, `rest`},
		{"newline_in_body", "\"a\nb\"rest", `rest`},
		{"unterminated", `"abc`, ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New([]byte(tt.in))
			s.ScanSimpleString('"')
			if got := string(s.Src[s.Pos:]); got != tt.rem {
				t.Errorf("got %q, want %q", got, tt.rem)
			}
		})
	}
}

func TestScanner_ScanInterpolatedString(t *testing.T) {
	s := New([]byte("`hello ${name}!`rest"))
	calls := 0
	s.ScanInterpolatedString('`', "${", func(sc *Scanner) {
		calls++
		depth := 1
		for depth > 0 && !sc.EOF() {
			switch sc.Peek() {
			case '{':
				depth++
			case '}':
				depth--
			}
			sc.Next()
		}
	})
	if calls != 1 {
		t.Errorf("callback calls: %d", calls)
	}
	if got := string(s.Src[s.Pos:]); got != "rest" {
		t.Errorf("stopped at %q", got)
	}
}

func TestScanner_ScanInterpolatedString_Nested(t *testing.T) {
	// Nested template literal: `a ${`b ${c}`}`end
	s := New([]byte("`a ${`b ${c}`}`end"))
	var onInterp func(sc *Scanner)
	onInterp = func(sc *Scanner) {
		depth := 1
		for depth > 0 && !sc.EOF() {
			c := sc.Peek()
			switch c {
			case '{':
				depth++
				sc.Next()
			case '}':
				depth--
				sc.Next()
			case '`':
				sc.ScanInterpolatedString('`', "${", onInterp)
			default:
				sc.Next()
			}
		}
	}
	s.ScanInterpolatedString('`', "${", onInterp)
	if got := string(s.Src[s.Pos:]); got != "end" {
		t.Errorf("stopped at %q", got)
	}
}

func TestScanner_ScanSlashRegex(t *testing.T) {
	tests := []struct{ in, rem string }{
		{`/abc/g rest`, ` rest`},
		{`/\//g rest`, ` rest`},
		{`/[a/b]/i rest`, ` rest`},
		{`/foo/gimsuy rest`, ` rest`},
	}
	for _, tt := range tests {
		s := New([]byte(tt.in))
		s.ScanSlashRegex()
		if got := string(s.Src[s.Pos:]); got != tt.rem {
			t.Errorf("in=%q: got %q want %q", tt.in, got, tt.rem)
		}
	}
}

func TestScanner_ScanIdent(t *testing.T) {
	s := New([]byte("foo_bar123 baz"))
	id := s.ScanIdent(IsDefaultIdentStart, IsDefaultIdentCont)
	if string(id) != "foo_bar123" {
		t.Errorf("got %q", id)
	}
	if s.Peek() != ' ' {
		t.Error("stopped wrong")
	}
	// Zero-copy check: the returned slice should share backing with Src.
	if len(id) > 0 && &id[0] != &s.Src[0] {
		t.Error("ScanIdent should be zero-copy (subslice of Src)")
	}
}

func TestScanner_ScanIdent_EmptyOnNonIdent(t *testing.T) {
	s := New([]byte("123abc"))
	id := s.ScanIdent(IsDefaultIdentStart, IsDefaultIdentCont)
	if id != nil {
		t.Errorf("expected nil, got %q", id)
	}
	if s.Pos != 0 {
		t.Error("scanner advanced on failed ident")
	}
}

func TestScanner_SkipBalanced(t *testing.T) {
	s := New([]byte(`(a("b)", c(1)))after`))
	strs := func(sc *Scanner) bool {
		if sc.Peek() == '"' {
			sc.ScanSimpleString('"')
			return true
		}
		return false
	}
	s.SkipBalanced('(', ')', strs)
	if got := string(s.Src[s.Pos:]); got != "after" {
		t.Errorf("stopped at %q", got)
	}
}

func TestScanner_SkipAngles(t *testing.T) {
	s := New([]byte(`<T extends Array<number>>rest`))
	s.SkipAngles()
	if got := string(s.Src[s.Pos:]); got != "rest" {
		t.Errorf("stopped at %q", got)
	}
}

func TestScopeStack(t *testing.T) {
	var ss ScopeStack[string]
	if ss.Depth() != 0 {
		t.Error("initial depth")
	}
	ss.Push(Scope[string]{Data: "class", SymIdx: 0, OpenLine: 1})
	ss.Push(Scope[string]{Data: "method", SymIdx: 1, OpenLine: 5})
	if ss.Depth() != 2 {
		t.Error("depth after push")
	}
	if ss.Top().Data != "method" {
		t.Error("top")
	}
	if ss.NearestSym() != 1 {
		t.Error("nearest")
	}
	if !ss.Any(func(s string) bool { return s == "class" }) {
		t.Error("Any class")
	}
	e, ok := ss.Pop()
	if !ok || e.Data != "method" {
		t.Error("pop")
	}
	if ss.NearestSym() != 0 {
		t.Error("nearest after pop")
	}
}

func TestScopeStack_EmptyPop(t *testing.T) {
	var ss ScopeStack[int]
	_, ok := ss.Pop()
	if ok {
		t.Error("pop on empty should return false")
	}
	if ss.Top() != nil {
		t.Error("top on empty should be nil")
	}
	if ss.NearestSym() != -1 {
		t.Error("NearestSym on empty should be -1")
	}
}

func TestIdentPredicates(t *testing.T) {
	cases := []struct {
		c     byte
		start bool
		cont  bool
	}{
		{'a', true, true},
		{'Z', true, true},
		{'_', true, true},
		{'0', false, true},
		{'9', false, true},
		{' ', false, false},
		{'$', false, false},
	}
	for _, tc := range cases {
		if IsDefaultIdentStart(tc.c) != tc.start {
			t.Errorf("IsDefaultIdentStart(%q)", tc.c)
		}
		if IsDefaultIdentCont(tc.c) != tc.cont {
			t.Errorf("IsDefaultIdentCont(%q)", tc.c)
		}
	}
}