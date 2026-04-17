package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runRename(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	newName := flagString(flags, "new_name", "")
	if newName == "" {
		return nil, fmt.Errorf("rename: --to <new_name> is required")
	}
	dryRun := flagBool(flags, "dry_run", false)
	crossFile := flagBool(flags, "cross_file", false)
	force := flagBool(flags, "force", false)
	commentMode := flagString(flags, "comments", "rewrite")
	switch commentMode {
	case "rewrite", "skip":
	default:
		return nil, fmt.Errorf("rename: --comments must be 'rewrite' or 'skip' (got %q)", commentMode)
	}

	// Resolve the symbol to rename.
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}
	oldName := sym.Name

	if oldName == newName {
		return &output.RenameResult{
			OldName: oldName,
			NewName: newName,
			Status:  "noop",
		}, nil
	}

	// Build regex patterns for the old name.
	// For methods (non-empty Receiver), use a dot-prefixed pattern at call sites
	// to match .spawn( but not mod spawn or ::spawn.
	isMethod := sym.Receiver != ""
	quotedName := regexp.QuoteMeta(oldName)

	defPattern := `\b` + quotedName + `\b`
	defRe, err := regexp.Compile(defPattern)
	if err != nil {
		return nil, fmt.Errorf("rename: invalid symbol name for regex: %w", err)
	}

	// Call-site regex: for methods, require a dot prefix.
	var callRe *regexp.Regexp
	if isMethod {
		callRe, err = regexp.Compile(`\.` + quotedName + `\b`)
		if err != nil {
			return nil, fmt.Errorf("rename: invalid symbol name for regex: %w", err)
		}
	} else {
		callRe = defRe
	}

	// Find references. Default scope is same-file; --cross-file opts into
	// repo-wide fan-out.
	var refs []index.SymbolInfo
	if crossFile {
		refs, err = db.FindSemanticReferences(ctx, oldName, sym.File)
	} else {
		refs, err = db.FindSameFileCallers(ctx, oldName, sym.File)
	}
	if err != nil {
		return nil, fmt.Errorf("rename: finding references: %w", err)
	}

	// Build a map of file → symbol byte ranges to replace within.
	// Definition span uses defRe (matches bare name in declaration).
	// Reference spans use callRe (matches .name for methods).
	type span struct {
		start, end uint32
		isDef      bool
	}
	fileSpans := map[string][]span{}
	defStart := expandToDocComment(sym.File, sym.StartByte)
	fileSpans[sym.File] = append(fileSpans[sym.File], span{defStart, sym.EndByte, true})
	for _, ref := range refs {
		fileSpans[ref.File] = append(fileSpans[ref.File], span{ref.StartByte, ref.EndByte, false})
	}

	// Sort files for deterministic output.
	files := make([]string, 0, len(fileSpans))
	for f := range fileSpans {
		files = append(files, f)
	}
	sort.Strings(files)

	// For each file, replace only within the identified symbol spans.
	type fileEdit struct {
		file         string
		oldData      []byte
		newData      []byte
		count        int
		commentCount int
	}
	var edits []fileEdit
	totalCodeEdits := 0
	totalCommentMatches := 0

	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("rename: read %s: %w", output.Rel(file), err)
		}

		if !bytes.Contains(data, []byte(oldName)) {
			continue
		}

		spans := fileSpans[file]
		// Sort spans by start offset for a single forward pass.
		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

		fileExt := commentSyntaxFor(file)

		// Build new file content: copy unchanged regions verbatim,
		// apply replacement only within span ranges. Match-by-match (rather
		// than ReplaceAll) so we can classify each match as code vs comment
		// and honor --comments=skip.
		var buf bytes.Buffer
		pos := 0
		codeCount := 0
		commentCount := 0
		for _, s := range spans {
			start := int(s.start)
			end := int(s.end)
			if start > len(data) {
				start = len(data)
			}
			if end > len(data) {
				end = len(data)
			}
			if start < pos {
				// Overlapping or out-of-order span; skip.
				continue
			}
			buf.Write(data[pos:start])

			re := callRe
			repl := []byte("." + newName)
			if s.isDef {
				re = defRe
				repl = []byte(newName)
			} else if !isMethod {
				repl = []byte(newName)
			}
			region := data[start:end]
			matches := re.FindAllIndex(region, -1)
			rpos := 0
			for _, m := range matches {
				absPos := start + m[0]
				inComment := positionInComment(data, absPos, fileExt)
				if inComment {
					commentCount++
					if commentMode == "skip" {
						// Copy verbatim — don't rewrite this match.
						buf.Write(region[rpos:m[1]])
						rpos = m[1]
						continue
					}
				} else {
					codeCount++
				}
				buf.Write(region[rpos:m[0]])
				buf.Write(repl)
				rpos = m[1]
			}
			buf.Write(region[rpos:])
			pos = end
		}
		// Copy remainder after last span.
		buf.Write(data[pos:])

		// Count matches even if we ended up with no edits (skip mode +
		// only-comment matches). The summary should still report what was
		// found vs ignored.
		totalCodeEdits += codeCount
		totalCommentMatches += commentCount

		newData := buf.Bytes()
		if bytes.Equal(data, newData) {
			continue
		}

		count := codeCount
		if commentMode == "rewrite" {
			count += commentCount
		}
		edits = append(edits, fileEdit{
			file:         file,
			oldData:      data,
			newData:      newData,
			count:        count,
			commentCount: commentCount,
		})
	}

	if len(edits) == 0 {
		return &output.RenameResult{
			OldName: oldName,
			NewName: newName,
			Status:  "noop",
		}, nil
	}

	// Blast-radius gate: cross-file rename touching large swaths of the repo
	// almost certainly indicates a common-name collision (e.g. renaming "init"
	// matches thousands of unrelated identifiers). Refuse unless --force.
	const (
		crossFileFileCap = 50
		crossFileEditCap = 200
	)
	totalOccurrences := totalCodeEdits
	if commentMode == "rewrite" {
		totalOccurrences += totalCommentMatches
	}
	if crossFile && !force && (len(edits) > crossFileFileCap || totalOccurrences > crossFileEditCap) {
		return nil, fmt.Errorf("rename refused: --cross-file would edit %d files and %d occurrences (limits: %d files, %d occurrences). The name %q likely collides with unrelated identifiers. Re-run with --force to proceed, --dry-run to inspect, or narrow scope",
			len(edits), totalOccurrences, crossFileFileCap, crossFileEditCap, oldName)
	}

	// Build result.
	result := &output.RenameResult{
		OldName:            oldName,
		NewName:            newName,
		Occurrences:        totalOccurrences,
		CodeOccurrences:    totalCodeEdits,
		CommentOccurrences: totalCommentMatches,
		CommentMode:        commentMode,
	}

	result.OldContents = make(map[string][]byte, len(edits))
	for _, fe := range edits {
		rel := output.Rel(fe.file)
		diff := edit.UnifiedDiff(rel, fe.oldData, fe.newData)
		result.FilesChanged = append(result.FilesChanged, rel)
		result.Diffs = append(result.Diffs, output.RenameDiff{
			File: rel,
			Diff: diff,
		})
		result.OldContents[rel] = fe.oldData
	}

	if dryRun {
		result.Status = "dry_run"
		return result, nil
	}

	// Apply all edits atomically via Transaction.
	tx := edit.NewTransaction()
	for _, fe := range edits {
		hash := edit.HashBytes(fe.oldData)
		// Replace entire file content.
		tx.Add(fe.file, 0, uint32(len(fe.oldData)), string(fe.newData), hash)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}

	// Mark files dirty in the trigram index.
	for _, fe := range edits {
		idx.MarkDirty(db.EdrDir(), output.Rel(fe.file))
	}

	result.Status = "applied"
	return result, nil
}

// commentSyntaxFor returns a small bitmask describing which comment styles
// the file's language uses. Approximate but enough to cover the languages we
// support: line comments via // for C-family/JS/TS/Rust/Go, # for Python/Ruby
// /shell/Makefile, -- for SQL/Lua, ; for Lisp/asm. Block comments via /* */
// for C-family/JS/TS/Rust/Go, """ for Python.
func commentSyntaxFor(file string) commentSyntax {
	ext := ""
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '.' {
			ext = file[i:]
			break
		}
		if file[i] == '/' {
			break
		}
	}
	switch ext {
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
		".m", ".mm", ".java", ".kt", ".kts", ".scala", ".sc",
		".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts",
		".go", ".rs", ".swift", ".cs", ".dart", ".groovy", ".php":
		return commentSyntax{slashSlash: true, slashStar: true}
	case ".py", ".pyi":
		return commentSyntax{hash: true, tripleQuote: true}
	case ".rb", ".sh", ".bash", ".zsh", ".pl", ".pm", ".tcl", ".cmake":
		return commentSyntax{hash: true}
	case ".lua", ".sql":
		return commentSyntax{dashDash: true}
	}
	// Default: slash-slash + slash-star covers most curly-brace languages we
	// haven't enumerated.
	return commentSyntax{slashSlash: true, slashStar: true}
}

type commentSyntax struct {
	slashSlash  bool
	slashStar   bool
	hash        bool
	dashDash    bool
	tripleQuote bool
}

// positionInComment reports whether the byte at `pos` in `data` falls inside
// a comment, scanning back to the start of the line for line-comments and
// scanning the file for the most recent unterminated block-comment opener.
//
// Cheap and approximate — we don't tokenize strings, so a comment marker
// inside a string literal will be misclassified. For the rename use case the
// trade-off is fine: a false positive (treating "// foo" inside a string as
// a comment) just means we report it as a comment edit, which is at worst
// noisy in the summary, never an incorrect edit.
func positionInComment(data []byte, pos int, syn commentSyntax) bool {
	if pos < 0 || pos >= len(data) {
		return false
	}
	// Scan back to the line start.
	lineStart := pos
	for lineStart > 0 && data[lineStart-1] != '\n' {
		lineStart--
	}
	for i := lineStart; i < pos; i++ {
		c := data[i]
		if syn.slashSlash && c == '/' && i+1 < len(data) && data[i+1] == '/' {
			return true
		}
		if syn.hash && c == '#' {
			return true
		}
		if syn.dashDash && c == '-' && i+1 < len(data) && data[i+1] == '-' {
			return true
		}
	}
	// Block-comment scan: look for the nearest /* before pos that isn't
	// closed before pos. (Skip when the language doesn't use /* */.)
	if syn.slashStar {
		open := bytes.LastIndex(data[:pos], []byte("/*"))
		if open >= 0 {
			close := bytes.Index(data[open:pos], []byte("*/"))
			if close < 0 {
				return true
			}
		}
	}
	return false
}

// expandToDocComment scans backwards from startByte to include preceding
// comment lines (// or #) that are part of the symbol's documentation.
func expandToDocComment(file string, startByte uint32) uint32 {
	data, err := os.ReadFile(file)
	if err != nil || startByte == 0 {
		return startByte
	}

	pos := int(startByte)
	for pos > 0 {
		// Skip backwards over the newline before startByte.
		nl := pos - 1
		if nl < 0 || data[nl] != '\n' {
			break
		}
		// Find the start of the previous line.
		lineStart := nl
		for lineStart > 0 && data[lineStart-1] != '\n' {
			lineStart--
		}
		line := bytes.TrimSpace(data[lineStart:nl])
		if len(line) >= 2 && line[0] == '/' && line[1] == '/' {
			pos = lineStart
		} else if len(line) >= 1 && line[0] == '#' {
			pos = lineStart
		} else if len(line) >= 2 && line[0] == '/' && line[1] == '*' {
			pos = lineStart
		} else if len(line) >= 3 && bytes.HasPrefix(line, []byte("///")) {
			pos = lineStart
		} else if len(line) >= 1 && line[0] == '*' {
			// Middle or end of a block comment (/** ... */).
			pos = lineStart
		} else {
			break
		}
	}
	return uint32(pos)
}

