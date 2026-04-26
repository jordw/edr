package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	scopestore "github.com/jordw/edr/internal/scope/store"
	scopezig "github.com/jordw/edr/internal/scope/zig"
)

// zigCrossFileSpans is the Zig branch of scopeAwareCrossFileSpans.
// Zig has @import("foo.zig") but the import graph isn't modeled in
// the trigram-driven FindSemanticReferences pipeline; walking every
// .zig file is the most direct path to picking up `lib.compute(...)`
// callers from translation units that import this file.
//
// FP risk: a same-named decl on an unrelated `pub const Other =
// struct {...}` would also be rewritten. Same loose tradeoff as Lua;
// users verify with --dry-run.
func zigCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, []string, bool) {
	if !strings.HasSuffix(strings.ToLower(sym.File), ".zig") {
		return nil, nil, false
	}
	out := map[string][]span{}
	for _, abs := range allZigFiles(db.Root(), db.EdrDir()) {
		if abs == sym.File {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		res := scopezig.Parse(abs, src)
		if res == nil {
			continue
		}
		for _, ref := range res.Refs {
			if ref.Name != sym.Name {
				continue
			}
			out[abs] = append(out[abs], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
			})
		}
		for _, d := range res.Decls {
			if d.Name != sym.Name || d.Kind == scope.KindImport {
				continue
			}
			out[abs] = append(out[abs], span{
				start: d.Span.StartByte,
				end:   d.Span.EndByte,
			})
		}
	}
	return out, nil, true
}

// allZigFiles enumerates every .zig file under root. Prefers the
// persisted scope index, falls back to a filesystem walk.
func allZigFiles(root, edrDir string) []string {
	if idx, _ := scopestore.Load(edrDir); idx != nil {
		results := idx.AllResults()
		out := make([]string, 0, len(results))
		for rel := range results {
			if !strings.HasSuffix(strings.ToLower(rel), ".zig") {
				continue
			}
			out = append(out, filepath.Join(root, rel))
		}
		if len(out) > 0 {
			return out
		}
	}
	skipDirs := map[string]bool{".git": true, ".edr": true, "zig-cache": true, "zig-out": true, ".zig-cache": true}
	var out []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".zig") {
			out = append(out, path)
		}
		return nil
	})
	return out
}
