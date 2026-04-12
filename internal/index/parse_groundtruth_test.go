package index

import (
	"os"
	"testing"
)

// TestGroundTruth_Go verifies the Go parser against langs.go.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Go(t *testing.T) {
	src, err := os.ReadFile("langs.go")
	if err != nil {
		t.Skip("file not available")
	}

	r := ParseGo(src)

	// Expected symbols (manually verified from source)
	// langs.go declares: ContainerStyle type, three constants, langConfig struct,
	// langByExt variable, and seven exported + one unexported function.
	want := []struct{ typ, name string }{
		{"type", "ContainerStyle"},
		{"constant", "ContainerBrace"},
		{"constant", "ContainerIndent"},
		{"constant", "ContainerKeyword"},
		{"struct", "langConfig"},
		{"variable", "langByExt"},
		{"function", "langForFile"},
		{"function", "Supported"},
		{"function", "LangMethodsOutside"},
		{"function", "LangContainer"},
		{"function", "LangContainerClose"},
		{"function", "Parse"},
		{"function", "LangID"},
	}

	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth_Python verifies the Python parser against dedupe_symint_uses.py.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Python(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/pytorch/torch/_inductor/fx_passes/dedupe_symint_uses.py")
	if err != nil {
		t.Skip("file not available")
	}

	r := ParsePython(src)

	// Expected symbols (manually verified from source):
	//   _SymExprHash class with __hash__ and __eq__ methods
	//   _SymHashingDict class with __init__, __setitem__, __getitem__,
	//     __contains__, get, _wrap_to_sym_expr_hash methods
	//   dedupe_symints module-level function
	want := []struct{ typ, name string }{
		{"class", "_SymExprHash"},
		{"method", "__hash__"},
		{"method", "__eq__"},
		{"class", "_SymHashingDict"},
		{"method", "__init__"},
		{"method", "__setitem__"},
		{"method", "__getitem__"},
		{"method", "__contains__"},
		{"method", "get"},
		{"method", "_wrap_to_sym_expr_hash"},
		{"function", "dedupe_symints"},
	}

	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth_Java verifies the Java parser against AspectJAfterThrowingAdvice.java.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Java(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/spring-framework/spring-aop/src/main/java/org/springframework/aop/aspectj/AspectJAfterThrowingAdvice.java")
	if err != nil {
		t.Skip("file not available")
	}

	r := ParseJava(src)

	// Expected symbols (manually verified from source):
	//   AspectJAfterThrowingAdvice class
	//   constructor AspectJAfterThrowingAdvice
	//   methods: isBeforeAdvice, isAfterAdvice, setThrowingName
	//   invoke method — note: the parser also picks up "Throwable" from
	//     the "throws Throwable" clause at the method boundary; this is
	//     the actual parser behaviour that this test documents.
	//   shouldInvokeOnThrowing private method
	want := []struct{ typ, name string }{
		{"class", "AspectJAfterThrowingAdvice"},
		{"method", "AspectJAfterThrowingAdvice"},
		{"method", "isBeforeAdvice"},
		{"method", "isAfterAdvice"},
		{"method", "setThrowingName"},
		{"method", "invoke"},
		{"method", "shouldInvokeOnThrowing"},
	}

	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth_PHP verifies the PHP parser against BelongsToManyRelationship.php.
// Symbols manually verified by reading the source file.
func TestGroundTruth_PHP(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/laravel/src/Illuminate/Database/Eloquent/Factories/BelongsToManyRelationship.php")
	if err != nil {
		t.Skip("file not available")
	}

	r := ParsePHP(src)

	// Expected symbols (manually verified from source):
	//   BelongsToManyRelationship class
	//   Three methods: __construct, createFor, recycle
	//   (PHP parser emits class methods as "function", not "method")
	want := []struct{ typ, name string }{
		{"class", "BelongsToManyRelationship"},
		{"function", "__construct"},
		{"function", "createFor"},
		{"function", "recycle"},
	}

	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}

// TestGroundTruth_Rust verifies the Rust parser against metric_atomics.rs.
// Symbols manually verified by reading the source file.
func TestGroundTruth_Rust(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/tokio/tokio/src/util/metric_atomics.rs")
	if err != nil {
		t.Skip("file not available")
	}

	r := ParseRust(src)

	// Expected symbols (manually verified from source):
	//   MetricAtomicU64 struct + impl with methods load, store, new, add
	//     (cfg_64bit_metrics! / cfg_no_64bit_metrics! macros expand to
	//      duplicate names; the parser records each occurrence separately)
	//   MetricAtomicUsize struct + impl with methods new, load, store,
	//     increment, decrement
	want := []struct{ typ, name string }{
		{"struct", "MetricAtomicU64"},
		{"impl", "MetricAtomicU64"},
		{"function", "load"},
		{"function", "store"},
		{"function", "new"},
		{"function", "add"},
		{"function", "store"},
		{"function", "add"},
		{"function", "new"},
		{"struct", "MetricAtomicUsize"},
		{"impl", "MetricAtomicUsize"},
		{"function", "new"},
		{"function", "load"},
		{"function", "store"},
		{"function", "increment"},
		{"function", "decrement"},
	}

	if len(r.Symbols) != len(want) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s L%d", i, s.Type, s.Name, s.StartLine)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(want))
	}
	for i, w := range want {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
}
