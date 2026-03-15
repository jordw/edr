package index

import (
	"testing"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

func TestAllLanguagesParse(t *testing.T) {
	cases := []struct {
		filename string
		source   string
	}{
		// Go
		{"main.go", "package main\nfunc main() {}\n"},
		// Python
		{"main.py", "def hello():\n    pass\n"},
		// JavaScript
		{"main.js", "function hello() {}\n"},
		// JSX
		{"app.jsx", "function App() { return <div/>; }\n"},
		// TypeScript
		{"main.ts", "function hello(): void {}\n"},
		// TSX
		{"app.tsx", "function App(): JSX.Element { return <div/>; }\n"},
		// C
		{"main.c", "int main() { return 0; }\n"},
		// C header
		{"util.h", "int add(int a, int b);\n"},
		// Rust
		{"main.rs", "fn main() {}\n"},
		// Java
		{"Main.java", "public class Main { public static void main(String[] args) {} }\n"},
		// Ruby
		{"main.rb", "def hello\n  puts 'hi'\nend\n"},
		// C++ (.cpp)
		{"main.cpp", "int main() { return 0; }\n"},
		// C++ (.cc)
		{"main.cc", "int main() { return 0; }\n"},
		// C++ (.cxx)
		{"main.cxx", "int main() { return 0; }\n"},
		// C++ (.hpp)
		{"util.hpp", "class Util {};\n"},
		// C++ (.hxx)
		{"util.hxx", "class Util {};\n"},
		// C++ (.hh)
		{"util.hh", "class Util {};\n"},
		// PHP
		{"main.php", "<?php\nfunction hello() {}\n"},
		// Zig
		{"main.zig", "pub fn main() void {}\n"},
		// Lua
		{"main.lua", "function hello()\nend\n"},
		// Bash (.sh)
		{"script.sh", "#!/bin/bash\nhello() { echo hi; }\n"},
		// Bash (.bash)
		{"script.bash", "#!/bin/bash\nhello() { echo hi; }\n"},
		// C#
		{"Main.cs", "class Main { static void Main() {} }\n"},
		// Kotlin (.kt)
		{"Main.kt", "fun main() {}\n"},
		// Kotlin (.kts)
		{"build.kts", "val x = 1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			cfg := GetLangConfig(tc.filename)
			if cfg == nil {
				t.Fatalf("GetLangConfig(%q) returned nil", tc.filename)
			}
			parser := tree_sitter.NewParser()
			if err := parser.SetLanguage(cfg.Language); err != nil {
				t.Fatalf("SetLanguage failed for %s: %v", tc.filename, err)
			}
			tree := parser.Parse([]byte(tc.source), nil)
			if tree == nil {
				t.Fatalf("Parse returned nil for %s", tc.filename)
			}
			root := tree.RootNode()
			if root.ChildCount() == 0 {
				t.Errorf("Parse produced empty tree for %s", tc.filename)
			}
			tree.Close()
		})
	}
}
