package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// editPlanEntry describes a single edit within an edit-plan.
type editPlanEntry struct {
	File        string `json:"file"`
	Symbol      string `json:"symbol,omitempty"`      // symbol-based edit
	StartLine   int    `json:"start_line,omitempty"`  // line-based edit
	EndLine     int    `json:"end_line,omitempty"`    // line-based edit
	OldText     string `json:"old_text,omitempty"`    // text-based edit (find this text)
	NewText     string `json:"new_text,omitempty"`    // the replacement content (all modes)
	Replacement string `json:"replacement,omitempty"` // legacy alias for new_text
	ExpectHash  string `json:"expect_hash,omitempty"`
	All         bool   `json:"all,omitempty"`   // replace all occurrences (text-based)
}

// resolvedNewText returns the effective replacement text, preferring new_text over replacement.
func (e editPlanEntry) resolvedNewText() string {
	if e.NewText != "" {
		return e.NewText
	}
	return e.Replacement
}

func runEditPlan(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	// Edits come from flags.edits as a JSON array
	rawEdits, ok := flags["edits"]
	if !ok {
		return nil, fmt.Errorf("edit-plan requires 'edits' array in flags")
	}

	// Re-marshal to get proper typing from the any interface
	var edits []editPlanEntry
	switch v := rawEdits.(type) {
	case []any:
		// JSON came through as []any — need to re-marshal
		rawJSON, _ := json.Marshal(v)
		if err := json.Unmarshal(rawJSON, &edits); err != nil {
			return nil, fmt.Errorf("edit-plan: invalid edits array: %w", err)
		}
	default:
		rawJSON, _ := json.Marshal(v)
		if err := json.Unmarshal(rawJSON, &edits); err != nil {
			return nil, fmt.Errorf("edit-plan: invalid edits: %w", err)
		}
	}

	if len(edits) == 0 {
		return nil, fmt.Errorf("edit-plan: edits array is empty")
	}

	dryRun := flagBool(flags, "dry-run", false)

	var resolved []resolvedEdit

	for i, e := range edits {
		if e.File == "" {
			return nil, fmt.Errorf("edit-plan: edit %d missing 'file'", i)
		}
		file, err := db.ResolvePath(e.File)
		if err != nil {
			return nil, fmt.Errorf("edit-plan: edit %d: %w", i, err)
		}

		switch {
		case e.Symbol != "":
			// Symbol-based edit
			sym, err := db.GetSymbol(ctx, file, e.Symbol)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: symbol %q: %w", i, e.Symbol, err)
			}
			expectHash := e.ExpectHash
			if expectHash == "" {
				expectHash, _ = edit.FileHash(file)
			}
			resolved = append(resolved, resolvedEdit{
				File: file, StartByte: sym.StartByte, EndByte: sym.EndByte,
				Replacement: e.resolvedNewText(), ExpectHash: expectHash,
				Description: fmt.Sprintf("replace symbol %s in %s", e.Symbol, output.Rel(file)),
			})

		case e.OldText != "":
			// Detect no-op: old_text == new_text means nothing would change
			if e.OldText == e.resolvedNewText() {
				// Even for no-ops, validate expect_hash if provided
				if e.ExpectHash != "" {
					currentHash, err := edit.FileHash(file)
					if err != nil {
						return nil, fmt.Errorf("edit-plan: edit %d: hash check: %w", i, err)
					}
					if currentHash != e.ExpectHash {
						return nil, fmt.Errorf("edit-plan: edit %d: hash mismatch on %s: expected %s, got %s (file was modified)", i, output.Rel(file), e.ExpectHash, currentHash)
					}
				}
				return map[string]any{
					"ok":      true,
					"noop":    true,
					"message": fmt.Sprintf("edit %d: old_text equals new_text, no change applied", i),
				}, nil
			}
			// Text-based edit — resolve to byte spans
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: read %s: %w", i, output.Rel(file), err)
			}
			content := string(data)

			// Auto-compute hash if caller didn't provide one
			expectHash := e.ExpectHash
			if expectHash == "" {
				expectHash = edit.HashBytes(data)
			}

			{
				// Literal text-based edit
				idx := strings.Index(content, e.OldText)
				if idx < 0 {
					nfErr := notFoundError(content, output.Rel(file), e.OldText)
					nfErr.EditIndex = intPtr(i)
					nfErr.EditMode = "old_text"
					nfErr.TotalEdits = intPtr(len(edits))
					return nil, fmt.Errorf("edit-plan: edit %d: %w", i, nfErr)
				}
				// Check for ambiguous matches before proceeding
				totalMatches := strings.Count(content, e.OldText)
				if totalMatches > 1 && !e.All {
					locs := make([][]int, 0, totalMatches)
					off := 0
					for {
						j := strings.Index(content[off:], e.OldText)
						if j < 0 {
							break
						}
						abs := off + j
						locs = append(locs, []int{abs, abs + len(e.OldText)})
						off = abs + len(e.OldText)
					}
					return nil, fmt.Errorf("edit-plan: edit %d: %w", i, ambiguousMatchError(content, output.Rel(file), e.OldText, locs))
				}
				if e.All {
					// For replace-all, collect all occurrences (reverse order for offset stability)
					var offsets []int
					start := 0
					for {
						idx := strings.Index(content[start:], e.OldText)
						if idx < 0 {
							break
						}
						offsets = append(offsets, start+idx)
						start += idx + len(e.OldText)
					}
					for j := len(offsets) - 1; j >= 0; j-- {
						resolved = append(resolved, resolvedEdit{
							File: file, StartByte: uint32(offsets[j]), EndByte: uint32(offsets[j] + len(e.OldText)),
							Replacement: e.resolvedNewText(), ExpectHash: expectHash,
							Description: fmt.Sprintf("replace text in %s (occurrence %d)", output.Rel(file), j+1),
						})
					}
				} else {
					resolved = append(resolved, resolvedEdit{
						File: file, StartByte: uint32(idx), EndByte: uint32(idx + len(e.OldText)),
						Replacement: e.resolvedNewText(), ExpectHash: expectHash,
						Description: fmt.Sprintf("replace text in %s", output.Rel(file)),
					})
				}
			}

		case e.StartLine > 0 && e.EndLine > 0:
			// Line-based edit — convert to byte offsets
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: read %s: %w", i, output.Rel(file), err)
			}
			var startByte, endByte uint32
			line := 1
			foundStart := false
			for j := 0; j <= len(data); j++ {
				if line == e.StartLine && !foundStart {
					startByte = uint32(j)
					foundStart = true
				}
				if line == e.EndLine+1 || (line == e.EndLine && j == len(data)) {
					endByte = uint32(j)
					break
				}
				if j < len(data) && data[j] == '\n' {
					line++
				}
			}
			if !foundStart {
				return nil, fmt.Errorf("edit-plan: edit %d: start line %d beyond file", i, e.StartLine)
			}
			expectHash := e.ExpectHash
			if expectHash == "" {
				expectHash = edit.HashBytes(data)
			}
			resolved = append(resolved, resolvedEdit{
				File: file, StartByte: startByte, EndByte: endByte,
				Replacement: e.resolvedNewText(), ExpectHash: expectHash,
				Description: fmt.Sprintf("replace lines %d-%d in %s", e.StartLine, e.EndLine, output.Rel(file)),
			})

		default:
			return nil, fmt.Errorf("edit-plan: edit %d: must specify symbol, old_text, or start_line/end_line", i)
		}
	}

	// Detect overlapping edits on the same file before applying.
	// Group by file and check for byte-range overlaps.
	{
		byFile := make(map[string][]resolvedEdit)
		for _, r := range resolved {
			byFile[r.File] = append(byFile[r.File], r)
		}
		for _, edits := range byFile {
			if len(edits) < 2 {
				continue
			}
			// Sort by StartByte ascending.
			sort.Slice(edits, func(i, j int) bool {
				return edits[i].StartByte < edits[j].StartByte
			})
			for i := 1; i < len(edits); i++ {
				prev := edits[i-1]
				curr := edits[i]
				// Overlap: curr starts before prev ends (insertions at same point are OK).
				if curr.StartByte < prev.EndByte && !(prev.StartByte == prev.EndByte || curr.StartByte == curr.EndByte) {
					return nil, fmt.Errorf("edit-plan: overlapping edits on %s: [%d:%d] and [%d:%d] — split into separate calls",
						output.Rel(prev.File), prev.StartByte, prev.EndByte, curr.StartByte, curr.EndByte)
				}
			}
		}
	}

	// Dry-run: apply all edits in memory, produce combined per-file diffs.
	if dryRun {
		// Group edits by file, preserving order.
		type fileEdits struct {
			file     string
			original []byte
			edits    []resolvedEdit
			descs    []string
		}
		fileOrder := make([]string, 0)
		byFile := make(map[string]*fileEdits)
		for _, r := range resolved {
			rel := output.Rel(r.File)
			fe, ok := byFile[rel]
			if !ok {
				data, err := os.ReadFile(r.File)
				if err != nil {
					return nil, fmt.Errorf("dry-run: read %s: %w", rel, err)
				}
				fe = &fileEdits{file: r.File, original: data}
				byFile[rel] = fe
				fileOrder = append(fileOrder, rel)
			}
			fe.edits = append(fe.edits, r)
			fe.descs = append(fe.descs, r.Description)
		}

		var preview []map[string]any
		for _, rel := range fileOrder {
			fe := byFile[rel]
			// Apply all edits for this file in reverse byte order (highest offset first)
			// to preserve offsets for earlier edits.
			result := make([]byte, len(fe.original))
			copy(result, fe.original)
			sorted := make([]resolvedEdit, len(fe.edits))
			copy(sorted, fe.edits)
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].StartByte > sorted[j].StartByte
			})
			for _, r := range sorted {
				result = append(result[:r.StartByte], append([]byte(r.Replacement), result[r.EndByte:]...)...)
			}
			diff := edit.UnifiedDiff(rel, fe.original, result)

			// Compute edit metrics from the replaced spans, matching standalone.
			// Sum old/new span sizes across all edits for this file.
			var totalOldBytes, totalNewBytes int
			var totalOldNewlines, totalNewNewlines int
			for _, r := range fe.edits {
				oldSpan := fe.original[r.StartByte:r.EndByte]
				totalOldBytes += len(oldSpan)
				totalNewBytes += len(r.Replacement)
				totalOldNewlines += bytes.Count(oldSpan, []byte("\n"))
				totalNewNewlines += strings.Count(r.Replacement, "\n")
			}
			linesAdded := 0
			linesRemoved := 0
			if totalNewNewlines > totalOldNewlines {
				linesAdded = totalNewNewlines - totalOldNewlines
			} else {
				linesRemoved = totalOldNewlines - totalNewNewlines
			}

			// Count changed lines from the diff (matching session.CountDiffLines)
			changedLines := 0
			for _, line := range strings.Split(diff, "\n") {
				if len(line) > 0 && (line[0] == '+' || line[0] == '-') {
					if !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
						changedLines++
					}
				}
			}

			entry := map[string]any{
				"file":           rel,
				"description":    fe.descs,
				"status":         "dry_run",
				"destructive":    len(result) == 0,
				"old_size":       totalOldBytes / 4,
				"new_size":       totalNewBytes / 4,
				"lines_added":    linesAdded,
				"lines_removed":  linesRemoved,
				"lines_changed":  changedLines,
				"diff_available": diff != "",
			}
			if diff != "" {
				entry["diff"] = diff
			}
			preview = append(preview, entry)
		}
		return map[string]any{
			"dry_run": true,
			"edits":   preview,
			"count":   len(resolved),
		}, nil
	}

	// Snapshot file contents before commit so we can compute diffs.
	preContents := make(map[string][]byte)
	for _, r := range resolved {
		rel := output.Rel(r.File)
		if _, seen := preContents[rel]; !seen {
			if data, err := os.ReadFile(r.File); err == nil {
				preContents[rel] = data
			}
		}
	}

	cr, err := commitEdits(ctx, db, resolved)
	if err != nil {
		return nil, fmt.Errorf("edit-plan: %w", err)
	}

	var descriptions []string
	for _, r := range resolved {
		descriptions = append(descriptions, r.Description)
	}

	// Compute per-file unified diffs for the session's slim-edit pipeline.
	// Sort keys for deterministic output order.
	diffKeys := make([]string, 0, len(preContents))
	for rel := range preContents {
		diffKeys = append(diffKeys, rel)
	}
	sort.Strings(diffKeys)
	var diffs []string
	for _, rel := range diffKeys {
		oldData := preContents[rel]
		absPath := filepath.Join(root, rel)
		newData, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		if d := edit.UnifiedDiff(rel, oldData, newData); d != "" {
			diffs = append(diffs, d)
		}
	}

	result := map[string]any{
		"ok":          true,
		"status":      cr.Status,
		"edits":       cr.EditCount,
		"files":       cr.FileCount,
		"hashes":      cr.Hashes,
		"description": descriptions,
	}
	if len(diffs) > 0 {
		result["diff"] = strings.Join(diffs, "\n")
	}
	if len(cr.IndexErrors) > 0 {
		result["index_errors"] = cr.IndexErrors
	}
	return result, nil
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

// intPtr returns a pointer to the given int.
func intPtr(v int) *int { return &v }
