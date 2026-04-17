package dispatch

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runSmartEdit(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	dryRun := flagBool(flags, "dry-run", false)
	readBack := flagBool(flags, "read_back", true)

	// Delegate to inner logic, then optionally attach read-back context.
	result, err := runSmartEditInner(ctx, db, root, args, flags, dryRun)
	if err != nil && strings.Contains(err.Error(), "hash mismatch") && flagBool(flags, "refresh_hash", false) {
		if refreshed := refreshEditHash(db, args, flags); refreshed {
			result, err = runSmartEditInner(ctx, db, root, args, flags, dryRun)
		}
	}
	if err != nil || !readBack {
		return result, err
	}

	// Annotate with target resolution provenance
	if m, ok := result.(map[string]any); ok {
		annotateEditMode(m, flags)
		// For dry-run, add index freshness context
		if dryRun {
			edrDir := db.EdrDir()
			if idx.IsDirty(edrDir) {
				m["index_state"] = "dirty"
			} else if idx.HasSymbolIndex(edrDir) {
				m["index_state"] = "fresh"
			} else {
				m["index_state"] = "none"
			}
		}
	}

	return attachReadBack(ctx, db, result)
}

// annotateEditMode adds target_origin and edit_mode to the result for provenance.
func annotateEditMode(result map[string]any, flags map[string]any) {
	if flagString(flags, "where", "") != "" {
		result["target_origin"] = "where"
	} else if flagString(flags, "move_after", "") != "" {
		result["target_origin"] = "move_after"
	}

	if flagString(flags, "in", "") != "" {
		result["edit_mode"] = "scoped_text_match"
	} else if flagInt(flags, "insert_at", 0) > 0 {
		result["edit_mode"] = "insert_at"
	} else if flagInt(flags, "start_line", 0) > 0 {
		result["edit_mode"] = "line_range"
	} else if flagString(flags, "old_text", "") != "" {
		result["edit_mode"] = "text_match"
	} else if _, hasSym := result["symbol"]; hasSym {
		result["edit_mode"] = "symbol"
	}
}

func refreshEditHash(db index.SymbolStore, args []string, flags map[string]any) bool {
	if len(args) == 0 {
		return false
	}
	target := args[0]
	if parts := splitFileSymbol(target); parts != nil {
		target = parts[0]
	}
	file, err := db.ResolvePath(target)
	if err != nil {
		return false
	}
	currentHash, err := edit.FileHash(file)
	if err != nil || currentHash == "" {
		return false
	}
	flags["expect_hash"] = currentHash
	return true
}

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
			return nil, fmt.Errorf("hash mismatch on %s: expected %s, got %s (file was modified)", output.Rel(file), expectHash, currentHash)
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

// attachReadBack reads context around the edit point and attaches it to the result.
// If the edit targeted a symbol, reads the full updated symbol body.
// Otherwise, reads ~10 lines above/below the diff location.
func attachReadBack(ctx context.Context, db index.SymbolStore, result any) (any, error) {
	m, ok := result.(map[string]any)
	if !ok {
		return result, nil
	}
	status, _ := m["status"].(string)
	if status != "applied" {
		return result, nil
	}

	relFile, _ := m["file"].(string)
	if relFile == "" {
		return result, nil
	}
	file, err := db.ResolvePath(relFile)
	if err != nil {
		return result, nil // best-effort
	}

	// Read the post-edit file and attach context around the edit.
	data, err := os.ReadFile(file)
	if err != nil {
		return result, nil
	}

	// If we know which symbol was edited, return its full updated body.
	if symName, ok := m["symbol"].(string); ok && symName != "" {
		db.InvalidateFiles(ctx, []string{file})
		sym, sErr := db.GetSymbol(ctx, file, symName)
		if sErr == nil && int(sym.EndByte) <= len(data) {
			m["read_back"] = map[string]any{
				"symbol":  symName,
				"lines":   [2]int{int(sym.StartLine), int(sym.EndLine)},
				"content": string(data[sym.StartByte:sym.EndByte]),
			}
			return result, nil
		}
	}

	// Otherwise, find the symbol containing the edit location.
	// Parse the post-edit file directly (fast, no cache issues).
	diff, _ := m["diff"].(string)
	editLine := diffStartLine(diff)
	if editLine > 0 {
		syms := index.Parse(file, data)
		var best *index.SymbolInfo
		for i := range syms {
			s := &syms[i]
			if int(s.StartLine) <= editLine && editLine <= int(s.EndLine) {
				if best == nil || s.StartLine >= best.StartLine {
					best = s
				}
			}
		}
		if best != nil && int(best.EndByte) <= len(data) {
			m["read_back"] = map[string]any{
				"symbol":  best.Name,
				"lines":   [2]int{int(best.StartLine), int(best.EndLine)},
				"content": string(data[best.StartByte:best.EndByte]),
			}
			return result, nil
		}
	}

	// Fallback: return lines around the edit point
	if editLine > 0 {
		lines := strings.SplitAfter(string(data), "\n")
		start := editLine - 5
		if start < 1 {
			start = 1
		}
		end := editLine + 5
		if end > len(lines) {
			end = len(lines)
		}
		m["read_back"] = map[string]any{
			"lines":   [2]int{start, end},
			"content": strings.Join(lines[start-1:end], ""),
		}
	}
	return result, nil
}

// diffStartLine extracts the new-file start line from a unified diff header.
// Parses the @@ -a,b +c,d @@ line and returns c.
func diffStartLine(diff string) int {
	hunkStart := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			idx := strings.Index(line, "+")
			if idx < 0 {
				continue
			}
			rest := line[idx+1:]
			n := 0
			for _, ch := range rest {
				if ch >= '0' && ch <= '9' {
					n = n*10 + int(ch-'0')
				} else {
					break
				}
			}
			hunkStart = n
			continue
		}
		// Find first actual change line (+ or -)
		if hunkStart > 0 && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
			return hunkStart
		}
		if hunkStart > 0 && (strings.HasPrefix(line, " ") || line == "") {
			hunkStart++
		}
	}
	return 0
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

// validateMoveSpan returns an error if the symbol's byte range is unusable
// for a move/extract (zero or inverted). Surfacing a clean error here keeps
// downstream slicing from panicking with "slice bounds out of range" when an
// upstream resolver picks an overload-only declaration that the parser
// couldn't fully bracket.
func validateMoveSpan(s *index.SymbolInfo, role string) error {
	if s == nil {
		return fmt.Errorf("%s symbol: nil", role)
	}
	if s.EndByte <= s.StartByte {
		return fmt.Errorf("%s symbol %q in %s has no body span (likely an overload or forward declaration); pick the implementation",
			role, s.Name, output.Rel(s.File))
	}
	return nil
}

// smartEditMoveAfter moves a source symbol after a target symbol within the same file.
func smartEditMoveAfter(ctx context.Context, db index.SymbolStore, root string, args []string, targetName string, dryRun bool) (any, error) {
	// Resolve source symbol
	srcSym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("source symbol: %w", err)
	}

	// Resolve target symbol — check if target specifies a different file (cross-file move).
	var tgtSym *index.SymbolInfo
	if parts := splitFileSymbol(targetName); parts != nil {
		tgtFile, err := db.ResolvePath(parts[0])
		if err != nil {
			return nil, fmt.Errorf("target file: %w", err)
		}
		tgtSym, err = db.GetSymbol(ctx, tgtFile, parts[1])
		if err != nil {
			return nil, fmt.Errorf("target symbol: %w", err)
		}
	} else {
		tgtSym, err = db.GetSymbol(ctx, srcSym.File, targetName)
		if err != nil {
			return nil, fmt.Errorf("target symbol: %w", err)
		}
	}

	if err := validateMoveSpan(srcSym, "source"); err != nil {
		return nil, err
	}
	if err := validateMoveSpan(tgtSym, "target"); err != nil {
		return nil, err
	}

	if srcSym.File != tgtSym.File {
		return smartEditMoveAcrossFiles(ctx, db, srcSym, tgtSym, dryRun)
	}

	data, err := os.ReadFile(srcSym.File)
	if err != nil {
		return nil, err
	}

	srcStart := int(srcSym.StartByte)
	srcEnd := int(srcSym.EndByte)
	// Include trailing newline in the cut
	if srcEnd < len(data) && data[srcEnd] == '\n' {
		srcEnd++
	}
	srcBody := string(data[srcStart:srcEnd])

	tgtEnd := int(tgtSym.EndByte)
	// Insert after target's trailing newline if present
	if tgtEnd < len(data) && data[tgtEnd] == '\n' {
		tgtEnd++
	}

	// Build the new file content: remove source, insert after target
	var result strings.Builder
	if srcStart < tgtEnd {
		// Source is before target: remove src, then insert after target
		result.Write(data[:srcStart])
		result.Write(data[srcEnd:tgtEnd])
		result.WriteString("\n")
		result.WriteString(srcBody)
		result.Write(data[tgtEnd:])
	} else {
		// Source is after target: insert after target, then remove src
		result.Write(data[:tgtEnd])
		result.WriteString("\n")
		result.WriteString(srcBody)
		result.Write(data[tgtEnd:srcStart])
		result.Write(data[srcEnd:])
	}

	newContent := result.String()
	diff := edit.UnifiedDiff(output.Rel(srcSym.File), data, []byte(newContent))

	if dryRun {
		return map[string]any{
			"file":   output.Rel(srcSym.File),
			"status": "dry_run",
			"diff":   diff,
			"symbol": srcSym.Name,
			"after":  tgtSym.Name,
		}, nil
	}

	// Write the new content
	hash, _ := edit.FileHash(srcSym.File)
	if err := os.WriteFile(srcSym.File, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	idx.MarkDirty(db.EdrDir(), output.Rel(srcSym.File))
	newHash, _ := edit.FileHash(srcSym.File)

	return map[string]any{
		"file":     output.Rel(srcSym.File),
		"status":   "applied",
		"diff":     diff,
		"hash":     newHash,
		"old_hash": hash,
		"symbol":   srcSym.Name,
		"after":    tgtSym.Name,
	}, nil
}

// smartEditMoveAcrossFiles moves a symbol from one file to another, placing it
// after the target symbol. Uses Transaction for atomic two-file writes.
func smartEditMoveAcrossFiles(_ context.Context, db index.SymbolStore, srcSym, tgtSym *index.SymbolInfo, dryRun bool) (any, error) {
	srcData, err := os.ReadFile(srcSym.File)
	if err != nil {
		return nil, fmt.Errorf("move: read source: %w", err)
	}
	dstData, err := os.ReadFile(tgtSym.File)
	if err != nil {
		return nil, fmt.Errorf("move: read dest: %w", err)
	}

	// Expand to include any doc comment preceding the symbol.
	docStart := int(expandToDocComment(srcSym.File, srcSym.StartByte))
	srcStart := docStart
	srcEnd := int(srcSym.EndByte)
	if srcEnd < len(srcData) && srcData[srcEnd] == '\n' {
		srcEnd++
	}
	// Also consume one leading blank line to avoid double-spacing.
	if srcStart > 0 && srcData[srcStart-1] == '\n' {
		srcStart--
	}
	srcBody := string(srcData[docStart:int(srcSym.EndByte)])

	// Remove from source.
	newSrc := make([]byte, 0, len(srcData)-(srcEnd-srcStart))
	newSrc = append(newSrc, srcData[:srcStart]...)
	newSrc = append(newSrc, srcData[srcEnd:]...)

	// Collapse runs of 3+ consecutive newlines down to 2 at the splice point.
	// This prevents artifacts like *//** when a doc comment block ended just
	// before the removed symbol and another begins right after.
	if srcStart > 0 && srcStart < len(newSrc) {
		i := srcStart
		// Count consecutive newlines around the splice.
		start := i
		for start > 0 && newSrc[start-1] == '\n' {
			start--
		}
		end := i
		for end < len(newSrc) && newSrc[end] == '\n' {
			end++
		}
		nlCount := end - start
		if nlCount > 2 {
			// Keep exactly 2 newlines (one blank line).
			cut := nlCount - 2
			copy(newSrc[start+2:], newSrc[end:])
			newSrc = newSrc[:len(newSrc)-cut]
		}
	}

	// Insert into dest after target symbol.
	tgtEnd := int(tgtSym.EndByte)
	if tgtEnd < len(dstData) && dstData[tgtEnd] == '\n' {
		tgtEnd++
	}
	insertion := "\n" + srcBody + "\n"
	newDst := make([]byte, 0, len(dstData)+len(insertion))
	newDst = append(newDst, dstData[:tgtEnd]...)
	newDst = append(newDst, []byte(insertion)...)
	newDst = append(newDst, dstData[tgtEnd:]...)

	srcDiff := edit.UnifiedDiff(output.Rel(srcSym.File), srcData, newSrc)
	dstDiff := edit.UnifiedDiff(output.Rel(tgtSym.File), dstData, newDst)
	combinedDiff := srcDiff + dstDiff

	if dryRun {
		return map[string]any{
			"file":   output.Rel(srcSym.File),
			"status": "dry_run",
			"diff":   combinedDiff,
			"symbol": srcSym.Name,
			"after":  tgtSym.Name,
			"dest":   output.Rel(tgtSym.File),
		}, nil
	}

	// Atomic commit via Transaction.
	srcHash := edit.HashBytes(srcData)
	dstHash := edit.HashBytes(dstData)
	tx := edit.NewTransaction()
	tx.Add(srcSym.File, 0, uint32(len(srcData)), string(newSrc), srcHash)
	tx.Add(tgtSym.File, 0, uint32(len(dstData)), string(newDst), dstHash)
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("move: %w", err)
	}

	idx.MarkDirty(db.EdrDir(), output.Rel(srcSym.File))
	idx.MarkDirty(db.EdrDir(), output.Rel(tgtSym.File))
	newHash, _ := edit.FileHash(tgtSym.File)

	return map[string]any{
		"file":   output.Rel(srcSym.File),
		"status": "applied",
		"diff":   combinedDiff,
		"hash":   newHash,
		"symbol": srcSym.Name,
		"after":  tgtSym.Name,
		"dest":   output.Rel(tgtSym.File),
	}, nil
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
			return nil, ambiguousMatchError(content, output.Rel(file), matchText, locs)
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
		return nil, ambiguousMatchError(content, output.Rel(file)+":"+symName, matchText, locs)
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
	Line       int    `json:"line"`
	Kind       string `json:"kind"` // "whitespace", "indentation", "partial"
	ActualText string `json:"actual_text,omitempty"`
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
		nfe.Hint = "old_text matches after normalizing whitespace — check tabs vs spaces, trailing spaces, or line endings; or use --old-text @file for exact content"
		nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "whitespace"}
		nfe.NextAction = fmt.Sprintf("re-read %s and copy exact whitespace, or write old_text to a file and use --old-text @/tmp/old.txt", relFile)
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
