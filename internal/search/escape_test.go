package search

import "testing"

func TestStripRegexEscapes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Agent-style escaped patterns
		{`map\[string\]`, `map[string]`},
		{`db\.Root`, `db.Root`},
		{`interface\{\}`, `interface{}`},
		{`foo\(bar\)`, `foo(bar)`},
		{`a\+b`, `a+b`},
		{`end\$`, `end$`},
		{`\^start`, `^start`},
		{`a\|b`, `a|b`},
		{`star\*`, `star*`},
		{`q\?`, `q?`},
		{`back\\slash`, `back\slash`},

		// Patterns without escapes — unchanged
		{"plain text", "plain text"},
		{"func main()", "func main()"},
		{"no escapes here", "no escapes here"},

		// Non-metacharacter escapes — preserved (e.g. \n, \t, \w)
		{`\n`, `\n`},
		{`\t`, `\t`},
		{`\w+`, `\w+`},
		{`\d`, `\d`},

		// Mixed
		{`db\.Get\[T\]`, `db.Get[T]`},
		{`fmt\.Fprintf\(`, `fmt.Fprintf(`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripRegexEscapes(tt.input)
			if got != tt.want {
				t.Errorf("stripRegexEscapes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripRegexEscapes_Idempotent(t *testing.T) {
	// Stripping twice should give the same result as stripping once
	patterns := []string{`map\[string\]`, `db\.Root`, "plain", `\\escaped`}
	for _, p := range patterns {
		once := stripRegexEscapes(p)
		twice := stripRegexEscapes(once)
		if once != twice {
			t.Errorf("not idempotent for %q: once=%q, twice=%q", p, once, twice)
		}
	}
}
