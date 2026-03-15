package dispatch

import (
	"context"
	"fmt"
	"os"
	"regexp"
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

	// Move mode: --move <symbol> --after/--before <symbol>
	moveSymbol := flagString(flags, "move", "")
	if moveSymbol != "" {
		if len(args) < 1 {
			return nil, fmt.Errorf("edit --move requires a file argument")
		}
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		after := flagString(flags, "after", "")
		before := flagString(flags, "before", "")
		if after == "" && before == "" {
			return nil, fmt.Errorf("edit --move requires --after or --before to specify destination")
		}
		if after != "" && before != "" {
			return nil, fmt.Errorf("edit --move: use --after or --before, not both")
		}
		return smartEditMove(ctx, db, file, moveSymbol, after, before, dryRun)
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
		result := map[string]any{
			"file":     output.Rel(file),
			"diff":     diff,
			"old_size": len(oldBody) / 4,
			"new_size": len(replacement) / 4,
			"dry_run":  true,
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
		"ok":       true,
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
			"ok":      true,
			"noop":    true,
			"file":    output.Rel(file),
			"message": "old_text equals new_text, no change applied",
		}, nil
	}

	useRegex := flagBool(flags, "regex", false)
	replaceAll := flagBool(flags, "all", false)

	// Find first match and validate
	var startByte, endByte int
	if useRegex {
		re, err := regexp.Compile(matchText)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		locs := re.FindAllStringIndex(content, -1)
		if len(locs) == 0 {
			return nil, fmt.Errorf("smart-edit: pattern %q not found in %s", matchText, output.Rel(file))
		}
		if replaceAll {
			resultText := re.ReplaceAllString(content, replacement)
			return applyReplaceAll(ctx, db, file, content, resultText, matchText, len(locs), dryRun)
		}
		if len(locs) > 1 {
			return nil, ambiguousMatchError(content, output.Rel(file), matchText, locs)
		}
		startByte = locs[0][0]
		endByte = locs[0][1]
	} else {
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

// smartEditMove moves a symbol to a new position relative to another symbol, atomically.
func smartEditMove(ctx context.Context, db *index.DB, file, moveSymbol, afterSymbol, beforeSymbol string, dryRun bool) (any, error) {
	src, err := db.GetSymbol(ctx, file, moveSymbol)
	if err != nil {
		return nil, fmt.Errorf("move source: %w", err)
	}

	destName := afterSymbol
	if destName == "" {
		destName = beforeSymbol
	}
	dest, err := db.GetSymbol(ctx, file, destName)
	if err != nil {
		return nil, fmt.Errorf("move destination: %w", err)
	}

	// Self-move is a no-op
	if src.StartByte == dest.StartByte && src.EndByte == dest.EndByte {
		return map[string]any{
			"ok":      true,
			"noop":    true,
			"file":    output.Rel(file),
			"message": fmt.Sprintf("%s is already at the target position", moveSymbol),
		}, nil
	}

	// Check for overlap
	if src.StartByte < dest.EndByte && dest.StartByte < src.EndByte {
		return nil, fmt.Errorf("cannot move %s: overlaps with %s", moveSymbol, destName)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Extract the source text, including leading blank lines that visually belong to it.
	// Also consume trailing newlines to avoid leaving a gap.
	deleteStart := src.StartByte
	deleteEnd := src.EndByte

	// Expand backward to consume preceding blank line(s) (up to one blank line)
	pos := int(deleteStart)
	// Skip back past the newline right before the symbol
	if pos > 0 && data[pos-1] == '\n' {
		pos--
		// Skip back past another blank line if present
		if pos > 0 && data[pos-1] == '\n' {
			pos--
		}
		deleteStart = uint32(pos + 1) // keep one newline as separator
	}

	// Expand forward to consume trailing newline(s), but keep one
	// so the symbol below the gap isn't glued to the one above it.
	pos = int(deleteEnd)
	nlCount := 0
	for pos < len(data) && data[pos] == '\n' {
		pos++
		nlCount++
	}
	if nlCount > 1 && pos < len(data) {
		// Leave one \n so adjacent symbols stay separated
		pos--
	}
	deleteEnd = uint32(pos)

	symbolText := strings.TrimRight(string(data[src.StartByte:src.EndByte]), "\n")

	// Compute insertion point
	var insertAt uint32
	if afterSymbol != "" {
		insertAt = dest.EndByte
	} else {
		insertAt = dest.StartByte
	}

	// Build the replacement text
	var insertion string
	if afterSymbol != "" {
		insertion = "\n\n" + symbolText + "\n"
	} else {
		insertion = symbolText + "\n\n"
	}

	if dryRun {
		// Apply both edits in memory to produce a preview
		result := string(data)
		// Apply in reverse byte order to preserve offsets
		if deleteStart > insertAt {
			// Delete is after insert position
			result = result[:deleteStart] + result[deleteEnd:]
			result = result[:insertAt] + insertion + result[insertAt:]
		} else {
			// Insert is after delete position
			result = result[:insertAt] + insertion + result[insertAt:]
			result = result[:deleteStart] + result[deleteEnd:]
		}
		diff, _ := edit.DiffPreviewContent(file, data, []byte(result))
		return map[string]any{
			"file":    output.Rel(file),
			"symbol":  moveSymbol,
			"diff":    diff,
			"dry_run": true,
		}, nil
	}

	hash, _ := edit.FileHash(file)
	cr, err := commitEdits(ctx, db, []resolvedEdit{
		{File: file, StartByte: deleteStart, EndByte: deleteEnd, Replacement: "", ExpectHash: hash},
		{File: file, StartByte: insertAt, EndByte: insertAt, Replacement: insertion, ExpectHash: hash},
	})
	if err != nil {
		return nil, fmt.Errorf("move failed: %w", err)
	}

	rel := output.Rel(file)
	result := map[string]any{
		"ok":     true,
		"file":   rel,
		"hash":   cr.Hashes[rel],
		"symbol": moveSymbol,
	}
	if afterSymbol != "" {
		result["after"] = afterSymbol
	} else {
		result["before"] = beforeSymbol
	}
	if len(cr.IndexErrors) > 0 {
		result["index_error"] = cr.IndexErrors[rel]
	}
	return result, nil
}

// applyReplaceAll handles the shared tail of regex and literal replace-all edits.
func applyReplaceAll(ctx context.Context, db *index.DB, file, oldContent, newContent, matchText string, count int, dryRun bool) (any, error) {
	if oldContent == newContent {
		return map[string]any{
			"ok":      true,
			"noop":    true,
			"file":    output.Rel(file),
			"message": "old_text equals new_text, no change applied",
		}, nil
	}
	if dryRun {
		diff, _ := edit.DiffPreviewContent(file, []byte(oldContent), []byte(newContent))
		return map[string]any{
			"file":    output.Rel(file),
			"diff":    diff,
			"count":   count,
			"match":   matchText,
			"dry_run": true,
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
		"ok":       true,
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
	EditIndex  *int           `json:"edit_index,omitempty"`
	EditMode   string         `json:"edit_mode,omitempty"`
	TotalEdits *int           `json:"total_edits,omitempty"`
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
			return nfe
		}
	}

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
	return fmt.Errorf("old_text %q matched %d locations in %s (lines %s); provide more surrounding context to make it unique, or use all: true to replace all",
		matchText, len(locs), relFile, strings.Join(lineStrs, ", "))
}
