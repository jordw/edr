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
		// Scala (.scala)
		{"Main.scala", "object Main { def main(): Unit = {} }\n"},
		// Scala (.sc)
		{"script.sc", "val x = 1\n"},
		// Kotlin (.kt)
		{"Main.kt", "fun main() {}\n"},
		// Kotlin (.kts)
		{"build.kts", "val x = 1\n"},
		// Elixir (.ex)
		{"main.ex", "defmodule Main do\n  def hello, do: :ok\nend\n"},
		// Elixir (.exs)
		{"test.exs", "defmodule Test do\nend\n"},
		// HTML (.html)
		{"index.html", "<html><body></body></html>\n"},
		// HTML (.htm)
		{"index.htm", "<html><body></body></html>\n"},
		// CSS
		{"style.css", "body { color: red; }\n"},
		// JSON
		{"data.json", "{\"key\": \"value\"}\n"},
		// YAML (.yaml)
		{"config.yaml", "key: value\n"},
		// YAML (.yml)
		{"config.yml", "key: value\n"},
		// TOML
		{"config.toml", "[section]\nkey = \"value\"\n"},
		// Markdown (.md)
		{"README.md", "# Hello\nWorld\n"},
		// Markdown (.markdown)
		{"doc.markdown", "# Hello\nWorld\n"},
		// Protobuf
		{"service.proto", "syntax = \"proto3\";\nmessage Msg {}\n"},
		// SQL
		{"schema.sql", "CREATE TABLE users (id INT);\n"},
		// HCL (.hcl)
		{"main.hcl", "resource \"null\" \"x\" {}\n"},
		// HCL (.tf)
		{"main.tf", "resource \"null\" \"x\" {}\n"},
		// HCL (.tfvars)
		{"vars.tfvars", "region = \"us-east-1\"\n"},
		// Dockerfile (with extension)
		{"app.dockerfile", "FROM alpine\nRUN echo hi\n"},
		// Dockerfile (no extension, default case)
		{"Dockerfile", "FROM alpine\nRUN echo hi\n"},
		// Dockerfile with suffix (no extension, default case)
		{"Dockerfile.dev", "FROM alpine\nRUN echo hi\n"},
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
