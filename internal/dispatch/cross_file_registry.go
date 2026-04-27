package dispatch

import (
	"context"

	"github.com/jordw/edr/internal/index"
)

// crossFileRenamerResult is what a per-language cross-file renamer returns.
// commit signals "I have a definitive answer for this language — early
// return"; when commit is false, scopeAwareCrossFileSpans falls through
// to the generic ref-filtering path.
type crossFileRenamerResult struct {
	spans    map[string][]span
	warnings []string
	commit   bool
}

type crossFileRenamerFn func(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult

// crossFileRenamers maps file extension → language-specific renamer.
// Each wrapper encapsulates the commit policy for its language: most
// commit on any non-error result, but TS/JS, Python, C++, C#, Swift,
// PHP only commit when the namespace resolver actually produced cross-
// file matches (otherwise the generic path catches CommonJS / star-
// imports / patterns the namespace model doesn't see). Ruby commits
// when there are cross-file matches OR the rename target is a method
// (legacy fallback would silently rewrite same-name calls on unrelated
// classes for methods).
var crossFileRenamers = map[string]crossFileRenamerFn{
	".go":     goRenamer,
	".java":   javaRenamer,
	".kt":     kotlinRenamer,
	".kts":    kotlinRenamer,
	".rs":     rustRenamer,
	".c":      cRenamer,
	".h":      cRenamer,
	".ts":     tsRenamer,
	".tsx":    tsRenamer,
	".js":     tsRenamer,
	".jsx":    tsRenamer,
	".mjs":    tsRenamer,
	".cjs":    tsRenamer,
	".mts":    tsRenamer,
	".cts":    tsRenamer,
	".d.ts":   tsRenamer,
	".py":     pythonRenamer,
	".pyi":    pythonRenamer,
	".rb":     rubyRenamer,
	".lua":    luaRenamer,
	".zig":    zigRenamer,
	".cpp":    cppRenamer,
	".cxx":    cppRenamer,
	".cc":     cppRenamer,
	".c++":    cppRenamer,
	".hpp":    cppRenamer,
	".hxx":    cppRenamer,
	".hh":     cppRenamer,
	".h++":    cppRenamer,
	".cs":     csharpRenamer,
	".swift":  swiftRenamer,
	".php":    phpRenamer,
	".phtml":  phpRenamer,
}

func goRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := goCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func javaRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := javaCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func kotlinRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := kotlinCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func rustRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	// Only use namespace-driven Rust path when Cargo metadata is
	// reachable. Otherwise fall through so the generic path can use
	// rustSameCrateRefs (which preserves the shadow guard for fixtures
	// without Cargo.toml).
	spans, ok := rustCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func cRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := cCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

// tsRenamer commits only when the namespace resolver finds cross-file
// matches. CommonJS `require(...)` destructuring and bare `module.exports`
// patterns aren't modeled, so an empty result must fall through to the
// generic ref-filtering path.
func tsRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := tsCrossFileSpans(ctx, db, sym)
	if !ok || len(spans) == 0 {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

// pythonRenamer follows the same fall-through rule as TS/JS: let the
// generic path handle bare `import X`, star imports, etc.
func pythonRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := pythonCrossFileSpans(ctx, db, sym)
	if !ok || len(spans) == 0 {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

// rubyRenamer commits when the resolver produces a result OR when the
// rename target is a method. Methods always need the receiver-aware path;
// the legacy fallback would silently rewrite same-name calls on unrelated
// classes. Top-level (non-method) renames with no cross-file matches fall
// through so the legacy path can still handle imports the namespace
// resolver doesn't model.
func rubyRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, warns, ok := rubyCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	if len(spans) > 0 || sym.Receiver != "" {
		return crossFileRenamerResult{spans: spans, warnings: warns, commit: true}
	}
	return crossFileRenamerResult{}
}

// luaRenamer always commits — Lua has no static type info or trackable
// imports; the resolver walks every .lua file unconditionally. The legacy
// fallback is import-graph filtered and would miss `dofile`/`require`
// callers the walk picks up.
func luaRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, warns, ok := luaCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, warnings: warns, commit: true}
}

// zigRenamer always commits — `@import("foo.zig")` is not in the import
// graph, so we walk every .zig file and emit name-matched refs.
func zigRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, warns, ok := zigCrossFileSpans(ctx, db, sym)
	if !ok {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, warnings: warns, commit: true}
}

func cppRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := cppCrossFileSpans(ctx, db, sym)
	if !ok || len(spans) == 0 {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func csharpRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := csharpCrossFileSpans(ctx, db, sym)
	if !ok || len(spans) == 0 {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func swiftRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := swiftCrossFileSpans(ctx, db, sym)
	if !ok || len(spans) == 0 {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}

func phpRenamer(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) crossFileRenamerResult {
	spans, ok := phpCrossFileSpans(ctx, db, sym)
	if !ok || len(spans) == 0 {
		return crossFileRenamerResult{}
	}
	return crossFileRenamerResult{spans: spans, commit: true}
}
