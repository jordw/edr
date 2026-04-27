package dispatch

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runSmartEditInner(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any, dryRun bool) (any, error) {

	// Pre-check: if --expect-hash is set, validate file hash before any edit
	if expectHash := flagString(flags, "expect_hash", ""); expectHash != "" && len(args) >= 1 {
		// Strip :Symbol suffix — args[0] may be file:Symbol
		hashArg := args[0]
		if parts := splitFileSymbol(hashArg); parts != nil {
			hashArg = parts[0]
		}
		file, err := db.ResolvePath(hashArg)
		if err != nil {
			return nil, err
		}
		currentHash, _ := edit.FileHash(file)
		if currentHash != expectHash {
			return nil, fmt.Errorf("hash mismatch on %s: expected %s, got %s (file was modified externally — run 'edr focus %s' to refresh session state, then retry)", output.Rel(file), expectHash, currentHash, output.Rel(file))
		}
	}

	// --move-after: move source symbol after target symbol (same file only)
	if moveAfter := flagString(flags, "move_after", ""); moveAfter != "" {
		if len(args) < 1 {
			return nil, fmt.Errorf("edit --move-after requires a source symbol argument")
		}
		return smartEditMoveAfter(ctx, db, root, args, moveAfter, dryRun)
	}

	newText := flagString(flags, "new_text", "")
	// Whether new_text was explicitly provided (even as empty string = deletion).
	_, newTextSet := flags["new_text"]
	// --delete is equivalent to --new-text ""
	if flagBool(flags, "delete", false) {
		newText = ""
		newTextSet = true
	}

	oldText := flagString(flags, "old_text", "")

	// --where: resolve symbol by name, determine file, scope edit automatically.
	// This is sugar over --in with automatic file resolution.
	if whereSpec := flagString(flags, "where", ""); whereSpec != "" {
		if len(args) > 0 {
			return nil, fmt.Errorf("--where and positional file argument are mutually exclusive")
		}
		if flagString(flags, "in", "") != "" {
			return nil, fmt.Errorf("--where and --in are mutually exclusive")
		}
		if flagString(flags, "lines", "") != "" || flagInt(flags, "start_line", 0) > 0 || flagInt(flags, "end_line", 0) > 0 {
			return nil, fmt.Errorf("--where is incompatible with --lines/--start-line/--end-line")
		}
		if flagInt(flags, "insert_at", 0) > 0 {
			return nil, fmt.Errorf("--where is incompatible with --insert-at")
		}

		// Resolve the symbol via the index
		sym, err := resolveSymbolArgs(ctx, db, root, []string{whereSpec})
		if err != nil {
			return nil, fmt.Errorf("--where: %w", err)
		}

		if oldText != "" {
			// Scoped text match within the resolved symbol
			return smartEditMatchInSymbol(ctx, db, sym.File, oldText, newText, flags, sym.Name, dryRun)
		}

		// Whole-symbol replacement or deletion
		endByte := sym.EndByte
		if newText == "" && newTextSet {
			src, _ := os.ReadFile(sym.File)
			if src != nil && int(endByte) < len(src) && src[endByte] == '\n' {
				endByte++
			}
		}
		return smartEditByteRange(ctx, db, sym.File, sym.StartByte, endByte, newText, sym.Name, dryRun)
	}

	// Determine targeting mode:
	// 1. --start_line/--end_line: line range (requires file as first arg)
	// 2. --old_text: find and replace text (requires file as first arg)
	// 3. Default: symbol-based (replace entire symbol body)

	// --lines flag: parse "start:end" into start_line/end_line
	if linesStr := flagString(flags, "lines", ""); linesStr != "" {
		start, end, err := parseColonRange(linesStr)
		if err != nil {
			return nil, fmt.Errorf("--lines: %w", err)
		}
		flags["start_line"] = start
		flags["end_line"] = end
	}
	startLine := flagInt(flags, "start_line", 0)
	endLine := flagInt(flags, "end_line", 0)

	// Require new_text if an edit mode is active.
	if !newTextSet && newText == "" {
		if oldText != "" {
			return nil, fmt.Errorf("--old requires --new (or --delete to remove the matched text)")
		}
		return nil, fmt.Errorf("edit requires --new, --content, or --delete")
	}

	// --insert-at N: zero-width insertion before line N
	if insertAt := flagInt(flags, "insert_at", 0); insertAt > 0 {
		if len(args) < 1 {
			return nil, fmt.Errorf("edit with --insert-at requires a file argument")
		}
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		return smartEditInsertAt(ctx, db, file, insertAt, newText, dryRun)
	}

	if startLine > 0 && endLine > 0 {
		if len(args) < 1 {
			return nil, fmt.Errorf("edit with --start_line/--end_line requires a file argument")
		}
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		return smartEditSpan(ctx, db, file, startLine, endLine, newText, "", dryRun)
	}

	if oldText != "" {
		if len(args) < 1 {
			return nil, fmt.Errorf("edit with --old_text requires a file argument")
		}
		// Support file:Symbol syntax — split and use symbol as implicit --in
		fileArg := args[0]
		inSpec := flagString(flags, "in", "")
		if parts := splitFileSymbol(fileArg); parts != nil && inSpec == "" {
			fileArg = parts[0]
			inSpec = fileArg + ":" + parts[1]
		}
		file, err := db.ResolvePath(fileArg)
		if err != nil {
			return nil, err
		}
		if inSpec != "" {
			return smartEditMatchInSymbol(ctx, db, file, oldText, newText, flags, inSpec, dryRun)
		}
		return smartEditMatch(ctx, db, file, oldText, newText, flags, dryRun)
	}

	// Symbol mode: edit file Symbol or edit file:Symbol (delete/replace entire symbol)
	if len(args) < 1 {
		return nil, fmt.Errorf("edit requires: [file] <symbol>, or <file> with --old_text/--start_line/--end_line")
	}
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		if flagBool(flags, "delete", false) {
			return nil, fmt.Errorf("%w (use --old with --delete to delete text, or provide a symbol name as second arg)", err)
		}
		return nil, err
	}
	endByte := sym.EndByte
	// When deleting a whole symbol, consume one trailing newline to avoid blank line
	if newText == "" && newTextSet {
		src, _ := os.ReadFile(sym.File)
		if src != nil && int(endByte) < len(src) && src[endByte] == '\n' {
			endByte++
		}
	}
	return smartEditByteRange(ctx, db, sym.File, sym.StartByte, endByte, newText, sym.Name, dryRun)
}

// smartEditByteRange applies an edit to a byte range and returns a smart-edit result.
func smartEditByteRange(ctx context.Context, db index.SymbolStore, file string, startByte, endByte uint32, replacement, label string, dryRun bool) (any, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	oldBody := string(src[startByte:endByte])

	diff, err := edit.DiffPreview(file, startByte, endByte, replacement)
	if err != nil {
		return nil, err
	}

	if dryRun {
		oldLines := strings.Count(oldBody, "\n")
		newLines := strings.Count(replacement, "\n")
		linesAdded := 0
		linesRemoved := 0
		if newLines > oldLines {
			linesAdded = newLines - oldLines
		} else {
			linesRemoved = oldLines - newLines
		}
		result := map[string]any{
			"file":          output.Rel(file),
			"status":        "dry_run",
			"diff":          diff,
			"old_size":      len(oldBody) / 4,
			"new_size":      len(replacement) / 4,
			"lines_added":   linesAdded,
			"lines_removed": linesRemoved,
			"destructive":   replacement == "",
		}
		if label != "" {
			result["symbol"] = label
		}
		return result, nil
	}

	hash, _ := edit.FileHash(file)
	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: file, StartByte: startByte, EndByte: endByte,
		Replacement: replacement, ExpectHash: hash,
	}})
	if err != nil {
		return nil, fmt.Errorf("edit failed: %w", err)
	}

	result := map[string]any{
		"file":     output.Rel(file),
		"diff":     diff,
		"hash":     cr.Hashes[output.Rel(file)],
		"status":   cr.Status,
		"old_size": len(oldBody) / 4,
		"new_size": len(replacement) / 4,
	}
	if label != "" {
		result["symbol"] = label
	}
	if len(cr.IndexErrors) > 0 {
		result["index_error"] = cr.IndexErrors[output.Rel(file)]
	}
	return result, nil
}

// smartEditSpan applies an edit to a line range.
func smartEditSpan(ctx context.Context, db index.SymbolStore, file string, startLine, endLine int, replacement, label string, dryRun bool) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Convert lines to byte offsets
	line := 1
	var startByte, endByte uint32
	foundStart := false
	for i := 0; i <= len(data); i++ {
		if line == startLine && !foundStart {
			startByte = uint32(i)
			foundStart = true
		}
		if line == endLine+1 || (line == endLine && i == len(data)) {
			endByte = uint32(i)
			break
		}
		if i < len(data) && data[i] == '\n' {
			line++
		}
	}
	if !foundStart {
		return nil, fmt.Errorf("smart-edit: start line %d beyond file (%d lines)", startLine, line-1)
	}
	if endByte == 0 && endLine >= line {
		endByte = uint32(len(data))
	}

	if label == "" {
		label = fmt.Sprintf("lines %d-%d", startLine, endLine)
	}
	return smartEditByteRange(ctx, db, file, startByte, endByte, replacement, label, dryRun)
}

// smartEditInsertAt performs a zero-width insertion before the given line number.
func smartEditInsertAt(ctx context.Context, db index.SymbolStore, file string, lineNum int, text string, dryRun bool) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Normalize: inserted text should end with newline for line-oriented use
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}

	// Find byte offset of the start of the target line
	line := 1
	insertByte := uint32(0)
	found := false
	for i := 0; i <= len(data); i++ {
		if line == lineNum {
			insertByte = uint32(i)
			found = true
			break
		}
		if i < len(data) && data[i] == '\n' {
			line++
		}
	}
	// Allow inserting at EOF (one past the last line)
	if !found {
		if lineNum == line+1 || lineNum == line {
			insertByte = uint32(len(data))
		} else {
			return nil, fmt.Errorf("insert-at: line %d beyond file (%d lines)", lineNum, line)
		}
	}

	label := fmt.Sprintf("insert at line %d", lineNum)
	return smartEditByteRange(ctx, db, file, insertByte, insertByte, text, label, dryRun)
}
