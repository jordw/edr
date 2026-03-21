package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runSmartEdit(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	dryRun := flagBool(flags, "dry-run", false)

	// Pre-check: if --expect-hash is set, validate file hash before any edit
	if expectHash := flagString(flags, "expect_hash", ""); expectHash != "" && len(args) >= 1 {
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		currentHash, _ := edit.FileHash(file)
		if currentHash != expectHash {
			return nil, fmt.Errorf("hash mismatch on %s: expected %s, got %s (file was modified)", output.Rel(file), expectHash, currentHash)
		}
	}

	// new_text is the primary flag name; replacement is accepted as a legacy alias.
	newText := flagString(flags, "new_text", "")
	if newText == "" {
		newText = flagString(flags, "replacement", "")
	}
	// Whether new_text was explicitly provided (even as empty string = deletion).
	_, newTextSet := flags["new_text"]
	if !newTextSet {
		_, newTextSet = flags["replacement"]
	}

	// Determine targeting mode:
	// 1. --start_line/--end_line: line range (requires file as first arg)
	// 2. --old_text: find and replace text (requires file as first arg)
	// 3. Default: symbol-based (replace entire symbol body)

	startLine := flagInt(flags, "start_line", 0)
	endLine := flagInt(flags, "end_line", 0)
	// old_text is the primary flag name; match is accepted as a legacy alias.
	oldText := flagString(flags, "old_text", "")
	if oldText == "" {
		oldText = flagString(flags, "match", "")
	}

	// Require new_text if an edit mode is active.
	if !newTextSet && newText == "" {
		return nil, fmt.Errorf("edit requires 'new_text' in flags")
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
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		return smartEditMatch(ctx, db, file, oldText, newText, flags, dryRun)
	}

	// Symbol mode
	if len(args) < 1 {
		return nil, fmt.Errorf("edit requires: [file] <symbol>, or <file> with --old_text/--start_line/--end_line")
	}
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}
	return smartEditByteRange(ctx, db, sym.File, sym.StartByte, sym.EndByte, newText, sym.Name, dryRun)
}

// smartEditByteRange applies an edit to a byte range and returns a smart-edit result.
func smartEditByteRange(ctx context.Context, db *index.DB, file string, startByte, endByte uint32, replacement, label string, dryRun bool) (any, error) {
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
func smartEditSpan(ctx context.Context, db *index.DB, file string, startLine, endLine int, replacement, label string, dryRun bool) (any, error) {
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

// smartEditMatch applies an edit by finding and replacing text.
func smartEditMatch(ctx context.Context, db *index.DB, file, matchText, replacement string, flags map[string]any, dryRun bool) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	content := string(data)

	// Detect no-op: old_text == new_text means nothing would change
	if matchText == replacement {
		// Even for no-ops, validate expect_hash if provided
		if expectHash := flagString(flags, "expect_hash", ""); expectHash != "" {
			currentHash := edit.HashBytes(data)
			if currentHash != expectHash {
				return nil, fmt.Errorf("hash mismatch on %s: expected %s, got %s (file was modified)", output.Rel(file), expectHash, currentHash)
			}
		}
		return map[string]any{
			"status":  "noop",
			"file":    output.Rel(file),
			"message": "old_text equals new_text, no change applied",
		}, nil
	}

	replaceAll := flagBool(flags, "all", false)

	// Find first match and validate
	var startByte, endByte int
	{
		idx := strings.Index(content, matchText)
		if idx < 0 {
			return nil, notFoundError(content, output.Rel(file), matchText)
		}
		totalMatches := strings.Count(content, matchText)
		if replaceAll {
			resultText := strings.ReplaceAll(content, matchText, replacement)
			return applyReplaceAll(ctx, db, file, content, resultText, matchText, totalMatches, dryRun)
		}
		if totalMatches > 1 {
			// Build match locations for the error
			locs := make([][]int, 0, totalMatches)
			off := 0
			for {
				i := strings.Index(content[off:], matchText)
				if i < 0 {
					break
				}
				abs := off + i
				locs = append(locs, []int{abs, abs + len(matchText)})
				off = abs + len(matchText)
			}
			return nil, ambiguousMatchError(content, output.Rel(file), matchText, locs)
		}
		startByte = idx
		endByte = idx + len(matchText)
	}

	return smartEditByteRange(ctx, db, file, uint32(startByte), uint32(endByte), replacement, "", dryRun)
}

// applyReplaceAll handles the shared tail of regex and literal replace-all edits.
func applyReplaceAll(ctx context.Context, db *index.DB, file, oldContent, newContent, matchText string, count int, dryRun bool) (any, error) {
	if oldContent == newContent {
		return map[string]any{
			"status":  "noop",
			"file":    output.Rel(file),
			"message": "old_text equals new_text, no change applied",
		}, nil
	}
	if dryRun {
		diff, _ := edit.DiffPreviewContent(file, []byte(oldContent), []byte(newContent))
		oldLines := strings.Count(oldContent, "\n")
		newLines := strings.Count(newContent, "\n")
		linesAdded := 0
		linesRemoved := 0
		if newLines > oldLines {
			linesAdded = newLines - oldLines
		} else {
			linesRemoved = oldLines - newLines
		}
		return map[string]any{
			"file":          output.Rel(file),
			"status":        "dry_run",
			"diff":          diff,
			"count":         count,
			"match":         matchText,
			"lines_added":   linesAdded,
			"lines_removed": linesRemoved,
			"destructive":   newContent == "",
		}, nil
	}

	oldHash, _ := edit.FileHash(file)
	// Replace-all: replace entire file content as a single span
	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: file, StartByte: 0, EndByte: uint32(len(oldContent)),
		Replacement: newContent, ExpectHash: oldHash,
	}})
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"file":     output.Rel(file),
		"hash":     cr.Hashes[output.Rel(file)],
		"status":   cr.Status,
		"old_hash": oldHash,
		"count":    count,
		"match":    matchText,
	}
	if len(cr.IndexErrors) > 0 {
		result["index_error"] = cr.IndexErrors[output.Rel(file)]
	}
	return result, nil
}

// NotFoundError is a structured error returned when old_text doesn't match.
// It implements error for Go error chains and is detected by asNotFoundError
// in the batch handler to produce structured JSON output.
type NotFoundError struct {
	ErrorType  string         `json:"error"`
	File       string         `json:"file"`
	OldText    string         `json:"old_text"`
	Hint       string         `json:"hint"`
	NearMatch  *nearMatchInfo `json:"near_match,omitempty"`
	NextAction string         `json:"next_action,omitempty"`
}

type nearMatchInfo struct {
	Line        int    `json:"line"`
	Kind        string `json:"kind"` // "whitespace", "indentation", "partial"
	ActualText  string `json:"actual_text,omitempty"`
}

func (e *NotFoundError) Error() string {
	msg := fmt.Sprintf("old_text not found in %s", e.File)
	if e.NearMatch != nil {
		msg += fmt.Sprintf(" (%s near line %d)", e.NearMatch.Kind, e.NearMatch.Line)
	}
	return msg
}

// notFoundError builds a NotFoundError with diagnostic hints.
func notFoundError(content, relFile, matchText string) *NotFoundError {
	nfe := &NotFoundError{
		ErrorType: "not_found",
		File:      relFile,
		OldText:   matchText,
		Hint:      "file may have changed since last read — re-read before editing",
	}

	// Truncate old_text in the struct for JSON output
	if len(nfe.OldText) > 200 {
		nfe.OldText = nfe.OldText[:200] + "..."
	}

	// 1. Check whitespace-normalized match (tabs vs spaces, trailing spaces, etc.)
	normContent := normalizeWhitespace(content)
	normMatch := normalizeWhitespace(matchText)
	if idx := strings.Index(normContent, normMatch); idx >= 0 {
		line := 1 + strings.Count(content[:findOriginalOffset(content, normContent, idx)], "\n")
		nfe.Hint = "old_text matches after normalizing whitespace — check tabs vs spaces, trailing spaces, or line endings"
		nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "whitespace"}
		nfe.NextAction = fmt.Sprintf("re-read %s and copy exact whitespace from the output", relFile)
		return nfe
	}

	// 2. Check if old_text matches after trimming leading/trailing whitespace from each line
	trimmedMatch := trimLines(matchText)
	trimmedContent := trimLines(content)
	if idx := strings.Index(trimmedContent, trimmedMatch); idx >= 0 {
		origOff := findOriginalOffset(content, trimmedContent, idx)
		line := 1 + strings.Count(content[:origOff], "\n")
		nfe.Hint = "old_text matches after trimming indentation — check leading whitespace on each line"
		nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "indentation"}
		nfe.NextAction = fmt.Sprintf("re-read %s and copy exact indentation from the output", relFile)
		return nfe
	}

	// 3. Find best partial match — first line of old_text
	firstLine := matchText
	if nl := strings.Index(matchText, "\n"); nl >= 0 {
		firstLine = matchText[:nl]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine != "" && len(firstLine) > 5 {
		if idx := strings.Index(content, firstLine); idx >= 0 {
			line := 1 + strings.Count(content[:idx], "\n")
			lineStart := strings.LastIndex(content[:idx], "\n") + 1
			lineEnd := strings.Index(content[idx:], "\n")
			if lineEnd < 0 {
				lineEnd = len(content) - idx
			}
			actualLine := content[lineStart : idx+lineEnd]
			if len(actualLine) > 120 {
				actualLine = actualLine[:120] + "..."
			}
			nfe.Hint = "first line of old_text found but full match failed — content may have diverged"
			nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "partial", ActualText: actualLine}
			nfe.NextAction = fmt.Sprintf("re-read %s to get current content, then retry with updated old_text", relFile)
			return nfe
		}
	}

	nfe.NextAction = fmt.Sprintf("re-read %s to get current content", relFile)
	return nfe
}

// normalizeWhitespace collapses runs of whitespace to single spaces.
func normalizeWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return b.String()
}

// trimLines trims leading/trailing whitespace from each line.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}

// findOriginalOffset maps a position in a normalized string back to the approximate
// position in the original string by counting non-whitespace characters.
func findOriginalOffset(original, normalized string, normIdx int) int {
	// Count characters (non-whitespace) in normalized up to normIdx
	target := 0
	for i, r := range normalized {
		if i >= normIdx {
			break
		}
		if r != ' ' && r != '\t' {
			target++
		}
	}
	// Find same count in original
	count := 0
	for i, r := range original {
		if r != ' ' && r != '\t' {
			count++
		}
		if count >= target {
			return i
		}
	}
	return len(original)
}

// ambiguousMatchError builds an error with line numbers for all match locations.
func ambiguousMatchError(content, relFile, matchText string, locs [][]int) error {
	lines := make([]int, 0, len(locs))
	for _, loc := range locs {
		line := 1 + strings.Count(content[:loc[0]], "\n")
		lines = append(lines, line)
	}
	lineStrs := make([]string, len(lines))
	for i, l := range lines {
		lineStrs[i] = fmt.Sprintf("%d", l)
	}
	return fmt.Errorf("ambiguous: old_text %q matched %d locations in %s (lines %s); provide more surrounding context to make it unique, or use all: true to replace all",
		matchText, len(locs), relFile, strings.Join(lineStrs, ", "))
}

func runRenameSymbol(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("rename-symbol requires 2 arguments: <old-name> <new-name>")
	}
	oldName := args[0]
	newName := args[1]
	dryRun := flagBool(flags, "dry-run", false)

	// Find all identifier occurrences (exact byte ranges for replacement).
	refs, err := index.FindIdentifierOccurrences(ctx, db, oldName)
	if err != nil {
		return nil, err
	}

	if oldName == newName {
		return output.RenameResult{OldName: oldName, NewName: newName, DryRun: dryRun, Noop: true}, nil
	}

	if len(refs) == 0 {
		return output.RenameResult{OldName: oldName, NewName: newName, DryRun: dryRun, Noop: true}, nil
	}

	// Group refs by file
	grouped := make(map[string][]index.SymbolInfo)
	for _, r := range refs {
		grouped[r.File] = append(grouped[r.File], r)
	}

	// Collect file list
	var filesChanged []string
	for file := range grouped {
		filesChanged = append(filesChanged, output.Rel(file))
	}

	// Dry-run: show what would change without applying
	if dryRun {
		var preview []output.RenameOccurrence
		for _, r := range refs {
			src, err := os.ReadFile(r.File)
			if err != nil {
				continue
			}
			lines := strings.SplitAfter(string(src), "\n")
			lineIdx := int(r.StartLine) - 1
			lineText := ""
			if lineIdx >= 0 && lineIdx < len(lines) {
				lineText = strings.TrimRight(lines[lineIdx], "\n")
			}
			preview = append(preview, output.RenameOccurrence{
				File: output.Rel(r.File),
				Line: int(r.StartLine),
				Text: lineText,
			})
		}
		return output.RenameResult{
			OldName:      oldName,
			NewName:      newName,
			FilesChanged: filesChanged,
			Occurrences:  len(refs),
			DryRun:       true,
			Preview:      preview,
		}, nil
	}

	var edits []resolvedEdit
	for file, fileRefs := range grouped {
		hash, _ := edit.FileHash(file)
		for i, r := range fileRefs {
			h := ""
			if i == 0 {
				h = hash
			}
			edits = append(edits, resolvedEdit{
				File: file, StartByte: r.StartByte, EndByte: r.EndByte,
				Replacement: newName, ExpectHash: h,
			})
		}
	}

	cr, err := commitEdits(ctx, db, edits)
	if err != nil {
		return nil, fmt.Errorf("rename failed: %w", err)
	}

	var renameWarnings []string
	if len(cr.IndexErrors) > 0 {
		var parts []string
		for file, errMsg := range cr.IndexErrors {
			parts = append(parts, file+": "+errMsg)
		}
		renameWarnings = append(renameWarnings, "re-index partial failure: "+strings.Join(parts, "; "))
	}

	// Verify the new symbol is queryable. ResolveSymbol fails if the name is
	// ambiguous (exists in multiple files), which is expected after a rename
	// that touched multiple files. Treat ambiguity as success, only warn on
	// true not-found (stale index / WAL issue).
	if _, err := db.ResolveSymbol(ctx, newName); err != nil {
		var ambErr *index.AmbiguousSymbolError
		if errors.As(err, &ambErr) {
			// Multiple matches is fine — the rename worked, symbol exists
		} else {
			renameWarnings = append(renameWarnings,
				fmt.Sprintf("new symbol %q not found in index — try 'edr init'", newName))
		}
	}

	result := output.RenameResult{
		OldName:      oldName,
		NewName:      newName,
		FilesChanged: filesChanged,
		Occurrences:  len(refs),
		Hashes:       cr.Hashes,
		Warnings:     renameWarnings,
	}
	return result, nil
}
