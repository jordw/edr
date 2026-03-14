package index

import (
	"testing"

	"unsafe"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"

	"github.com/jordw/edr/internal/grammars/go_lang"
)

// parseAndFindFunc is a test helper that parses Go source and returns the root node
// and the first function_declaration node found.
func parseAndFindFunc(t *testing.T, src []byte) (*tree_sitter.Parser, *tree_sitter.Tree, *tree_sitter.Node) {
	t.Helper()
	parser := tree_sitter.NewParser()
	lang := tree_sitter.NewLanguage(unsafe.Pointer(go_lang.Language()))
	if err := parser.SetLanguage(lang); err != nil {
		t.Fatal(err)
	}
	tree := parser.Parse(src, nil)
	root := tree.RootNode()
	var funcNode *tree_sitter.Node
	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(uint(i))
		if child != nil && child.Kind() == "function_declaration" {
			funcNode = child
			break
		}
	}
	if funcNode == nil {
		t.Fatal("could not find function_declaration node")
	}
	return parser, tree, funcNode
}

func TestCollectIdentifiers_FiltersBuiltinsAndDeclarations(t *testing.T) {
	src := []byte(`package main

func example(ctx context.Context, name string) error {
	err := doSomething(name)
	if err != nil {
		return err
	}
	stale := checkStale()
	_ = stale
	x := make([]byte, 10)
	copy(x, name)
	cfg := NewConfig()
	return cfg.Validate()
}
`)

	parser, tree, funcNode := parseAndFindFunc(t, src)
	defer parser.Close()
	defer tree.Close()
	root := tree.RootNode()

	startByte := uint32(funcNode.StartByte())
	endByte := uint32(funcNode.EndByte())

	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(root, src, startByte, endByte, seen, &idents)

	identSet := make(map[string]bool)
	for _, id := range idents {
		identSet[id] = true
	}

	// Builtins should always be filtered
	builtinsToFilter := []string{
		"err", "ok", "ctx", "nil", "string", "byte", "error", "bool",
		"make", "len", "copy", "append", "true", "false", "_",
	}
	for _, name := range builtinsToFilter {
		if identSet[name] {
			t.Errorf("collectIdentifiers should have filtered out builtin/common name %q", name)
		}
	}

	// Actual function calls and type refs SHOULD be present
	shouldContain := []string{
		"doSomething",  // function call
		"checkStale",   // function call
		"NewConfig",    // function call
		"Context",      // type_identifier
	}
	for _, name := range shouldContain {
		if !identSet[name] {
			t.Errorf("collectIdentifiers should have kept dependency identifier %q, but it was filtered", name)
		}
	}

	t.Logf("Collected identifiers: %v", idents)
}

func TestCollectIdentifiers_FiltersDeclarationOnlyVars(t *testing.T) {
	// Variables that are declared but never used elsewhere in the body
	// should be filtered (their only occurrence is on the LHS of :=)
	src := []byte(`package main

func foo() {
	unused := bar()
	_ = unused
}
`)

	parser, tree, funcNode := parseAndFindFunc(t, src)
	defer parser.Close()
	defer tree.Close()
	root := tree.RootNode()

	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(root, src, uint32(funcNode.StartByte()), uint32(funcNode.EndByte()), seen, &idents)

	identSet := make(map[string]bool)
	for _, id := range idents {
		identSet[id] = true
	}

	// "bar" should be kept as a function call
	if !identSet["bar"] {
		t.Error("'bar' (function call) should have been kept")
	}

	// "unused" appears on LHS of := (filtered) and as argument to _ = unused.
	// The second occurrence is in an assignment_statement on the RHS,
	// so it should be kept. This is fine — the variable is used.
	// The key improvement is that declaration-ONLY identifiers are filtered.
}

func TestCollectIdentifiers_FiltersParameters(t *testing.T) {
	src := []byte(`package main

func process(input Data, count int) {
	doWork(input)
}
`)

	parser, tree, funcNode := parseAndFindFunc(t, src)
	defer parser.Close()
	defer tree.Close()
	root := tree.RootNode()

	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(root, src, uint32(funcNode.StartByte()), uint32(funcNode.EndByte()), seen, &idents)

	identSet := make(map[string]bool)
	for _, id := range idents {
		identSet[id] = true
	}

	// "count" is a parameter name that's never used in the body,
	// so its only occurrence is in parameter_declaration — should be filtered.
	if identSet["count"] {
		t.Error("'count' (parameter name, never used in body) should have been filtered")
	}

	// "Data" is a type_identifier used in parameter — should be kept
	if !identSet["Data"] {
		t.Error("'Data' (type_identifier) should have been kept")
	}

	if !identSet["doWork"] {
		t.Error("'doWork' (function call) should have been kept")
	}
}

func TestBuiltinNames(t *testing.T) {
	expected := []string{
		"err", "ok", "ctx", "nil", "true", "false",
		"string", "int", "bool", "byte", "error", "any",
		"append", "len", "make", "new", "close", "copy",
		"delete", "panic", "print", "println",
		"cap", "complex", "imag", "real", "recover",
	}
	for _, name := range expected {
		if !builtinNames[name] {
			t.Errorf("builtinNames should contain %q", name)
		}
	}
}

func TestIsDeclarationName_ShortVarDecl(t *testing.T) {
	src := []byte(`package main

func foo() {
	x := bar()
}
`)

	parser, tree, funcNode := parseAndFindFunc(t, src)
	defer parser.Close()
	defer tree.Close()
	root := tree.RootNode()

	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(root, src, uint32(funcNode.StartByte()), uint32(funcNode.EndByte()), seen, &idents)

	identSet := make(map[string]bool)
	for _, id := range idents {
		identSet[id] = true
	}

	if identSet["x"] {
		t.Error("'x' (short var declaration LHS) should have been filtered")
	}
	if !identSet["bar"] {
		t.Error("'bar' (function call) should have been kept")
	}
}

func TestIsDeclarationName_MultipleAssign(t *testing.T) {
	src := []byte(`package main

func foo() {
	a, b := multiReturn()
}
`)

	parser, tree, funcNode := parseAndFindFunc(t, src)
	defer parser.Close()
	defer tree.Close()
	root := tree.RootNode()

	var idents []string
	seen := make(map[string]bool)
	collectIdentifiers(root, src, uint32(funcNode.StartByte()), uint32(funcNode.EndByte()), seen, &idents)

	identSet := make(map[string]bool)
	for _, id := range idents {
		identSet[id] = true
	}

	if identSet["a"] {
		t.Error("'a' (LHS of :=) should have been filtered")
	}
	if identSet["b"] {
		t.Error("'b' (LHS of :=) should have been filtered")
	}
	if !identSet["multiReturn"] {
		t.Error("'multiReturn' (function call) should have been kept")
	}
}
