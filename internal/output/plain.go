package output

// plain.go renders the envelope as a compact JSON header line followed by
// raw text body. This is the default and only public transport format.
//
// Format: first line is a short JSON object with metadata, everything
// after the first newline is raw content (code, diff, grep-style matches).
// Agents parse metadata with json.Unmarshal(line1), read body as-is.
//
//   Read:    {"file":"f.go","sym":"Foo","lines":[10,20],"hash":"abc"}\n<raw code>
//   Search:  {"n":4}\n<grep-style lines>
//   Edit:    {"file":"f.go","status":"applied","hash":"abc"}\n<diff>
//   Error:   {"error":"...","ec":"not_found"}\n
//   Batch:   ops separated by "---\n"
//   Verify:  {"verify":"passed"}\n  (or {"verify":"skipped","reason":"..."})

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// toStringSlice extracts a []string from an any that might be []string or []any.
func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	if ss, ok := v.([]string); ok {
		return ss
	}
	if aa, ok := v.([]any); ok {
		out := make([]string, 0, len(aa))
		for _, a := range aa {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// printPlain renders the envelope as JSON-header + raw-body to stdout.
func printPlain(e *Envelope) {
	w := os.Stdout

	// Envelope-level errors (non-warning errors block ops)
	hasBlockingErrors := false
	for _, err := range e.Errors {
		if err.Code != "warning" {
			writeHeader(w, map[string]any{"error": err.Message, "ec": err.Code})
			hasBlockingErrors = true
		}
	}
	if hasBlockingErrors {
		return
	}

	multi := len(e.Ops) > 1

	for i, op := range e.Ops {
		if multi && i > 0 {
			fmt.Fprintln(w, "---")
		}

		// Error ops
		if errMsg, ok := op["error"].(string); ok {
			h := map[string]any{"error": errMsg}
			if code, ok := op["error_code"].(string); ok {
				h["ec"] = code
			}
			writeHeader(w, h)
			continue
		}

		opType, _ := op["type"].(string)
		switch opType {
		case "focus", "read", "prepare":
			plainRead(w, op)
		case "search":
			plainSearch(w, op)
		case "edit", "write":
			plainEdit(w, op)
		case "rename":
			plainRename(w, op)
		case "orient", "map":
			plainMap(w, op)
		case "reset":
			plainReset(w, op)
		case "status":
			plainNext(w, op)
		case "undo":
			plainUndo(w, op)
		case "index":
			plainIndex(w, op)
		case "files":
			plainFiles(w, op)
		default:
			writeHeader(w, op)
		}
	}

	// Verify
	if m, ok := e.Verify.(map[string]any); ok {
		status, _ := m["status"].(string)
		if status != "" {
			h := map[string]any{"verify": status}
			if cmd, ok := m["command"].(string); ok && cmd != "" {
				h["command"] = cmd
			}
			if reason, ok := m["reason"].(string); ok {
				h["reason"] = reason
			}
			if errMsg, ok := m["error"].(string); ok {
				h["error"] = errMsg
			}
			// Include output in the header (not as a body) to preserve header-only contract
			if status == "failed" {
				if out, ok := m["output"].(string); ok && out != "" {
					h["output"] = out
				}
				if ec, ok := m["error_context"]; ok {
					h["error_context"] = ec
				}
			}
			writeHeader(w, h)
		}
	}
}

// writeHeader writes a compact JSON line.
func writeHeader(w *os.File, m map[string]any) {
	// Emit keys in a stable, scannable order.
	// Priority fields first so agents and humans can quickly parse status.
	priority := []string{"session", "file", "sym", "hash", "lines", "status"}
	var buf []byte
	buf = append(buf, '{')
	first := true
	wrote := map[string]bool{}
	for _, k := range priority {
		v, ok := m[k]
		if !ok {
			continue
		}
		if !first {
			buf = append(buf, ',')
		}
		kj, _ := json.Marshal(k)
		vj, _ := json.Marshal(v)
		buf = append(buf, kj...)
		buf = append(buf, ':')
		buf = append(buf, vj...)
		first = false
		wrote[k] = true
	}
	// Remaining keys in natural order
	for k, v := range m {
		if wrote[k] {
			continue
		}
		if !first {
			buf = append(buf, ',')
		}
		kj, _ := json.Marshal(k)
		vj, _ := json.Marshal(v)
		buf = append(buf, kj...)
		buf = append(buf, ':')
		buf = append(buf, vj...)
		first = false
	}
	buf = append(buf, '}', '\n')
	w.Write(buf)
}

// writeBody writes raw text, ensuring it ends with a newline.
func writeBody(w *os.File, body string) {
	if body == "" {
		return
	}
	fmt.Fprint(w, body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Fprintln(w)
	}
}

func plainRead(w *os.File, op Op) {
	h := map[string]any{}
	hStr(h, "file", op, "file")
	hStr(h, "sym", op, "symbol")
	if v, ok := op["lines"]; ok {
		h["lines"] = v
	}
	hStr(h, "hash", op, "hash")
	hTrunc(h, op)
	hSession(h, op)
	hStr(h, "hint", op, "hint")
	hStr(h, "auto", op, "auto")
	writeHeader(w, h)

	content, _ := op["content"].(string)
	writeBody(w, content)

	// Render symbol list (from --symbols flag)
	if symList, ok := toSliceOfMaps(op["symbols"]); ok && len(symList) > 0 {
		fmt.Fprintln(w, "\n--- symbols ---")
		for _, s := range symList {
			name, _ := s["name"].(string)
			typ, _ := s["type"].(string)
			lines := s["lines"]
			fmt.Fprintf(w, "%s %s %v\n", typ, name, lines)
		}
	}

	// Render expanded deps/callers/tests as compact lists
	for _, key := range []string{"deps", "callers"} {
		items, ok := op[key]
		if !ok {
			continue
		}
		// items is a slice (JSON-serialized from []relatedSym)
		slice, ok := toSliceOfMaps(items)
		if !ok || len(slice) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n--- %s ---\n", key)
		for _, item := range slice {
			file, _ := item["file"].(string)
			sig, _ := item["signature"].(string)
			if sig != "" {
				fmt.Fprintf(w, "%s  %s\n", file, sig)
			}
		}
	}

	// Render test locations
	if tests, ok := op["tests"]; ok {
		slice, ok := toSliceOfMaps(tests)
		if ok && len(slice) > 0 {
			fmt.Fprintf(w, "\n--- tests ---\n")
			for _, t := range slice {
				file, _ := t["file"].(string)
				name, _ := t["name"].(string)
				line := anyInt(t["line"])
				fmt.Fprintf(w, "%s:%d  %s\n", file, line, name)
			}
		}
	}
}

func plainSearch(w *os.File, op Op) {
	h := map[string]any{}
	hSession(h, op)
	h["n"] = anyInt(op["total_matches"])
	hTrunc(h, op)
	hStr(h, "hint", op, "hint")
	writeHeader(w, h)

	// Flat matches
	if matches, ok := op["matches"].([]any); ok {
		for _, m := range matches {
			writeMatch(w, "", m)
		}
	}

	// File-grouped matches
	if files, ok := op["files"].([]any); ok {
		for _, f := range files {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			file, _ := fm["file"].(string)
			if matches, ok := fm["matches"].([]any); ok {
				for _, m := range matches {
					writeMatch(w, file, m)
				}
			}
		}
	}
}

func writeMatch(w *os.File, file string, m any) {
	mm, ok := m.(map[string]any)
	if !ok {
		return
	}

	// Symbol match (may appear with or without outer file grouping)
	if sym, ok := mm["symbol"].(map[string]any); ok {
		f, _ := sym["file"].(string)
		if f == "" {
			f = file
		}
		name, _ := sym["name"].(string)
		line := anyInt(sym["line"])
		body, _ := mm["body"].(string)
		fmt.Fprintf(w, "%s:%d: %s\n", f, line, name)
		writeBody(w, body)
		return
	}

	// Text search match
	line := anyInt(mm["line"])
	text, _ := mm["text"].(string)

	if file != "" {
		if snippet, ok := mm["snippet"].(string); ok && snippet != "" {
			fmt.Fprintf(w, "%s:%d:\n", file, line)
			writeBody(w, snippet)
		} else {
			fmt.Fprintf(w, "%s:%d: %s\n", file, line, text)
		}
	} else {
		fmt.Fprintf(w, "%d: %s\n", line, text)
	}
}

func plainEdit(w *os.File, op Op) {
	h := map[string]any{}
	hStr(h, "file", op, "file")
	hStr(h, "status", op, "status")
	hStr(h, "hash", op, "hash")
	hStr(h, "msg", op, "message")

	// Include read_back metadata in the header if present
	if rb, ok := op["read_back"].(map[string]any); ok {
		rbH := map[string]any{}
		if v, ok := rb["lines"]; ok {
			rbH["lines"] = v
		}
		if v, ok := rb["symbol"]; ok {
			rbH["symbol"] = v
		}
		h["read_back"] = rbH
	}

	writeHeader(w, h)

	diff, _ := op["diff"].(string)
	writeBody(w, diff)

	// Append read-back content after the diff
	if rb, ok := op["read_back"].(map[string]any); ok {
		if content, ok := rb["content"].(string); ok && content != "" {
			writeBody(w, content)
		}
	}
}

func plainRename(w *os.File, op Op) {
	h := map[string]any{}
	hStr(h, "status", op, "status")
	hStr(h, "from", op, "old_name")
	hStr(h, "to", op, "new_name")
	if v := anyInt(op["occurrences"]); v > 0 {
		h["n"] = v
	}
	if t, ok := op["truncated"].(bool); ok && t {
		h["truncated"] = true
		h["hint"] = "use --include to narrow scope, or --budget N for more"
	}
	writeHeader(w, h)

	if preview, ok := op["preview"].([]any); ok {
		for _, p := range preview {
			if pm, ok := p.(map[string]any); ok {
				f, _ := pm["file"].(string)
				line := anyInt(pm["line"])
				text, _ := pm["text"].(string)
				fmt.Fprintf(w, "%s:%d: %s\n", f, line, text)
			}
		}
	}
	if diffs, ok := op["diffs"].([]any); ok {
		for _, d := range diffs {
			if dm, ok := d.(map[string]any); ok {
				diff, _ := dm["diff"].(string)
				if diff != "" {
					writeBody(w, diff)
				}
			}
		}
	}
	// Always show files_changed as a summary, but cap when truncated.
	if changes, ok := op["files_changed"].([]any); ok && len(changes) > 0 {
		maxFiles := len(changes)
		if _, trunc := op["truncated"].(bool); trunc && maxFiles > 20 {
			maxFiles = 20
		}
		for _, c := range changes[:maxFiles] {
			if f, ok := c.(string); ok {
				fmt.Fprintf(w, "  %s\n", f)
			}
		}
		if maxFiles < len(changes) {
			fmt.Fprintf(w, "  ... and %d more files\n", len(changes)-maxFiles)
		}
	}
}

func plainMap(w *os.File, op Op) {
	h := map[string]any{}
	hSession(h, op)
	if v := anyInt(op["files"]); v > 0 {
		h["files"] = v
	}
	if v := anyInt(op["symbols"]); v > 0 {
		h["symbols"] = v
	}
	hTrunc(h, op)
	hStr(h, "hint", op, "hint")
	writeHeader(w, h)

	// Structured map: list of file entries
	if content, ok := op["content"].([]any); ok {
		for _, entry := range content {
			fe, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			file, _ := fe["file"].(string)
			symbols, _ := fe["symbols"].([]any)
			for _, s := range symbols {
				sm, ok := s.(map[string]any)
				if !ok {
					continue
				}
				name, _ := sm["name"].(string)
				kind, _ := sm["kind"].(string)
				line := anyInt(sm["line"])
				endLine := anyInt(sm["end_line"])
				matches := anyInt(sm["matches"])
				if endLine > 0 && matches > 0 {
					fmt.Fprintf(w, "%s:%d-%d: %s %s (%d matches)\n", file, line, endLine, kind, name, matches)
				} else if endLine > 0 {
					fmt.Fprintf(w, "%s:%d-%d: %s %s\n", file, line, endLine, kind, name)
				} else {
					fmt.Fprintf(w, "%s:%d: %s %s\n", file, line, kind, name)
				}
			}
		}
		return
	}

	// Directory summary (severely truncated repos)
	if dirs, ok := op["dirs"].([]any); ok {
		for _, d := range dirs {
			dm, ok := d.(map[string]any)
			if !ok {
				continue
			}
			dir, _ := dm["dir"].(string)
			files := anyInt(dm["files"])
			symbols := anyInt(dm["symbols"])
			fmt.Fprintf(w, "%-30s %4d files  %5d symbols\n", dir+"/", files, symbols)
		}
		return
	}

	// String content (file-scoped map)
	if s, ok := op["content"].(string); ok && s != "" {
		writeBody(w, s)
	}
}

func plainRefs(w *os.File, op Op) {
	if v, ok := op["session"].(string); ok && v == "unchanged" {
		uh := map[string]any{"session": "unchanged"}
		if sym, ok := op["symbol"].(map[string]any); ok {
			f, _ := sym["file"].(string)
			name, _ := sym["name"].(string)
			uh["sym"] = f + ":" + name
		}
		if sn, ok := op["symbol"].(string); ok && sn != "" {
			uh["sym"] = sn
		}
		if n := anyInt(op["total_refs"]); n > 0 {
			uh["n"] = n
		} else if n := anyInt(op["n"]); n > 0 {
			uh["n"] = n
		} else if n := anyInt(op["total"]); n > 0 {
			uh["n"] = n
		}
		writeHeader(w, uh)
		return
	}
	h := map[string]any{}
	if v, ok := op["session"].(string); ok && v == "new" {
		h["session"] = "new"
	}
	if sym, ok := op["symbol"].(map[string]any); ok {
		f, _ := sym["file"].(string)
		name, _ := sym["name"].(string)
		h["sym"] = f + ":" + name
	}
	if sn, ok := op["symbol"].(string); ok && sn != "" {
		h["sym"] = sn
	}
	// Impact results
	if total := anyInt(op["total"]); total > 0 {
		h["n"] = total
		writeHeader(w, h)
		if impacted, ok := op["impacted"].([]any); ok {
			for _, item := range impacted {
				if im, ok := item.(map[string]any); ok {
					name, _ := im["name"].(string)
					file, _ := im["file"].(string)
					depth := anyInt(im["depth"])
					fmt.Fprintf(w, "%s:%s (depth %d)\n", file, name, depth)
				}
			}
		}
		return
	}

	// Expand results (--callers / --deps)
	callers, hasCallers := op["callers"].([]any)
	deps, hasDeps := op["deps"].([]any)
	if hasCallers || hasDeps {
		n := len(callers) + len(deps)
		h["n"] = n
		writeHeader(w, h)
		for _, s := range callers {
			writeSymbolLine(w, s, "caller")
		}
		for _, s := range deps {
			writeSymbolLine(w, s, "dep")
		}
		return
	}

	// Chain results (--chain)
	if chain, ok := op["chain"].([]any); ok {
		h["n"] = len(chain)
		if found, ok := op["found"].(bool); ok && !found {
			h["n"] = 0
			if msg, ok := op["message"].(string); ok {
				h["message"] = msg
			}
		}
		writeHeader(w, h)
		for _, name := range chain {
			if s, ok := name.(string); ok {
				fmt.Fprintf(w, "%s\n", s)
			}
		}
		return
	}

	refs, _ := op["references"].([]any)
	h["n"] = len(refs)
	writeHeader(w, h)

	for _, r := range refs {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		file, _ := rm["file"].(string)
		line := 0
		if lines, ok := rm["lines"].([]any); ok && len(lines) > 0 {
			line = anyInt(lines[0])
		}
		name, _ := rm["name"].(string)
		fmt.Fprintf(w, "%s:%d: %s\n", file, line, name)
	}
}

func writeSymbolLine(w *os.File, s any, label string) {
	sm, ok := s.(map[string]any)
	if !ok {
		return
	}
	file, _ := sm["file"].(string)
	name, _ := sm["name"].(string)
	line := 0
	if lines, ok := sm["lines"].([]any); ok && len(lines) > 0 {
		line = anyInt(lines[0])
	}
	fmt.Fprintf(w, "%s:%d: %s\n", file, line, name)
}

func plainReset(w *os.File, op Op) {
	h := map[string]any{}
	if v, ok := op["status"].(string); ok {
		h["status"] = v
	}
	if v, ok := op["files_changed"]; ok {
		h["files"] = v
	}
	if v, ok := op["symbols_changed"]; ok {
		h["symbols"] = v
	}
	if v, ok := op["scope"].(string); ok {
		h["scope"] = v
	}
	if v, ok := op["session"].(string); ok {
		h["session"] = v
	}
	if v, ok := op["mode"].(string); ok {
		h["mode"] = v
	}
	if v, ok := op["total_files"]; ok {
		h["files"] = v
	}
	writeHeader(w, h)
}

func plainNext(w *os.File, op Op) {
	h := map[string]any{}
	hStr(h, "focus", op, "focus")

	// Add build status to header
	if build, ok := op["build"].(map[string]any); ok {
		if status, ok := build["status"].(string); ok {
			h["build"] = status
		}
	}

	// Add fix count to header
	if fix, ok := op["fix"].([]any); ok && len(fix) > 0 {
		h["fix"] = len(fix)
	}

	// Add undo availability
	if v, ok := op["undo_available"].(bool); ok && v {
		h["undo_available"] = true
	}

	// Add external changes count to header
	if ext, ok := op["external_changes"].([]any); ok && len(ext) > 0 {
		h["external_changes"] = len(ext)
	}

	writeHeader(w, h)

	// Focus line
	if focus, ok := op["focus"].(string); ok && focus != "" {
		fmt.Fprintf(w, "focus: %s\n\n", focus)
	}

	// Build state
	if build, ok := op["build"].(map[string]any); ok {
		status, _ := build["status"].(string)
		editsSince, _ := build["edits_since"].(bool)
		if editsSince {
			fmt.Fprintf(w, "build: %s (edits since last verify)\n", status)
		} else {
			fmt.Fprintf(w, "build: %s\n", status)
		}
	}

	// Fix items
	if fix, ok := op["fix"].([]any); ok && len(fix) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "fix:")
		for _, f := range fix {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			file, _ := fm["file"].(string)
			symbol, _ := fm["symbol"].(string)
			reason, _ := fm["reason"].(string)
			suggest, _ := fm["suggest"].(string)

			detail := fmt.Sprintf("  %s:%s", file, symbol)
			if reason != "" {
				detail += " — " + reason
			}
			fmt.Fprintln(w, detail)
			if suggest != "" {
				fmt.Fprintf(w, "    => %s\n", suggest)
			}
		}
	}

	// External changes
	if ext, ok := op["external_changes"].([]any); ok && len(ext) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "external_changes:")
		for _, e := range ext {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			file, _ := em["file"].(string)
			kind, _ := em["kind"].(string)
			since, _ := em["since"].(string)
			line := fmt.Sprintf("  %s %s", file, kind)
			if since != "" {
				line += fmt.Sprintf(" (since %s)", since)
			}
			fmt.Fprintln(w, line)
		}
	}
}

func plainUndo(w *os.File, op Op) {
	h := map[string]any{"status": "undone"}
	hStr(h, "target", op, "target")
	hStr(h, "safety", op, "safety_checkpoint")
	if restored := toStringSlice(op["restored"]); len(restored) > 0 {
		h["files"] = len(restored)
	}
	writeHeader(w, h)
	if restored := toStringSlice(op["restored"]); len(restored) > 0 {
		for _, f := range restored {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
	if removed := toStringSlice(op["new_files_removed"]); len(removed) > 0 {
		fmt.Fprintln(w, "\nnew files removed:")
		for _, f := range removed {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}
}

func plainFiles(w *os.File, op Op) {
	h := map[string]any{}
	if v := anyInt(op["n"]); v >= 0 {
		h["n"] = v
	}
	if v, ok := op["source"].(string); ok {
		h["source"] = v
	}
	hTrunc(h, op)
	writeHeader(w, h)
	// Body: one file per line
	if files := toStringSlice(op["files"]); len(files) > 0 {
		for _, f := range files {
			fmt.Fprintln(w, f)
		}
	}
}

func plainIndex(w *os.File, op Op) {
	h := map[string]any{}
	hStr(h, "status", op, "status")
	if v, ok := op["mode"].(string); ok {
		h["mode"] = v
	}
	if v := anyInt(op["files_indexed"]); v > 0 {
		h["files"] = v
	}
	if v := anyInt(op["files_total"]); v > 0 {
		h["total"] = v
	}
	if v := anyInt(op["trigrams"]); v > 0 {
		h["trigrams"] = v
	}
	if v := anyInt(op["size_bytes"]); v > 0 {
		h["bytes"] = v
	}
	if v, ok := op["coverage"].(string); ok {
		h["coverage"] = v
	}
	if v, ok := op["stale"].(bool); ok && v {
		h["stale"] = true
	}
	writeHeader(w, h)
}

// hStr copies a string field from op to header, optionally renaming.
func hStr(h map[string]any, hKey string, op Op, opKey string) {
	if v, ok := op[opKey].(string); ok && v != "" {
		h[hKey] = v
	}
}

// hSession copies the session field if it is "unchanged" or "new".
func hSession(h map[string]any, op Op) {
	if v, ok := op["session"].(string); ok && v == "unchanged" {
		h["session"] = v
	}
	// "new" is the default — omit to reduce noise.
}

// hTrunc copies truncation info (trunc + budget_used).
func hTrunc(h map[string]any, op Op) {
	if v, ok := op["truncated"].(bool); ok && v {
		h["trunc"] = true
		if bu := anyInt(op["budget_used"]); bu > 0 {
			h["budget_used"] = bu
		}
	}
}

// toSliceOfMaps converts a JSON-deserialized slice of objects to []map[string]any.
func toSliceOfMaps(v any) ([]map[string]any, bool) {
	switch s := v.(type) {
	case []map[string]any:
		return s, true
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, item := range s {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

// anyInt extracts an int from any (handles float64 from JSON and int from Go).
func anyInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
