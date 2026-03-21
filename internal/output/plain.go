package output

// plain.go renders the envelope as a compact JSON header line followed by
// raw text body. Activated by EDR_FORMAT=plain.
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

	// Envelope-level errors
	if len(e.Errors) > 0 {
		for _, err := range e.Errors {
			writeHeader(w, map[string]any{"error": err.Message, "ec": err.Code})
		}
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
			if reason, ok := m["reason"].(string); ok {
				h["reason"] = reason
			}
			if errMsg, ok := m["error"].(string); ok {
				h["error"] = errMsg
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
	if v, ok := op["truncated"].(bool); ok && v {
		h["trunc"] = true
	}
	if v, ok := op["session"].(string); ok && v == "unchanged" {
		h["session"] = "unchanged"
	}
	writeHeader(w, h)

	content, _ := op["content"].(string)
	writeBody(w, content)
}

func plainSearch(w *os.File, op Op) {
	h := map[string]any{}
	if v := anyInt(op["total_matches"]); v > 0 {
		h["n"] = v
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
		fmt.Fprintf(w, "%s:%d: %s\n", file, line, text)
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
	if v, ok := op["status"].(string); ok {
		h["status"] = v
	}
	if v, ok := op["from"].(string); ok {
		h["from"] = v
	}
	if v, ok := op["to"].(string); ok {
		h["to"] = v
	}
	writeHeader(w, h)

	if changes, ok := op["changes"].([]any); ok {
		for _, c := range changes {
			if cm, ok := c.(map[string]any); ok {
				f, _ := cm["file"].(string)
				fmt.Fprintf(w, "  %s\n", f)
			}
		}
	}
}

func plainMap(w *os.File, op Op) {
	writeHeader(w, map[string]any{})

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
	h := map[string]any{}
	if sym, ok := op["symbol"].(map[string]any); ok {
		f, _ := sym["file"].(string)
		name, _ := sym["name"].(string)
		h["sym"] = f + ":" + name
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
