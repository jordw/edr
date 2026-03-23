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
		case "reindex", "reset":
			plainReindex(w, op)
		case "context":
			plainNext(w, op)
		case "checkpoint":
			plainCheckpoint(w, op)
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
	writeHeader(w, h)

	content, _ := op["content"].(string)
	writeBody(w, content)
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
	hStr(h, "file", op, "file")
	hStr(h, "status", op, "status")
	hStr(h, "hash", op, "hash")
	hStr(h, "msg", op, "message")
	writeHeader(w, h)

	diff, _ := op["diff"].(string)
	writeBody(w, diff)
}

func plainRename(w *os.File, op Op) {
	h := map[string]any{}
	hStr(h, "status", op, "status")
	hStr(h, "from", op, "old_name")
	hStr(h, "to", op, "new_name")
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
				if endLine > 0 {
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
	if v, ok := op["scope"].(string); ok {
		h["scope"] = v
	}
	if v, ok := op["session"].(string); ok {
		h["session"] = v
	}
	writeHeader(w, h)
}

func plainNext(w *os.File, op Op) {
	h := map[string]any{}
	if v, ok := op["total_ops"]; ok {
		h["ops"] = v
	}
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

	// Add current count to header
	if current, ok := op["current"].([]any); ok && len(current) > 0 {
		h["current"] = len(current)
	}

	writeHeader(w, h)

	// Focus line
	if focus, ok := op["focus"].(string); ok && focus != "" {
		fmt.Fprintf(w, "focus: %s\n\n", focus)
	}

	// Recent ops
	if recent, ok := op["recent"].([]any); ok && len(recent) > 0 {
		fmt.Fprintln(w, "recent:")
		for _, r := range recent {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			opID, _ := rm["op_id"].(string)
			file, _ := rm["file"].(string)
			symbol, _ := rm["symbol"].(string)
			kind, _ := rm["kind"].(string)

			loc := file
			if symbol != "" {
				loc = file + ":" + symbol
			}
			if loc == "" {
				loc = "-"
			}

			fmt.Fprintf(w, "  %s: %s — %s\n", opID, loc, kind)
		}
	} else {
		fmt.Fprintln(w, "(no recent ops)")
	}

	// Build state
	if build, ok := op["build"].(map[string]any); ok {
		status, _ := build["status"].(string)
		editsSince, _ := build["edits_since"].(bool)
		fmt.Fprintln(w)
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
			id, _ := fm["id"].(string)
			typ, _ := fm["type"].(string)
			confidence, _ := fm["confidence"].(string)
			file, _ := fm["file"].(string)
			symbol, _ := fm["symbol"].(string)
			reason, _ := fm["reason"].(string)
			assumedAt, _ := fm["assumed_at"].(string)
			suggest, _ := fm["suggest"].(string)

			fmt.Fprintf(w, "  [%s] %s (%s)\n", id, typ, confidence)
			detail := fmt.Sprintf("    %s:%s", file, symbol)
			if reason != "" {
				detail += " — " + reason
			}
			if assumedAt != "" {
				detail += fmt.Sprintf(" (read at %s)", assumedAt)
			}
			fmt.Fprintln(w, detail)
			if suggest != "" {
				fmt.Fprintf(w, "    => %s\n", suggest)
			}
		}
	}

	// Current: live signatures of active symbols
	if current, ok := op["current"].([]any); ok && len(current) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "current:")
		for _, c := range current {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			symbol, _ := cm["symbol"].(string)
			reason, _ := cm["reason"].(string)
			sig, _ := cm["signature"].(string)

			fmt.Fprintf(w, "  %s(%s): %s\n", symbol, reason, sig)
		}
	}
}

func plainCheckpoint(w *os.File, op Op) {
	status, _ := op["status"].(string)

	switch status {
	case "created":
		h := map[string]any{"status": "created"}
		hStr(h, "id", op, "id")
		hStr(h, "label", op, "label")
		hStr(h, "op_id", op, "op_id")
		if fc, ok := op["file_count"]; ok {
			h["files"] = fc
		}
		writeHeader(w, h)

	case "restored":
		h := map[string]any{"status": "restored"}
		hStr(h, "target", op, "target")
		hStr(h, "pre_restore", op, "pre_restore_checkpoint")
		if restored, ok := op["restored"].([]string); ok {
			h["files"] = len(restored)
		}
		writeHeader(w, h)
		if restored, ok := op["restored"].([]string); ok && len(restored) > 0 {
			fmt.Fprintln(w, "restored:")
			for _, f := range restored {
				fmt.Fprintf(w, "  %s\n", f)
			}
		}
		if notRemoved, ok := op["not_removed"].([]string); ok && len(notRemoved) > 0 {
			fmt.Fprintln(w, "\nnot removed (created after checkpoint):")
			for _, f := range notRemoved {
				fmt.Fprintf(w, "  %s\n", f)
			}
		}

	case "dropped":
		h := map[string]any{"status": "dropped"}
		hStr(h, "id", op, "id")
		writeHeader(w, h)

	default:
		// --list, --diff, or unknown
		if cps, ok := op["checkpoints"]; ok {
			writeHeader(w, map[string]any{"status": "list"})
			if items, ok := cps.([]any); ok && len(items) > 0 {
				for _, item := range items {
					if m, ok := item.(map[string]any); ok {
						id, _ := m["id"].(string)
						label, _ := m["label"].(string)
						fc, _ := m["file_count"]
						opID, _ := m["op_id"].(string)
						line := fmt.Sprintf("  %s", id)
						if label != "" {
							line += fmt.Sprintf(" (%s)", label)
						}
						line += fmt.Sprintf(" — %v files, at %s", fc, opID)
						fmt.Fprintln(w, line)
					}
				}
			} else {
				fmt.Fprintln(w, "(no checkpoints)")
			}
			return
		}
		if diffs, ok := op["diffs"]; ok {
			cpID, _ := op["checkpoint"].(string)
			writeHeader(w, map[string]any{"status": "diff", "checkpoint": cpID})
			if items, ok := diffs.([]any); ok && len(items) > 0 {
				for _, item := range items {
					if m, ok := item.(map[string]any); ok {
						path, _ := m["path"].(string)
						st, _ := m["status"].(string)
						fmt.Fprintf(w, "  %s: %s\n", st, path)
					}
				}
			} else {
				fmt.Fprintln(w, "(no changes)")
			}
			return
		}
		writeHeader(w, op)
	}
}

// Header helpers — reduce boilerplate in plain* functions.

// hStr copies a string field from op to header, optionally renaming.
func hStr(h map[string]any, hKey string, op Op, opKey string) {
	if v, ok := op[opKey].(string); ok && v != "" {
		h[hKey] = v
	}
}

// hSession copies the session field if it is "unchanged" or "new".
func hSession(h map[string]any, op Op) {
	if v, ok := op["session"].(string); ok && (v == "unchanged" || v == "new") {
		h["session"] = v
	}
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
