package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	All         bool   `json:"all,omitempty"` // replace all occurrences (text-based)
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
			resolved = append(resolved, resolvedEdit{
				File: file, StartByte: sym.StartByte, EndByte: sym.EndByte,
				Replacement: e.resolvedNewText(), ExpectHash: e.ExpectHash,
				Description: fmt.Sprintf("replace symbol %s in %s", e.Symbol, output.Rel(file)),
			})

		case e.OldText != "":
			// Text-based edit — resolve to byte spans
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: read %s: %w", i, output.Rel(file), err)
			}
			content := string(data)
			idx := strings.Index(content, e.OldText)
			if idx < 0 {
				return nil, fmt.Errorf("edit-plan: edit %d: old_text not found in %s", i, output.Rel(file))
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
						Replacement: e.resolvedNewText(), ExpectHash: e.ExpectHash,
						Description: fmt.Sprintf("replace text in %s (occurrence %d)", output.Rel(file), j+1),
					})
				}
			} else {
				resolved = append(resolved, resolvedEdit{
					File: file, StartByte: uint32(idx), EndByte: uint32(idx + len(e.OldText)),
					Replacement: e.resolvedNewText(), ExpectHash: e.ExpectHash,
					Description: fmt.Sprintf("replace text in %s", output.Rel(file)),
				})
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
			resolved = append(resolved, resolvedEdit{
				File: file, StartByte: startByte, EndByte: endByte,
				Replacement: e.resolvedNewText(), ExpectHash: e.ExpectHash,
				Description: fmt.Sprintf("replace lines %d-%d in %s", e.StartLine, e.EndLine, output.Rel(file)),
			})

		default:
			return nil, fmt.Errorf("edit-plan: edit %d: must specify symbol, old_text, or start_line/end_line", i)
		}
	}

	// Dry-run: return what would happen
	if dryRun {
		var preview []map[string]any
		for _, r := range resolved {
			entry := map[string]any{
				"file":        output.Rel(r.File),
				"description": r.Description,
			}
			diff, err := edit.DiffPreview(r.File, r.StartByte, r.EndByte, r.Replacement)
			if err == nil && diff != "" {
				entry["diff"] = diff
			}
			preview = append(preview, entry)
		}
		return map[string]any{
			"dry_run": true,
			"edits":   preview,
			"count":   len(preview),
		}, nil
	}

	cr, err := commitEdits(ctx, db, resolved)
	if err != nil {
		return nil, fmt.Errorf("edit-plan: %w", err)
	}

	var descriptions []string
	for _, r := range resolved {
		descriptions = append(descriptions, r.Description)
	}

	result := map[string]any{
		"ok":          true,
		"edits":       cr.EditCount,
		"files":       cr.FileCount,
		"hashes":      cr.Hashes,
		"description": descriptions,
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

	// Apply --scope filter (glob pattern on relative paths)
	if scope := flagString(flags, "scope", ""); scope != "" {
		var filtered []index.SymbolInfo
		for _, r := range refs {
			rel := output.Rel(r.File)
			if matched, _ := filepath.Match(scope, rel); matched {
				filtered = append(filtered, r)
			} else if matched, _ := filepath.Match(scope, filepath.Base(rel)); matched {
				filtered = append(filtered, r)
			} else if strings.HasPrefix(rel, strings.TrimSuffix(scope, "**")) {
				// Support "dir/**" style patterns
				filtered = append(filtered, r)
			}
		}
		refs = filtered
	}

	if len(refs) == 0 {
		return output.RenameResult{OldName: oldName, NewName: newName, DryRun: dryRun}, nil
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

	if len(cr.IndexErrors) > 0 {
		var parts []string
		for file, errMsg := range cr.IndexErrors {
			parts = append(parts, file+": "+errMsg)
		}
		return nil, fmt.Errorf("rename applied but re-index failed: %s", strings.Join(parts, "; "))
	}

	// Verify the new symbol is queryable — if not, the index is stale despite
	// IndexFile succeeding (e.g., WAL visibility issue).
	if _, err := db.ResolveSymbol(ctx, newName); err != nil {
		return nil, fmt.Errorf("rename applied and re-indexed, but new symbol %q not found in index — try 'edr init'", newName)
	}

	return output.RenameResult{
		OldName:      oldName,
		NewName:      newName,
		FilesChanged: filesChanged,
		Occurrences:  len(refs),
		Hashes:       cr.Hashes,
	}, nil
}
