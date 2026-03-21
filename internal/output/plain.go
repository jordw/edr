package output

// plain.go renders the envelope as plain text instead of JSON.
// Activated by EDR_FORMAT=plain. Zero overhead — content is emitted raw
// with real newlines, no escaping. Metadata goes in key-value headers.
//
// Format:
//   Success read:    headers + blank line + raw content
//   Success search:  "N matches" + grep-style lines
//   Success edit:    "applied file hash" + diff
//   Error:           "ERR code: message"
//   Batch:           ops separated by "---"
//   Verify:          "verify passed|failed|skipped" after edits

import (
	"fmt"
	"os"
	"strings"
)

// printPlain renders the envelope as plain text to stdout.
func printPlain(e *Envelope) {
	w := os.Stdout

	// Envelope-level errors
	if len(e.Errors) > 0 {
		for _, err := range e.Errors {
			fmt.Fprintf(w, "ERR %s: %s\n", err.Code, err.Message)
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
			code, _ := op["error_code"].(string)
			if code == "" {
				code = "error"
			}
			fmt.Fprintf(w, "ERR %s: %s\n", code, errMsg)
			continue
		}

		opType, _ := op["type"].(string)
		switch opType {
		case "read":
			printPlainRead(w, op)
		case "search":
			printPlainSearch(w, op)
		case "edit":
			printPlainEdit(w, op)
		case "write":
			printPlainWrite(w, op)
		case "rename":
			printPlainRename(w, op)
		case "map":
			printPlainMap(w, op)
		case "reindex":
			printPlainReindex(w, op)
		default:
			// Fallback: print key-value pairs
			for k, v := range op {
				fmt.Fprintf(w, "%s %v\n", k, v)
			}
		}
	}

	// Verify
	if m, ok := e.Verify.(map[string]any); ok {
		status, _ := m["status"].(string)
		if status != "" {
			fmt.Fprintf(w, "verify %s", status)
			if reason, ok := m["reason"].(string); ok {
				fmt.Fprintf(w, ": %s", reason)
			}
			if errMsg, ok := m["error"].(string); ok {
				fmt.Fprintf(w, ": %s", errMsg)
			}
			fmt.Fprintln(w)
		}
	}
}

func printPlainRead(w *os.File, op Op) {
	file, _ := op["file"].(string)
	sym, _ := op["symbol"].(string)
	hash, _ := op["hash"].(string)
	content, _ := op["content"].(string)

	// Headers
	if file != "" {
		fmt.Fprintf(w, "file %s\n", file)
	}
	if sym != "" {
		fmt.Fprintf(w, "sym %s\n", sym)
	}
	if lines, ok := op["lines"].([]any); ok && len(lines) == 2 {
		fmt.Fprintf(w, "lines %v-%v\n", lines[0], lines[1])
	}
	if hash != "" {
		fmt.Fprintf(w, "hash %s\n", hash)
	}
	if trunc, ok := op["truncated"].(bool); ok && trunc {
		fmt.Fprintln(w, "truncated")
	}
	if session, ok := op["session"].(string); ok && session == "unchanged" {
		fmt.Fprintln(w, "unchanged")
	}

	// Body
	if content != "" {
		fmt.Fprintln(w)
		fmt.Fprint(w, content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Fprintln(w)
		}
	}
}

func printPlainSearch(w *os.File, op Op) {
	total, _ := op["total_matches"].(int)
	if totalF, ok := op["total_matches"].(float64); ok {
		total = int(totalF)
	}
	fmt.Fprintf(w, "%d matches\n", total)

	if hint, ok := op["hint"].(string); ok && hint != "" {
		fmt.Fprintf(w, "hint: %s\n", hint)
	}

	// Flat matches
	if matches, ok := op["matches"].([]any); ok {
		for _, m := range matches {
			printPlainMatch(w, "", m)
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
					printPlainMatch(w, file, m)
				}
			}
		}
	}
}

func printPlainMatch(w *os.File, file string, m any) {
	mm, ok := m.(map[string]any)
	if !ok {
		return
	}
	line := 0
	if v, ok := mm["line"].(float64); ok {
		line = int(v)
	} else if v, ok := mm["line"].(int); ok {
		line = v
	}
	text, _ := mm["text"].(string)

	if file != "" {
		fmt.Fprintf(w, "%s:%d: %s\n", file, line, text)
	} else {
		// Symbol search — different shape
		if sym, ok := mm["symbol"].(map[string]any); ok {
			f, _ := sym["file"].(string)
			name, _ := sym["name"].(string)
			body, _ := mm["body"].(string)
			fmt.Fprintf(w, "%s:%s\n", f, name)
			if body != "" {
				fmt.Fprintln(w, body)
			}
		}
	}
}

func printPlainEdit(w *os.File, op Op) {
	file, _ := op["file"].(string)
	status, _ := op["status"].(string)
	hash, _ := op["hash"].(string)
	diff, _ := op["diff"].(string)
	msg, _ := op["message"].(string)

	fmt.Fprintf(w, "%s %s", status, file)
	if hash != "" {
		fmt.Fprintf(w, " %s", hash)
	}
	fmt.Fprintln(w)

	if msg != "" {
		fmt.Fprintln(w, msg)
	}
	if diff != "" {
		fmt.Fprint(w, diff)
		if !strings.HasSuffix(diff, "\n") {
			fmt.Fprintln(w)
		}
	}
}

func printPlainWrite(w *os.File, op Op) {
	// Same shape as edit
	printPlainEdit(w, op)
}

func printPlainRename(w *os.File, op Op) {
	status, _ := op["status"].(string)
	from, _ := op["from"].(string)
	to, _ := op["to"].(string)
	fmt.Fprintf(w, "%s %s → %s\n", status, from, to)

	if changes, ok := op["changes"].([]any); ok {
		for _, c := range changes {
			if cm, ok := c.(map[string]any); ok {
				f, _ := cm["file"].(string)
				fmt.Fprintf(w, "  %s\n", f)
			}
		}
	}
}

func printPlainMap(w *os.File, op Op) {
	content, _ := op["content"].(string)
	if content != "" {
		fmt.Fprint(w, content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Fprintln(w)
		}
		return
	}
	// Structured map output — print symbols
	if symbols, ok := op["symbols"].([]any); ok {
		for _, s := range symbols {
			if sm, ok := s.(map[string]any); ok {
				name, _ := sm["name"].(string)
				kind, _ := sm["kind"].(string)
				file, _ := sm["file"].(string)
				line := 0
				if v, ok := sm["line"].(float64); ok {
					line = int(v)
				} else if v, ok := sm["line"].(int); ok {
					line = v
				}
				fmt.Fprintf(w, "%s:%d: %s %s\n", file, line, kind, name)
			}
		}
	}
}

func printPlainReindex(w *os.File, op Op) {
	status, _ := op["status"].(string)
	fmt.Fprintf(w, "reindex %s\n", status)
	if fc, ok := op["files_changed"]; ok {
		fmt.Fprintf(w, "files_changed %v\n", fc)
	}
	if sc, ok := op["symbols_changed"]; ok {
		fmt.Fprintf(w, "symbols_changed %v\n", sc)
	}
}
