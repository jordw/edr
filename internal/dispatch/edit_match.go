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

// smartEditMatch applies an edit by finding and replacing text.
func smartEditMatch(ctx context.Context, db index.SymbolStore, file, matchText, replacement string, flags map[string]any, dryRun bool) (any, error) {
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
				return nil, fmt.Errorf("hash mismatch on %s: expected %s, got %s (file was modified externally — run 'edr focus %s' to refresh session state, then retry)", output.Rel(file), expectHash, currentHash, output.Rel(file))
			}
		}
		return map[string]any{
			"status":  "noop",
			"file":    output.Rel(file),
			"message": "old_text equals new_text, no change applied",
		}, nil
	}

	replaceAll := flagBool(flags, "all", false)
	fuzzy := flagBool(flags, "fuzzy", false)

	if fuzzy && replaceAll {
		return nil, fmt.Errorf("--fuzzy and --all are mutually exclusive")
	}

	// Find first match and validate
	var startByte, endByte int
	{
		idx := strings.Index(content, matchText)
		if idx < 0 {
			if fuzzy {
				start, end, kind := fuzzyMatch(content, matchText)
				if start >= 0 {
					return smartEditByteRange(ctx, db, file, uint32(start), uint32(end), replacement, fmt.Sprintf("fuzzy:%s", kind), dryRun)
				}
			}
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
			return nil, ambiguousMatchError(ctx, db, file, content, output.Rel(file), matchText, locs)
		}
		startByte = idx
		endByte = idx + len(matchText)
	}

	return smartEditByteRange(ctx, db, file, uint32(startByte), uint32(endByte), replacement, "", dryRun)
}

// smartEditMatchInSymbol scopes a text-match edit to within a symbol's body.
// The --in flag specifies the symbol (file:Symbol or just Symbol); old_text is
// searched only within that symbol's byte range.
func smartEditMatchInSymbol(ctx context.Context, db index.SymbolStore, file, matchText, replacement string, flags map[string]any, inSpec string, dryRun bool) (any, error) {
	// Parse the symbol spec — accept "Symbol", "file:Symbol", or line range "N-M" / "N:M"
	parts := splitFileSymbol(inSpec)
	var symFile, symName string
	if parts != nil {
		// Check if the "symbol" part is actually a line range (e.g., file.go:4200-4260)
		if start, end, rangeErr := parseColonRange(parts[1]); rangeErr == nil {
			flags["start_line"] = start
			flags["end_line"] = end
			return smartEditMatch(ctx, db, file, matchText, replacement, flags, dryRun)
		}
		symFile = parts[0]
		symName = parts[1]
	} else {
		// Check if the bare spec is a line range (e.g., 4200-4260)
		if start, end, rangeErr := parseColonRange(inSpec); rangeErr == nil {
			flags["start_line"] = start
			flags["end_line"] = end
			return smartEditMatch(ctx, db, file, matchText, replacement, flags, dryRun)
		}
		// Plain symbol name, use the file from args
		symFile = file
		symName = inSpec
	}

	// Resolve the symbol to get its byte range
	resolvedFile, err := db.ResolvePath(symFile)
	if err != nil {
		return nil, fmt.Errorf("--in: %w", err)
	}
	if resolvedFile != file {
		return nil, fmt.Errorf("--in symbol file %q doesn't match edit file %q", output.Rel(resolvedFile), output.Rel(file))
	}

	sym, err := db.GetSymbol(ctx, resolvedFile, symName)
	if err != nil {
		return nil, fmt.Errorf("--in: %w", err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Search within the symbol's byte range only
	symBody := string(data[sym.StartByte:sym.EndByte])
	if matchText == replacement {
		return map[string]any{
			"status":  "noop",
			"file":    output.Rel(file),
			"message": "old_text equals new_text, no change applied",
		}, nil
	}

	replaceAll := flagBool(flags, "all", false)

	idx := strings.Index(symBody, matchText)
	if idx < 0 {
		return nil, notFoundError(symBody, output.Rel(file)+":"+symName, matchText)
	}

	totalMatches := strings.Count(symBody, matchText)
	if replaceAll {
		resultBody := strings.ReplaceAll(symBody, matchText, replacement)
		absStart := int(sym.StartByte)
		absEnd := int(sym.EndByte)
		fullContent := string(data)
		newContent := fullContent[:absStart] + resultBody + fullContent[absEnd:]
		return applyReplaceAll(ctx, db, file, fullContent, newContent, matchText, totalMatches, dryRun)
	}
	if totalMatches > 1 {
		locs := make([][]int, 0, totalMatches)
		off := 0
		for {
			i := strings.Index(symBody[off:], matchText)
			if i < 0 {
				break
			}
			abs := int(sym.StartByte) + off + i
			locs = append(locs, []int{abs, abs + len(matchText)})
			off += i + len(matchText)
		}
		content := string(data)
		return nil, ambiguousMatchError(ctx, db, file, content, output.Rel(file)+":"+symName, matchText, locs)
	}

	absStart := uint32(int(sym.StartByte) + idx)
	absEnd := absStart + uint32(len(matchText))
	return smartEditByteRange(ctx, db, file, absStart, absEnd, replacement, symName, dryRun)
}

// applyReplaceAll handles the shared tail of regex and literal replace-all edits.
func applyReplaceAll(ctx context.Context, db index.SymbolStore, file, oldContent, newContent, matchText string, count int, dryRun bool) (any, error) {
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

// fuzzyMatch tries whitespace/indentation-normalized matching and returns
// the original byte range if exactly one match is found. Returns (-1,-1,"")
// if no match or ambiguous.
func fuzzyMatch(content, matchText string) (int, int, string) {
	// Try 1: whitespace-normalized match
	normContent := normalizeWhitespace(content)
	normMatch := normalizeWhitespace(matchText)
	if idx := strings.Index(normContent, normMatch); idx >= 0 {
		// Check uniqueness in normalized space
		if strings.Count(normContent, normMatch) > 1 {
			return -1, -1, ""
		}
		start := mapNormOffset(content, normContent, idx)
		end := mapNormOffset(content, normContent, idx+len(normMatch))
		return start, end, "whitespace"
	}

	// Try 2: indentation-trimmed match
	trimContent := trimLines(content)
	trimMatch := trimLines(matchText)
	if idx := strings.Index(trimContent, trimMatch); idx >= 0 {
		if strings.Count(trimContent, trimMatch) > 1 {
			return -1, -1, ""
		}
		start := mapTrimOffset(content, idx)
		end := mapTrimOffset(content, idx+len(trimMatch))
		return start, end, "indentation"
	}

	return -1, -1, ""
}

// mapNormOffset maps a position in whitespace-normalized text back to the original.
func mapNormOffset(original, normalized string, normPos int) int {
	ni := 0
	for oi := 0; oi < len(original); oi++ {
		if ni >= normPos {
			return oi
		}
		oc := original[oi]
		if oc == ' ' || oc == '\t' {
			// In normalized, consecutive whitespace collapses to one space
			if ni < len(normalized) && normalized[ni] == ' ' {
				ni++
			}
			// Skip remaining original whitespace
			for oi+1 < len(original) && (original[oi+1] == ' ' || original[oi+1] == '\t') {
				oi++
			}
		} else {
			ni++
		}
	}
	return len(original)
}

// mapTrimOffset maps a position in trim-lines text back to the original.
func mapTrimOffset(original string, trimPos int) int {
	origLines := strings.Split(original, "\n")
	trimLines := strings.Split(trimLines(original), "\n")

	ti := 0 // position in trimmed text
	for lineIdx, tl := range trimLines {
		if lineIdx >= len(origLines) {
			break
		}
		ol := origLines[lineIdx]
		leadingWS := len(ol) - len(strings.TrimLeft(ol, " \t"))

		if ti+len(tl) >= trimPos {
			// Target is in this line
			offsetInTrimLine := trimPos - ti
			// Find cumulative original offset for this line
			origLineStart := 0
			for i := 0; i < lineIdx; i++ {
				origLineStart += len(origLines[i]) + 1 // +1 for \n
			}
			return origLineStart + leadingWS + offsetInTrimLine
		}
		ti += len(tl) + 1 // +1 for \n
	}
	return len(original)
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
