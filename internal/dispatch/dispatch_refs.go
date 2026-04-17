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
	budget := flagInt(flags, "budget", 0)
	truncated := false
	if budget > 0 && len(refs) > budget {
		refs = refs[:budget]
		truncated = true
	}

	// Convert byte spans to line numbers for human-readable output.
	lines := computeLineStarts(src)
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		line, col := byteToLineCol(lines, ref.Span.StartByte)
		entry := map[string]any{
			"line":   line,
			"col":    col,
			"span":   [2]uint32{ref.Span.StartByte, ref.Span.EndByte},
			"reason": ref.Binding.Reason,
		}
		if ref.Binding.Kind != scope.BindResolved {
			entry["binding"] = bindingKindName(ref.Binding.Kind)
		}
		out = append(out, entry)
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
		"count":     len(refs),
		"refs":      out,
		"truncated": truncated,
	}, nil
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
