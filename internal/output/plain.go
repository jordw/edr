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
		case "read":
			plainRead(w, op)
		case "search":
			plainSearch(w, op)
		case "edit", "write":
			plainEdit(w, op)
		case "rename":
			plainRename(w, op)
		case "map":
			plainMap(w, op)
		case "refs":
			plainRefs(w, op)
		case "reindex":
			plainReindex(w, op)
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
			}
			writeHeader(w, h)
		}
	}
}

// writeHeader writes a compact JSON line.
func writeHeader(w *os.File, m map[string]any) {
	data, _ := json.Marshal(m)
	w.Write(data)
	w.Write([]byte{'\n'})
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
	if v, ok := op["file"].(string); ok {
		h["file"] = v
	}
	if v, ok := op["symbol"].(string); ok {
		h["sym"] = v
	}
	if v, ok := op["lines"]; ok {
		h["lines"] = v
	}
	if v, ok := op["hash"].(string); ok && v != "" {
		h["hash"] = v
	}
	if v, ok := op["truncated"].(bool); ok && v {
		h["trunc"] = true
		if bu := anyInt(op["budget_used"]); bu > 0 {
			h["budget_used"] = bu
		}
	}
	if v, ok := op["session"].(string); ok && (v == "unchanged" || v == "new") {
		h["session"] = v
	}
	if v, ok := op["hint"].(string); ok && v != "" {
		h["hint"] = v
	}
	writeHeader(w, h)

	content, _ := op["content"].(string)
	writeBody(w, content)
}

func plainSearch(w *os.File, op Op) {
	h := map[string]any{}
	if v, ok := op["session"].(string); ok && (v == "unchanged" || v == "new") {
		h["session"] = v
	}
	h["n"] = anyInt(op["total_matches"])
	if v, ok := op["truncated"].(bool); ok && v {
		h["trunc"] = true
		if bu := anyInt(op["budget_used"]); bu > 0 {
			h["budget_used"] = bu
		}
	}
	if v, ok := op["hint"].(string); ok && v != "" {
		h["hint"] = v
	}
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
	line := anyInt(mm["line"])
	text, _ := mm["text"].(string)

	if file != "" {
		// Text search match — show snippet if context was requested
		if snippet, ok := mm["snippet"].(string); ok && snippet != "" {
			fmt.Fprintf(w, "%s:%d:\n", file, line)
			writeBody(w, snippet)
		} else {
			fmt.Fprintf(w, "%s:%d: %s\n", file, line, text)
		}
	} else {
		// Symbol search
		if sym, ok := mm["symbol"].(map[string]any); ok {
			f, _ := sym["file"].(string)
			name, _ := sym["name"].(string)
			body, _ := mm["body"].(string)
			fmt.Fprintf(w, "%s:%s\n", f, name)
			writeBody(w, body)
		}
	}
}

func plainEdit(w *os.File, op Op) {
	h := map[string]any{}
	if v, ok := op["file"].(string); ok {
		h["file"] = v
	}
	if v, ok := op["status"].(string); ok {
		h["status"] = v
	}
	if v, ok := op["hash"].(string); ok {
		h["hash"] = v
	}
	if v, ok := op["message"].(string); ok {
		h["msg"] = v
	}
	writeHeader(w, h)

	diff, _ := op["diff"].(string)
	writeBody(w, diff)
}

func plainRename(w *os.File, op Op) {
	h := map[string]any{}
	if v, ok := op["status"].(string); ok && v != "" {
		h["status"] = v
	}
	if v, ok := op["old_name"].(string); ok {
		h["from"] = v
	}
	if v, ok := op["new_name"].(string); ok {
		h["to"] = v
	}
	if v, ok := op["occurrences"].(int); ok && v > 0 {
		h["n"] = v
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
	} else if changes, ok := op["files_changed"].([]any); ok && len(op) > 0 {
		for _, c := range changes {
			if f, ok := c.(string); ok {
				fmt.Fprintf(w, "  %s\n", f)
			}
		}
	}
}

func plainMap(w *os.File, op Op) {
	h := map[string]any{}
	if v, ok := op["session"].(string); ok && (v == "unchanged" || v == "new") {
		h["session"] = v
	}
	if v := anyInt(op["files"]); v > 0 {
		h["files"] = v
	}
	if v := anyInt(op["symbols"]); v > 0 {
		h["symbols"] = v
	}
	if v, ok := op["truncated"].(bool); ok && v {
		h["trunc"] = true
		if bu := anyInt(op["budget_used"]); bu > 0 {
			h["budget_used"] = bu
		}
	}
	if v, ok := op["hint"].(string); ok && v != "" {
		h["hint"] = v
	}
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
				if endLine > 0 {
					fmt.Fprintf(w, "%s:%d-%d: %s %s\n", file, line, endLine, kind, name)
				} else {
					fmt.Fprintf(w, "%s:%d: %s %s\n", file, line, kind, name)
				}
			}
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
		writeHeader(w, map[string]any{"session": "unchanged"})
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

func plainReindex(w *os.File, op Op) {
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
	writeHeader(w, h)
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
