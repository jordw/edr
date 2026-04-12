package index

import "testing"

func TestParseRuby_Fixture(t *testing.T) {
	src := []byte(`# top comment
require 'json'
require_relative '../lib/foo'

module Outer
  class Thing < Base
    SOMETHING = "class Fake  # this must not count"

    def initialize(name)
      @name = name
    end

    def self.build(*args)
      new(*args)
    end

    def size = @items.length

    def query(sql)
      result = connection.execute(<<~SQL)
        SELECT *
        FROM users
        WHERE name = 'def foo; end'
      SQL
      result
    end

    def divide(a, b)
      a / b / 2
    end

    def match?(s)
      s =~ /class \w+/
    end

    def each
      [1, 2, 3].each do |x|
        puts x if x > 1
      end
    end
  end

  module Helpers
    def self.noop; end
  end
end
`)
	r := ParseRuby(src)

	want := []struct {
		name string
		typ  string
	}{
		{"Outer", "module"},
		{"Thing", "class"},
		{"initialize", "method"},
		{"build", "method"},
		{"size", "method"},
		{"query", "method"},
		{"divide", "method"},
		{"match?", "method"},
		{"each", "method"},
		{"Helpers", "module"},
		{"noop", "method"},
	}
	if len(r.Symbols) != len(want) {
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
		}
	}
	for i, w := range want {
		if i >= len(r.Symbols) {
			break
		}
		if r.Symbols[i].Name != w.name || r.Symbols[i].Type != w.typ {
			t.Errorf("symbol %d: got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
	for _, s := range r.Symbols {
		if s.Name == "Fake" || s.Name == "foo" {
			t.Errorf("spurious symbol from string/heredoc: %+v", s)
		}
	}

	wantImps := []RubyImport{
		{Kind: "require", Path: "json"},
		{Kind: "require_relative", Path: "../lib/foo"},
	}
	if len(r.Imports) != len(wantImps) {
		t.Fatalf("got %d imports, want %d: %+v", len(r.Imports), len(wantImps), r.Imports)
	}
	for i, w := range wantImps {
		if r.Imports[i].Kind != w.Kind || r.Imports[i].Path != w.Path {
			t.Errorf("import %d: got %+v, want %+v", i, r.Imports[i], w)
		}
	}

	// Parent relationships
	for i, s := range r.Symbols {
		t.Logf("[%d] %s %s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
}