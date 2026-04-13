package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

	// Build the new function body.
	newFunc := fmt.Sprintf("func %s() {\n%s\n}\n", newName, strings.Join(deindented, "\n"))

	// Build the call expression that replaces the extracted lines.
	if callExpr == "" {
		callExpr = newName + "()"
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
