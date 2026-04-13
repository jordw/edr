package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runChangeSig(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	addParam := flagString(flags, "add", "")
	removeIdx := flagInt(flags, "remove", -1)
	atIdx := flagInt(flags, "at", -1)
	callarg := flagString(flags, "callarg", "")
	dryRun := flagBool(flags, "dry_run", false)

	if addParam == "" && removeIdx < 0 {
		return nil, fmt.Errorf("changesig: --add or --remove is required")
	}
	if addParam != "" && removeIdx >= 0 {
		return nil, fmt.Errorf("changesig: --add and --remove are mutually exclusive")
	}
	if addParam != "" && callarg == "" {
		return nil, fmt.Errorf("changesig: --callarg is required with --add")
	}

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("changesig: %w", err)
	}

	defData, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, fmt.Errorf("changesig: read %s: %w", output.Rel(sym.File), err)
	}

	// Find the parameter list in the definition.
	defBody := defData[sym.StartByte:sym.EndByte]
	parenOpen := findParamListOpen(defBody, sym.Name)
	if parenOpen < 0 {
		return nil, fmt.Errorf("changesig: could not find parameter list for %s", sym.Name)
	}
	parenClose := findMatchingClose(defBody, parenOpen)
	if parenClose < 0 {
		return nil, fmt.Errorf("changesig: unbalanced parentheses in %s definition", sym.Name)
	}

	// Parse current params.
	paramText := string(defBody[parenOpen+1 : parenClose])
	params := splitParams(paramText)

	// Build the new parameter list.
	var newParams []string
	if addParam != "" {
		if atIdx < 0 || atIdx >= len(params) {
			newParams = append(params, addParam)
		} else {
			newParams = make([]string, 0, len(params)+1)
			newParams = append(newParams, params[:atIdx]...)
			newParams = append(newParams, addParam)
			newParams = append(newParams, params[atIdx:]...)
		}
	} else {
		// Remove
		if removeIdx < 0 || removeIdx >= len(params) {
			return nil, fmt.Errorf("changesig: --remove %d out of range (function has %d params)", removeIdx, len(params))
		}
		newParams = append(params[:removeIdx], params[removeIdx+1:]...)
	}

	newParamText := strings.Join(newParams, ", ")

	// Build new definition.
	absParenOpen := int(sym.StartByte) + parenOpen
	absParenClose := int(sym.StartByte) + parenClose
	var newDef bytes.Buffer
	newDef.Write(defData[:absParenOpen+1])
	newDef.WriteString(newParamText)
	newDef.Write(defData[absParenClose:])
	newDefData := newDef.Bytes()

	// Find call sites via semantic references.
	isMethod := sym.Receiver != ""
	quotedName := regexp.QuoteMeta(sym.Name)
	var callPattern string
	if isMethod {
		callPattern = `\.` + quotedName + `\s*\(`
	} else {
		callPattern = `\b` + quotedName + `\s*\(`
	}
	callRe, err := regexp.Compile(callPattern)
	if err != nil {
		return nil, fmt.Errorf("changesig: %w", err)
	}

	refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File)
	if err != nil {
		return nil, fmt.Errorf("changesig: finding references: %w", err)
	}

	// Collect file edits. Start with the definition file.
	type fileEdit struct {
		file    string
		oldData []byte
		newData []byte
	}

	edits := []fileEdit{{
		file:    sym.File,
		oldData: defData,
		newData: newDefData,
	}}

	// Group references by file, then transform call sites in each.
	refFileSpans := map[string][]sigSpan{}
	for _, ref := range refs {
		refFileSpans[ref.File] = append(refFileSpans[ref.File], sigSpan{ref.StartByte, ref.EndByte})
	}

	// Also transform call sites within the definition file itself
	// (same-file callers aren't in refs but may exist).
	// The definition file already has the new param list, so we need to
	// find calls in newDefData outside the definition's own param list.

	for file, spans := range refFileSpans {
		if file == sym.File {
			// Already handled in the definition edit — but we need to also
			// transform call sites in the definition file. Handle below.
			continue
		}

		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
		// Deduplicate overlapping spans (e.g. a method and its containing class
		// can both be returned as references).
		spans = deduplicateSpans(spans)
		newData := transformCallSites(data, spans, callRe, sym.Name, addParam != "", callarg, atIdx, removeIdx)
		if !bytes.Equal(data, newData) {
			edits = append(edits, fileEdit{file: file, oldData: data, newData: newData})
		}
	}

	// Transform call sites in the definition file (outside the definition symbol itself).
	// We already replaced the param list; now find calls to the function elsewhere in that file.
	if defSpans, ok := refFileSpans[sym.File]; ok {
		sort.Slice(defSpans, func(i, j int) bool { return defSpans[i].start < defSpans[j].start })
		defSpans = deduplicateSpans(defSpans)
		transformed := transformCallSites(newDefData, defSpans, callRe, sym.Name, addParam != "", callarg, atIdx, removeIdx)
		if !bytes.Equal(newDefData, transformed) {
			edits[0].newData = transformed
		}
	}

	// Sort edits by file for deterministic output.
	sort.Slice(edits, func(i, j int) bool { return edits[i].file < edits[j].file })

	// Build diffs.
	totalFiles := 0
	var combinedDiff string
	for _, fe := range edits {
		if bytes.Equal(fe.oldData, fe.newData) {
			continue
		}
		totalFiles++
		combinedDiff += edit.UnifiedDiff(output.Rel(fe.file), fe.oldData, fe.newData)
	}

	if totalFiles == 0 {
		return map[string]any{
			"file":   output.Rel(sym.File),
			"status": "noop",
		}, nil
	}

	msg := ""
	if addParam != "" {
		msg = fmt.Sprintf("add param %q to %s, update %d files", addParam, sym.Name, totalFiles)
	} else {
		msg = fmt.Sprintf("remove param %d from %s, update %d files", removeIdx, sym.Name, totalFiles)
	}

	if dryRun {
		return map[string]any{
			"file":    output.Rel(sym.File),
			"status":  "dry_run",
			"diff":    combinedDiff,
			"message": msg,
		}, nil
	}

	tx := edit.NewTransaction()
	for _, fe := range edits {
		if bytes.Equal(fe.oldData, fe.newData) {
			continue
		}
		hash := edit.HashBytes(fe.oldData)
		tx.Add(fe.file, 0, uint32(len(fe.oldData)), string(fe.newData), hash)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("changesig: %w", err)
	}

	for _, fe := range edits {
		idx.MarkDirty(db.EdrDir(), output.Rel(fe.file))
	}

	newHash, _ := edit.FileHash(sym.File)
	return map[string]any{
		"file":    output.Rel(sym.File),
		"status":  "applied",
		"diff":    combinedDiff,
		"hash":    newHash,
		"message": msg,
	}, nil
}

// findParamListOpen finds the opening paren of the function's parameter list
// by searching for the function name followed by '(' in the definition body.
func findParamListOpen(body []byte, funcName string) int {
	nameBytes := []byte(funcName)
	idx := bytes.Index(body, nameBytes)
	if idx < 0 {
		return -1
	}
	// Scan forward from after the name to find '('
	for i := idx + len(nameBytes); i < len(body); i++ {
		switch body[i] {
		case '(':
			return i
		case ' ', '\t', '\n', '\r':
			continue
		case '[': // generic type params — skip to ']'
			depth := 1
			for i++; i < len(body) && depth > 0; i++ {
				if body[i] == '[' {
					depth++
				} else if body[i] == ']' {
					depth--
				}
			}
			continue
		default:
			return -1
		}
	}
	return -1
}

// findMatchingClose finds the closing paren matching the open paren at pos.
func findMatchingClose(body []byte, openPos int) int {
	depth := 1
	for i := openPos + 1; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		case '"':
			// Skip string literal
			for i++; i < len(body); i++ {
				if body[i] == '\\' {
					i++
				} else if body[i] == '"' {
					break
				}
			}
		case '\'':
			// Skip char/string literal
			for i++; i < len(body); i++ {
				if body[i] == '\\' {
					i++
				} else if body[i] == '\'' {
					break
				}
			}
		}
	}
	return -1
}

// splitParams splits a parameter list string by top-level commas.
func splitParams(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var params []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				params = append(params, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	last := strings.TrimSpace(s[start:])
	if last != "" {
		params = append(params, last)
	}
	// Filter out empty entries and comment-only entries.
	filtered := params[:0]
	for _, p := range params {
		clean := strings.TrimSpace(p)
		if clean != "" && !strings.HasPrefix(clean, "//") && !strings.HasPrefix(clean, "#") {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// transformCallSites finds all call sites of funcName within the given spans
// and adds/removes the argument.
type sigSpan struct{ start, end uint32 }

func transformCallSites(data []byte, spans []sigSpan, callRe *regexp.Regexp, funcName string, isAdd bool, callarg string, atIdx, removeIdx int) []byte {
	// Find all call site locations: positions of the '(' after funcName
	type callSite struct {
		openParen int // byte offset of '(' in data
	}
	var sites []callSite

	for _, sp := range spans {
		start := int(sp.start)
		end := int(sp.end)
		if start > len(data) || end > len(data) {
			continue
		}
		region := data[start:end]
		matches := callRe.FindAllIndex(region, -1)
		for _, m := range matches {
			// m[1]-1 is the '(' position within the region
			absPos := start + m[1] - 1
			if absPos < len(data) && data[absPos] == '(' {
				sites = append(sites, callSite{openParen: absPos})
			}
		}
	}

	if len(sites) == 0 {
		return data
	}

	// Sort in reverse order so byte offsets remain valid as we splice.
	sort.Slice(sites, func(i, j int) bool { return sites[i].openParen > sites[j].openParen })

	result := make([]byte, len(data))
	copy(result, data)

	for _, site := range sites {
		closeParen := findMatchingClose(result, site.openParen)
		if closeParen < 0 {
			continue
		}

		argText := string(result[site.openParen+1 : closeParen])
		args := splitParams(argText)

		var newArgs []string
		if isAdd {
			if atIdx < 0 || atIdx >= len(args) {
				newArgs = append(args, callarg)
			} else {
				newArgs = make([]string, 0, len(args)+1)
				newArgs = append(newArgs, args[:atIdx]...)
				newArgs = append(newArgs, callarg)
				newArgs = append(newArgs, args[atIdx:]...)
			}
		} else {
			if removeIdx >= 0 && removeIdx < len(args) {
				newArgs = append(args[:removeIdx], args[removeIdx+1:]...)
			} else {
				continue
			}
		}

		newArgText := strings.Join(newArgs, ", ")
		var buf bytes.Buffer
		buf.Write(result[:site.openParen+1])
		buf.WriteString(newArgText)
		buf.Write(result[closeParen:])
		result = buf.Bytes()
	}

	return result
}

// deduplicateSpans removes spans that are fully contained within another span.
// Input must be sorted by start offset.
func deduplicateSpans(spans []sigSpan) []sigSpan {
	if len(spans) <= 1 {
		return spans
	}
	out := []sigSpan{spans[0]}
	for _, s := range spans[1:] {
		prev := &out[len(out)-1]
		if s.start >= prev.start && s.end <= prev.end {
			// s is fully inside prev — skip it
			continue
		}
		if s.start < prev.end {
			// Overlapping — merge by extending prev
			if s.end > prev.end {
				prev.end = s.end
			}
			continue
		}
		out = append(out, s)
	}
	return out
}
