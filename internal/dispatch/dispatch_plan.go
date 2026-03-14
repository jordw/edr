package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	Regex       bool   `json:"regex,omitempty"` // treat old_text as regex
	Move        string `json:"move,omitempty"`  // symbol to move
	After       string `json:"after,omitempty"`  // place after this symbol
	Before      string `json:"before,omitempty"` // place before this symbol
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
			if !e.Regex && e.OldText == e.resolvedNewText() {
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

			if e.Regex {
				// Regex-based edit
				re, err := regexp.Compile(e.OldText)
				if err != nil {
					return nil, fmt.Errorf("edit-plan: edit %d: invalid regex: %w", i, err)
				}
				matches := re.FindAllStringIndex(content, -1)
				if len(matches) == 0 {
					return nil, fmt.Errorf("edit-plan: edit %d: regex %q not found in %s", i, e.OldText, output.Rel(file))
				}
				if len(matches) > 1 && !e.All {
					locs := make([][]int, len(matches))
					copy(locs, matches)
					return nil, fmt.Errorf("edit-plan: edit %d: %w", i, ambiguousMatchError(content, output.Rel(file), e.OldText, locs))
				}
				// Build replacements in reverse order for offset stability
				for j := len(matches) - 1; j >= 0; j-- {
					m := matches[j]
					// Support capture group references ($1, $2) in replacement
					repl := re.ReplaceAllString(content[m[0]:m[1]], e.resolvedNewText())
					desc := fmt.Sprintf("regex replace in %s", output.Rel(file))
					if e.All && len(matches) > 1 {
						desc = fmt.Sprintf("regex replace in %s (occurrence %d)", output.Rel(file), j+1)
					}
					resolved = append(resolved, resolvedEdit{
						File: file, StartByte: uint32(m[0]), EndByte: uint32(m[1]),
						Replacement: repl, ExpectHash: expectHash,
						Description: desc,
					})
				}
			} else {
				// Literal text-based edit
				idx := strings.Index(content, e.OldText)
				if idx < 0 {
					return nil, fmt.Errorf("edit-plan: edit %d: %w", i, notFoundError(content, output.Rel(file), e.OldText))
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

		case e.Move != "":
			// Move-symbol edit — resolves to a delete + insert pair
			if e.After == "" && e.Before == "" {
				return nil, fmt.Errorf("edit-plan: edit %d: move requires 'after' or 'before'", i)
			}
			src, err := db.GetSymbol(ctx, file, e.Move)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: move source %q: %w", i, e.Move, err)
			}
			destName := e.After
			if destName == "" {
				destName = e.Before
			}
			dest, err := db.GetSymbol(ctx, file, destName)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: move destination %q: %w", i, destName, err)
			}
			if src.StartByte < dest.EndByte && dest.StartByte < src.EndByte {
				return nil, fmt.Errorf("edit-plan: edit %d: cannot move %s: overlaps with %s", i, e.Move, destName)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: read %s: %w", i, output.Rel(file), err)
			}
			expectHash := e.ExpectHash
			if expectHash == "" {
				expectHash = edit.HashBytes(data)
			}
			// Compute delete range (consume surrounding blank lines)
			delStart, delEnd := src.StartByte, src.EndByte
			if pos := int(delStart); pos > 0 && data[pos-1] == '\n' {
				pos--
				if pos > 0 && data[pos-1] == '\n' {
					pos--
				}
				delStart = uint32(pos + 1)
			}
			if pos := int(delEnd); pos < len(data) {
				for pos < len(data) && data[pos] == '\n' {
					pos++
				}
				delEnd = uint32(pos)
			}
			symbolText := strings.TrimRight(string(data[src.StartByte:src.EndByte]), "\n")
			var insertAt uint32
			var insertion string
			if e.After != "" {
				insertAt = dest.EndByte
				insertion = "\n\n" + symbolText + "\n"
			} else {
				insertAt = dest.StartByte
				insertion = symbolText + "\n\n"
			}
			resolved = append(resolved,
				resolvedEdit{File: file, StartByte: delStart, EndByte: delEnd, Replacement: "", ExpectHash: expectHash,
					Description: fmt.Sprintf("move %s (delete)", e.Move)},
				resolvedEdit{File: file, StartByte: insertAt, EndByte: insertAt, Replacement: insertion, ExpectHash: expectHash,
					Description: fmt.Sprintf("move %s (insert %s %s)", e.Move, map[bool]string{true: "after", false: "before"}[e.After != ""], destName)},
			)

		default:
			return nil, fmt.Errorf("edit-plan: edit %d: must specify symbol, old_text, start_line/end_line, or move", i)
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
