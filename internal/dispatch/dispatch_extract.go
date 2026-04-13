package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runExtract(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	newName := flagString(flags, "name", "")
	if newName == "" {
		return nil, fmt.Errorf("extract: --name <function_name> is required")
	}
	dryRun := flagBool(flags, "dry_run", false)
	callExpr := flagString(flags, "call", "")

	// Resolve the source symbol.
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	data, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, fmt.Errorf("extract: read %s: %w", output.Rel(sym.File), err)
	}

	// Determine the line range to extract.
	linesSpec := flagString(flags, "lines", "")
	if linesSpec == "" {
		return nil, fmt.Errorf("extract: --lines <start-end> is required")
	}
	startLine, endLine, err := parseColonRange(linesSpec)
	if err != nil {
		return nil, fmt.Errorf("extract: --lines: %w", err)
	}

	// Convert to 0-indexed line numbers.
	lines := bytes.Split(data, []byte("\n"))
	if startLine < 1 || endLine < startLine || endLine > len(lines) {
		return nil, fmt.Errorf("extract: line range %d-%d out of bounds (file has %d lines)", startLine, endLine, len(lines))
	}

	// Validate the range is within the source symbol.
	symStartLine := int(sym.StartLine)
	symEndLine := int(sym.EndLine)
	if startLine < symStartLine || endLine > symEndLine {
		return nil, fmt.Errorf("extract: line range %d-%d is outside symbol %s (%d-%d)",
			startLine, endLine, sym.Name, symStartLine, symEndLine)
	}

	// Extract the selected lines.
	// lines is 0-indexed, startLine/endLine are 1-indexed.
	extracted := lines[startLine-1 : endLine]

	// Detect the indentation of the extracted block to de-indent for the new function.
	indent := detectMinIndent(extracted)
	var deindented []string
	for _, line := range extracted {
		s := string(line)
		if len(s) >= len(indent) && strings.HasPrefix(s, indent) {
			deindented = append(deindented, "\t"+s[len(indent):])
		} else if strings.TrimSpace(s) == "" {
			deindented = append(deindented, "")
		} else {
			deindented = append(deindented, "\t"+s)
		}
	}

	// Build the new function body using language-appropriate syntax.
	ext := strings.ToLower(filepath.Ext(sym.File))

	// Parse parameters from the --call expression if provided.
	// E.g. --call "helper(rq, flags)" => params = "rq, flags"
	var params string
	if callExpr != "" {
		if lp := strings.Index(callExpr, "("); lp >= 0 {
			if rp := strings.LastIndex(callExpr, ")"); rp > lp {
				params = strings.TrimSpace(callExpr[lp+1 : rp])
			}
		}
	}

	newFunc := buildExtractedFunction(ext, newName, params, deindented)

	// Build the call expression that replaces the extracted lines.
	if callExpr == "" {
		callExpr = newName + "()"
	}
	// Add statement terminator for languages that need it.
	if needsSemicolon(ext) && !strings.HasSuffix(callExpr, ";") {
		callExpr += ";"
	}
	// Indent the call to match the original indentation.
	callLine := indent + callExpr

	// Build new file content:
	// 1. Replace the extracted lines with the call
	// 2. Insert the new function after the source symbol
	var result []byte
	for i, line := range lines {
		lineNum := i + 1 // 1-indexed
		if lineNum == startLine {
			result = append(result, []byte(callLine)...)
			result = append(result, '\n')
		} else if lineNum > startLine && lineNum <= endLine {
			// Skip extracted lines.
			continue
		} else {
			result = append(result, line...)
			// Insert the new function after the symbol's closing line.
			if lineNum == symEndLine {
				result = append(result, '\n')
				result = append(result, '\n')
				result = append(result, []byte(newFunc)...)
			}
			if i < len(lines)-1 {
				result = append(result, '\n')
			}
		}
	}

	diff := edit.UnifiedDiff(output.Rel(sym.File), data, result)

	if dryRun {
		return map[string]any{
			"file":    output.Rel(sym.File),
			"status":  "dry_run",
			"diff":    diff,
			"message": fmt.Sprintf("extract %d lines from %s into %s", endLine-startLine+1, sym.Name, newName),
		}, nil
	}

	// Apply.
	hash := edit.HashBytes(data)
	tx := edit.NewTransaction()
	tx.Add(sym.File, 0, uint32(len(data)), string(result), hash)
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	idx.MarkDirty(db.EdrDir(), output.Rel(sym.File))
	newHash, _ := edit.FileHash(sym.File)

	return map[string]any{
		"file":    output.Rel(sym.File),
		"status":  "applied",
		"diff":    diff,
		"hash":    newHash,
		"message": fmt.Sprintf("extract %d lines from %s into %s", endLine-startLine+1, sym.Name, newName),
	}, nil
}

// detectMinIndent finds the minimum leading whitespace across non-empty lines.
func detectMinIndent(lines [][]byte) string {
	min := ""
	first := true
	for _, line := range lines {
		s := string(line)
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		leading := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
		if first || len(leading) < len(min) {
			min = leading
			first = false
		}
	}
	return min
}

// buildExtractedFunction wraps the deindented lines in a function declaration
// appropriate for the file's language.
func buildExtractedFunction(ext, name, params string, body []string) string {
	joined := strings.Join(body, "\n")

	switch ext {
	case ".py", ".pyi":
		// Python: def name(params):\n    body
		return fmt.Sprintf("def %s(%s):\n%s\n", name, params, joined)

	case ".rb":
		// Ruby: def name(params)\n  body\nend
		if params != "" {
			return fmt.Sprintf("def %s(%s)\n%s\nend\n", name, params, joined)
		}
		return fmt.Sprintf("def %s\n%s\nend\n", name, joined)

	case ".rs":
		// Rust: fn name(params) {\n    body\n}
		return fmt.Sprintf("fn %s(%s) {\n%s\n}\n", name, params, joined)

	case ".java", ".kt", ".kts", ".scala", ".sc":
		// Java/Kotlin/Scala: void name(params) {\n    body\n}
		return fmt.Sprintf("void %s(%s) {\n%s\n}\n", name, params, joined)

	case ".js", ".jsx", ".ts", ".tsx", ".mts", ".cts":
		// JavaScript/TypeScript: function name(params) {\n    body\n}
		return fmt.Sprintf("function %s(%s) {\n%s\n}\n", name, params, joined)

	case ".c", ".h", ".cpp", ".cc", ".hpp", ".cxx", ".hxx", ".hh":
		// C/C++: void name(params) {\n    body\n}
		return fmt.Sprintf("void %s(%s) {\n%s\n}\n", name, params, joined)

	default:
		// Go and fallback: func name(params) {\n    body\n}
		return fmt.Sprintf("func %s(%s) {\n%s\n}\n", name, params, joined)
	}
}

// needsSemicolon returns true for languages where a bare function call needs a trailing semicolon.
func needsSemicolon(ext string) bool {
	switch ext {
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
		".java", ".kt", ".kts", ".scala", ".sc",
		".js", ".jsx", ".ts", ".tsx", ".mts", ".cts",
		".rs", ".m", ".mm":
		return true
	}
	return false
}
