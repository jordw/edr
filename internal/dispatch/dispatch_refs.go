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
	"github.com/jordw/edr/internal/scope/python"
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
	case ".py", ".pyi":
		result = python.Parse(relFile, src)
	default:
		return nil, fmt.Errorf("refs-to: unsupported language %q (currently supports .ts/.tsx/.js/.jsx, .go, .py)", ext)
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

	// Property-access refs (obj.name): when the target is a method, field,
	// or function likely accessed via property (common for package-qualified
	// Go calls `pkg.Foo()` and class methods), add probable matches by name.
	// These are name-only — they match any same-named method on any object —
	// so they're tagged as probable. Skip if the target is clearly not an
	// access target (e.g., a local var).
	if propertyAccessLikelyTarget(decl) {
		for _, ref := range result.Refs {
			if ref.Name != symbolName {
				continue
			}
			if ref.Binding.Reason != "property_access" {
				continue
			}
			line, col := byteToLineCol(lines, ref.Span.StartByte)
			entries = append(entries, refEntry{
				file:   output.Rel(absFile),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: "property_access",
				kind:   scope.BindProbable,
			})
		}
	}

	// Cross-file: for file-scope decls only. Go walks siblings in the
	// same directory (package scope) for unresolved refs, AND walks the
	// whole repo for `pkg.Name` property-access call sites (cross-
	// package invocations of exported Go symbols). TS/JS walks all TS
	// files under the repo root and filters by name matching an Import
	// decl OR property-access use of the name.
	if decl.Scope == 1 {
		switch ext {
		case ".go":
			entries = append(entries, goCrossFileRefs(absFile, symbolName)...)
			// Walk the whole repo for cross-package property_access refs
			// ONLY for exported (capitalized) symbols — unexported refs
			// are package-internal and already covered by the sibling walk.
			if len(symbolName) > 0 && symbolName[0] >= 'A' && symbolName[0] <= 'Z' {
				entries = append(entries, goCrossPackagePropRefs(root, absFile, symbolName)...)
			}
		case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts":
			entries = append(entries, tsCrossFileRefs(root, absFile, symbolName)...)
		case ".py", ".pyi":
			entries = append(entries, pyCrossFileRefs(root, absFile, symbolName)...)
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
			// Accept: (a) unresolved (likely cross-package-scope candidate),
			// (b) property_access probable (pkg.Name call sites — the
			// canonical Go cross-package access pattern).
			reason := ""
			kind := scope.BindProbable
			if ref.Binding.Kind == scope.BindUnresolved {
				reason = "cross_file_same_package"
			} else if ref.Binding.Reason == "property_access" {
				reason = "property_access"
			} else {
				// Locally bound to something else, or a builtin — skip.
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(sibPath),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   kind,
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

// goCrossPackagePropRefs walks all .go files under root (outside the
// origin's package directory) and collects property-access refs whose
// name matches the target. These are the `pkg.Name` call sites for
// exported cross-package Go symbols — imprecise (we don't verify the
// package alias actually points at origin's package), but high-signal
// in practice for capitalized names.
func goCrossPackagePropRefs(root, originFile, name string) []refEntry {
	originDir := filepath.Dir(originFile)
	var out []refEntry
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "vendor" || n == "node_modules" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Skip files in the origin's own directory (already covered by
		// goCrossFileRefs).
		if filepath.Dir(path) == originDir {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := golang.Parse(path, src)
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name || ref.Binding.Reason != "property_access" {
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: "cross_package_property",
				kind:   scope.BindProbable,
			})
		}
		return nil
	})
	return out
}

// pyCrossFileRefs walks .py files under root and collects refs to
// `name` that are unresolved (likely cross-module imports not followed)
// or property-access probable (`pkg.name` call sites). Heuristic but
// high-signal — Python import resolution (dotted paths, from..import,
// relative imports) is deferred.
func pyCrossFileRefs(root, originFile, name string) []refEntry {
	var out []refEntry
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			n := info.Name()
			if n == ".git" || n == ".edr" || n == "__pycache__" || n == ".venv" || n == "node_modules" || n == "build" || n == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if path == originFile || !strings.HasSuffix(path, ".py") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r := python.Parse(path, src)
		// Only scan files that mention the name at all.
		seen := false
		for _, ref := range r.Refs {
			if ref.Name == name {
				seen = true
				break
			}
		}
		if !seen {
			return nil
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue
			case ref.Binding.Kind == scope.BindResolved:
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				reason = "cross_file_unresolved"
			default:
				continue
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
				kind:   scope.BindProbable,
			})
		}
		return nil
	})
	return out
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
		// Does this file reference `name` at all? Check for an Import
		// decl with the name (named import) or any ref (property access
		// like `foo.Name` where foo is imported).
		hasImport := false
		hasPropertyAccess := false
		for _, d := range r.Decls {
			if d.Name == name && d.Kind == scope.KindImport {
				hasImport = true
				break
			}
		}
		if !hasImport {
			for _, ref := range r.Refs {
				if ref.Name == name && ref.Binding.Reason == "property_access" {
					hasPropertyAccess = true
					break
				}
			}
		}
		if !hasImport && !hasPropertyAccess {
			return nil
		}
		sibLines := computeLineStarts(src)
		for _, ref := range r.Refs {
			if ref.Name != name {
				continue
			}
			var reason string
			switch {
			case ref.Binding.Reason == "property_access":
				reason = "property_access"
			case ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "builtin":
				continue // builtin, not our symbol
			case ref.Binding.Kind == scope.BindResolved:
				// Resolved to the local Import decl — this is our target.
				reason = "cross_file_import"
			case ref.Binding.Kind == scope.BindUnresolved:
				// File doesn't import the name — only accept if we saw a
				// property-access match above (possible re-export context).
				if !hasPropertyAccess {
					continue
				}
				reason = "cross_file_import"
			default:
				reason = "cross_file_import"
			}
			line, col := byteToLineCol(sibLines, ref.Span.StartByte)
			out = append(out, refEntry{
				file:   output.Rel(path),
				line:   line,
				col:    col,
				span:   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
				reason: reason,
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

// propertyAccessLikelyTarget reports whether a decl might be referenced
// via `obj.name` somewhere. Returns true for things that can legitimately
// be on the RHS of a dot access: methods, fields, top-level functions
// (cross-package), classes (for static members), consts/types exported
// from modules, and import aliases (Go `pkg.Foo`). Returns false for
// purely local decls like params and local vars.
func propertyAccessLikelyTarget(d *scope.Decl) bool {
	switch d.Kind {
	case scope.KindMethod, scope.KindField:
		return true
	case scope.KindFunction, scope.KindClass, scope.KindInterface,
		scope.KindType, scope.KindConst, scope.KindImport,
		scope.KindEnum, scope.KindNamespace:
		return true
	}
	return false
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
