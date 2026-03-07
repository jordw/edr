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
	// new_text is the primary flag name; replacement is accepted as a legacy alias.
	newText := flagString(flags, "new_text", "")
	if newText == "" {
		newText = flagString(flags, "replacement", "")
	}
	if newText == "" {
		return nil, fmt.Errorf("edit requires 'new_text' in flags")
	}

	dryRun := flagBool(flags, "dry-run", false) || flagBool(flags, "dry_run", false)

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
	useRegex := flagBool(flags, "regex", false)
	replaceAll := flagBool(flags, "all", false)

	// Find first match and validate
	var startByte, endByte int
	if useRegex {
		re, err := regexp.Compile(matchText)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		loc := re.FindStringIndex(content)
		if loc == nil {
			return nil, fmt.Errorf("smart-edit: pattern %q not found in %s", matchText, output.Rel(file))
		}
		if replaceAll {
			resultText := re.ReplaceAllString(content, replacement)
			count := len(re.FindAllStringIndex(content, -1))
			return applyReplaceAll(ctx, db, file, content, resultText, matchText, count, dryRun)
		}
		startByte = loc[0]
		endByte = loc[1]
	} else {
		idx := strings.Index(content, matchText)
		if idx < 0 {
			return nil, fmt.Errorf("smart-edit: text %q not found in %s", matchText, output.Rel(file))
		}
		if replaceAll {
			count := strings.Count(content, matchText)
			resultText := strings.ReplaceAll(content, matchText, replacement)
			return applyReplaceAll(ctx, db, file, content, resultText, matchText, count, dryRun)
		}
		startByte = idx
		endByte = idx + len(matchText)
	}

	return smartEditByteRange(ctx, db, file, uint32(startByte), uint32(endByte), replacement, "", dryRun)
}

// applyReplaceAll handles the shared tail of regex and literal replace-all edits.
func applyReplaceAll(ctx context.Context, db *index.DB, file, oldContent, newContent, matchText string, count int, dryRun bool) (any, error) {
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
		"old_hash": oldHash,
		"count":    count,
		"match":    matchText,
	}
	if len(cr.IndexErrors) > 0 {
		result["index_error"] = cr.IndexErrors[output.Rel(file)]
	}
	return result, nil
}
