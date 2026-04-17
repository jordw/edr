package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/golang"
	"github.com/jordw/edr/internal/scope/ts"
)

// runRefsTo implements `edr refs-to file:Symbol`. Parses the file with
// the appropriate language scope builder, locates the symbol, and lists
// every reference bound to it. Single-file for v1; cross-file resolution
// requires the persistent scope index that isn't wired yet.
func runRefsTo(_ context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("refs-to: need exactly one argument of the form file:Symbol")
	}
	arg := args[0]
	colon := strings.LastIndex(arg, ":")
	if colon <= 0 || colon == len(arg)-1 {
		return nil, fmt.Errorf("refs-to: argument %q must be file:Symbol", arg)
	}
	relFile := arg[:colon]
	symbolName := arg[colon+1:]

	absFile := relFile
	if !filepath.IsAbs(absFile) {
		absFile = filepath.Join(root, relFile)
	}
	src, err := os.ReadFile(absFile)
	if err != nil {
		return nil, fmt.Errorf("refs-to: read %s: %w", relFile, err)
	}

	ext := strings.ToLower(filepath.Ext(absFile))
	var result *scope.Result
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		result = ts.Parse(relFile, src)
	case ".go":
		result = golang.Parse(relFile, src)
	default:
		return nil, fmt.Errorf("refs-to: unsupported language %q (currently supports .ts/.tsx/.js/.jsx and .go)", ext)
	}

	// Find the decl. Prefer file-scope match; fall back to any decl with the name.
	decl := scope.FindDeclByName(result, symbolName)
	if decl == nil {
		for i := range result.Decls {
			if result.Decls[i].Name == symbolName {
				decl = &result.Decls[i]
				break
			}
		}
	}
	if decl == nil {
		return nil, fmt.Errorf("refs-to: symbol %q not declared in %s", symbolName, relFile)
	}

	refs := scope.RefsToDecl(result, decl.ID)

	// Build per-file entries: every ref gets { file, line, col, reason }.
	lines := computeLineStarts(src)
	var entries []refEntry
	for _, ref := range refs {
		line, col := byteToLineCol(lines, ref.Span.StartByte)
		entries = append(entries, refEntry{
			file:   output.Rel(absFile),
			line:   line,
			col:    col,
			span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
			reason: ref.Binding.Reason,
			kind:   ref.Binding.Kind,
		})
	}

	// Cross-file: for file-scope decls only. Go walks siblings in the
	// same directory (package scope). TS/JS walks all TS files under
	// the repo root and filters by name matching an Import decl OR
	// being unresolved — heuristic in the absence of full import-path
	// resolution, but high precision in practice.
	if decl.Scope == 1 {
		switch ext {
		case ".go":
			entries = append(entries, goCrossFileRefs(absFile, symbolName)...)
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
			entries = append(entries, tsCrossFileRefs(root, absFile, symbolName)...)
		}
	}

	budget := flagInt(flags, "budget", 0)
	truncated := false
	if budget > 0 && len(entries) > budget {
		entries = entries[:budget]
		truncated = true
	}

	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		m := map[string]any{
			"file":   e.file,
			"line":   e.line,
			"col":    e.col,
			"span":   e.span,
			"reason": e.reason,
		}
		if e.kind != scope.BindResolved {
			m["binding"] = bindingKindName(e.kind)
		}
		out = append(out, m)
	}

	declLine, declCol := byteToLineCol(lines, decl.Span.StartByte)
	return map[string]any{
		"file": output.Rel(absFile),
		"decl": map[string]any{
			"name": decl.Name,
			"kind": string(decl.Kind),
			"line": declLine,
			"col":  declCol,
		},
		"count":     len(entries),
		"refs":      out,
		"truncated": truncated,
	}, nil
}

// goCrossFileRefs walks sibling .go files in the same directory and
// returns refs to `name` that look like cross-file references (ident
// matches our symbol and is not locally bound). Skips test files.
func goCrossFileRefs(originFile, name string) []refEntry {
	dir := filepath.Dir(originFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []refEntry
	for _, e := range entries {
		fn := e.Name()
		if !strings.HasSuffix(fn, ".go") || strings.HasSuffix(fn, "_test.go") {
			continue
		}
		sibPath := filepath.Join(dir, fn)
		if sibPath == originFile {
			continue
		}
		sibSrc, err := os.ReadFile(sibPath)
		if err != nil {
			continue
		}
		r := golang.Parse(sibPath, sibSrc)
		sibLines := computeLineStarts(sibSrc)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			// Skip locally-bound refs (local var, param, or struct field
			// of same name). Accept unresolved — these are the cross-
			// package-scope candidates.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason != "builtin" {
				continue
			}
			if ref.Binding.Reason == "builtin" {
				// If there's a builtin with this name, skip — the ref is
				// resolving to the builtin, not our symbol.
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(sibPath),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: "cross_file_same_package",
				kind:   scope.BindProbable,
			})
		}
	}
	return out
}

type refEntry struct {
	file   string
	line   int
	col    int
	span   [2]uint32
	reason string
	kind   scope.BindingKind
}

// tsCrossFileRefs walks TS-like files under root and collects refs
// whose name matches `name` and whose binding is either unresolved or
// resolves to a local Import decl with the same name. Heuristic in
// the absence of full import-path resolution; false positives are
// possible when unrelated files happen to use the same identifier as
// an imported name.
func tsCrossFileRefs(root, originFile, name string) []refEntry {
	var out []refEntry
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			// Skip heavy / irrelevant directories.
			if n == "node_modules" || n == ".git" || n == ".edr" || n == "dist" || n == "build" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
		default:
			return nil
		}
		if strings.HasSuffix(path, ".d.ts") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := ts.Parse(path, src)
		// Only consider files that have an Import decl matching the name —
		// otherwise any `Foo` reference is unrelated. This keeps the
		// heuristic high-precision.
		hasImport := false
		for _, d := range r.Decls {
			if d.Name == name && d.Kind == scope.KindImport {
				hasImport = true
				break
			}
		}
		if !hasImport {
			return nil
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			// Skip refs that resolved to something other than the Import
			// (i.e., a local shadow or builtin of same name).
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin" {
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: "cross_file_import",
				kind:   scope.BindProbable,
			})
		}
		return nil
	})
	return out
}

// computeLineStarts returns the byte offset of the start of each line.
// lines[0] = 0 (start of file).
func computeLineStarts(src []byte) []uint32 {
	starts := []uint32{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, uint32(i+1))
		}
	}
	return starts
}

// byteToLineCol returns 1-based line and column for a byte offset.
func byteToLineCol(lineStarts []uint32, b uint32) (line, col int) {
	// Binary search: find largest i such that lineStarts[i] <= b
	lo, hi := 0, len(lineStarts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if lineStarts[mid] <= b {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1, int(b-lineStarts[lo]) + 1
}

func bindingKindName(k scope.BindingKind) string {
	switch k {
	case scope.BindResolved:
		return "resolved"
	case scope.BindAmbiguous:
		return "ambiguous"
	case scope.BindProbable:
		return "probable"
	case scope.BindUnresolved:
		return "unresolved"
	}
	return "unknown"
}
