package index

import (
	"os"
	"testing"
)

// TestGroundTruth2_Ruby verifies the Ruby parser against subscriber_map.rb.
func TestGroundTruth2_Ruby(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/rails/actioncable/lib/action_cable/subscription_adapter/subscriber_map.rb")
	if err != nil {
		t.Skip("file not available")
	}
	r := ParseRuby(src)
	want := []struct{ typ, name string }{
		{"module", "ActionCable"},
		{"module", "SubscriptionAdapter"},
		{"class", "SubscriberMap"},
		{"method", "initialize"},
		{"method", "add_subscriber"},
		{"method", "remove_subscriber"},
		{"method", "broadcast"},
		{"method", "add_channel"},
		{"method", "remove_channel"},
		{"method", "invoke_callback"},
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

// TestGroundTruth2_TypeScript verifies the TypeScript parser against
// partialCommandDetectionCapability.ts.
func TestGroundTruth2_TypeScript(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/vscode/src/vs/platform/terminal/common/capabilities/partialCommandDetectionCapability.ts")
	if err != nil {
		t.Skip("file not available")
	}
	r := ParseTS(src)
	// File declares:
	//   - `const enum Constants` (TS compile-time enum) → type "enum"
	//   - export class PartialCommandDetectionCapability → type "class"
	//   - get commands() → type "method"
	//   - constructor → type "method"
	//   - private _onData() → type "method"
	//   - private _onEnter() → type "method"
	//   - private _clearCommandsInViewport() → type "method"
	want := []struct{ typ, name string }{
		{"enum", "Constants"},
		{"class", "PartialCommandDetectionCapability"},
		{"method", "commands"},
		{"method", "constructor"},
		{"method", "_onData"},
		{"method", "_onEnter"},
		{"method", "_clearCommandsInViewport"},
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

// TestGroundTruth2_C verifies the C parser against timekeeping_internal.h.
//
// The header uses #ifdef / #else / #endif for conditional compilation; the
// parser is text-based and does NOT evaluate preprocessor conditions, so
// symbols from both branches are recorded. Concretely:
//
//   - DECLARE_PER_CPU macro call is parsed as a function declaration (sawParen
//     + semicolon path in tryParseDeclaration).
//   - timekeeping_inc_mg_floor_swaps appears in BOTH the #ifdef and #else
//     branches, so it is recorded twice.
//   - extern tk_debug_account_sleep_time() is a function declaration.
//   - The #define tk_debug_account_sleep_time(x) in the #else branch is a
//     preprocessor directive: skipped, no symbol recorded.
func TestGroundTruth2_C(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/linux/kernel/time/timekeeping_internal.h")
	if err != nil {
		t.Skip("file not available")
	}
	r := ParseCpp(src)
	want := []struct{ typ, name string }{
		// #ifdef CONFIG_DEBUG_FS branch
		{"function", "DECLARE_PER_CPU"},
		{"function", "timekeeping_inc_mg_floor_swaps"},
		{"function", "tk_debug_account_sleep_time"},
		// #else branch: duplicate static inline (no body variant counts too)
		{"function", "timekeeping_inc_mg_floor_swaps"},
		// After #endif
		{"function", "clocksource_delta"},
		{"function", "timekeeper_lock_irqsave"},
		{"function", "timekeeper_unlock_irqrestore"},
		{"function", "ktime_get_ntp_seconds"},
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

// TestGroundTruth2_CSharp verifies the C# parser against XmlLocation.cs.
//
// The parser records the enclosing namespace "Microsoft.CodeAnalysis" as a
// "class" (the C# parser uses "class" for namespace blocks), then the actual
// class XmlLocation and all its methods (including two overloads of Create and
// two overloads of Equals).
func TestGroundTruth2_CSharp(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/roslyn/src/Compilers/Core/Portable/Diagnostic/XmlLocation.cs")
	if err != nil {
		t.Skip("file not available")
	}
	r := ParseCSharp(src)
	want := []struct{ typ, name string }{
		{"class", "Microsoft.CodeAnalysis"},
		{"class", "XmlLocation"},
		{"method", "XmlLocation"},
		{"method", "Create"},
		{"method", "Create"},
		{"method", "Kind"},
		{"method", "GetLineSpan"},
		{"method", "Equals"},
		{"method", "Equals"},
		{"method", "GetHashCode"},
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

// TestGroundTruth2_Kotlin verifies the Kotlin parser against
// ClassNameCollectionClassBuilderFactory.kt.
//
// The file declares an abstract class at the top level and a private inner
// class. The parser records the outer class methods (handleClashingNames,
// newClassBuilder) and inner class methods (getDelegate, defineClass, done)
// as "function" because they appear outside a class body scope or because the
// Kotlin parser uses "function" for all fun declarations regardless of nesting.
func TestGroundTruth2_Kotlin(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/kotlin/compiler/backend/src/org/jetbrains/kotlin/codegen/ClassNameCollectionClassBuilderFactory.kt")
	if err != nil {
		t.Skip("file not available")
	}
	r := ParseKotlin(src)
	want := []struct{ typ, name string }{
		{"class", "ClassNameCollectionClassBuilderFactory"},
		{"function", "handleClashingNames"},
		{"function", "newClassBuilder"},
		{"class", "ClassNameCollectionClassBuilder"},
		{"function", "getDelegate"},
		{"function", "defineClass"},
		{"function", "done"},
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

// TestGroundTruth2_Swift verifies the Swift parser against ErrorSource.swift.
//
// The file declares:
//   - struct ErrorSource with an init method
//   - extension ErrorSource (recorded as "impl") with a static func capture
func TestGroundTruth2_Swift(t *testing.T) {
	src, err := os.ReadFile("/Users/jordw/Documents/GitHub/vapor/Sources/Vapor/Error/ErrorSource.swift")
	if err != nil {
		t.Skip("file not available")
	}
	r := ParseSwift(src)
	want := []struct{ typ, name string }{
		{"class", "ErrorSource"},
		{"method", "init"},
		{"impl", "ErrorSource"},
		{"function", "capture"},
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
