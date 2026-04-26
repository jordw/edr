package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	scopelua "github.com/jordw/edr/internal/scope/lua"
	scopestore "github.com/jordw/edr/internal/scope/store"
)

// luaCrossFileSpans is the Lua branch of scopeAwareCrossFileSpans.
// Lua has no static type info and no statically tracked imports —
// `require`/`dofile` are runtime calls. Cross-file rename therefore
// walks every `.lua` file in the persisted scope index (plus a
// filesystem walk fallback) and emits spans for every ref whose Name
// matches sym.Name.
//
// FP risk: `other_table.compute` calls in unrelated files will be
// rewritten as if they referred to ours. Same tradeoff as Ruby
// loose-receiver mode for unique names. Users verify with --dry-run.
func luaCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, []string, bool) {
	if !strings.HasSuffix(strings.ToLower(sym.File), ".lua") {
		return nil, nil, false
	}
	out := map[string][]span{}
	for _, abs := range allLuaFiles(db.Root(), db.EdrDir()) {
		if abs == sym.File {
			continue
		}
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		res := scopelua.Parse(abs, src)
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

// allLuaFiles enumerates every .lua file under root. Prefers the
// persisted scope index, falls back to a filesystem walk.
func allLuaFiles(root, edrDir string) []string {
	if idx, _ := scopestore.Load(edrDir); idx != nil {
		results := idx.AllResults()
		out := make([]string, 0, len(results))
		for rel := range results {
			if !strings.HasSuffix(strings.ToLower(rel), ".lua") {
				continue
			}
			out = append(out, filepath.Join(root, rel))
		}
		if len(out) > 0 {
			return out
		}
	}
	skipDirs := map[string]bool{".git": true, ".edr": true, "node_modules": true, ".luarocks": true}
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
		if strings.HasSuffix(strings.ToLower(path), ".lua") {
			out = append(out, path)
		}
		return nil
	})
	return out
}
