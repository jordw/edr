package cmd

import (
	"strings"

	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
)

// extractAndStripSignature extracts _signature from a dispatch result, records
// the assumption in the session, and removes the internal field before any
// serialization can leak it.
func extractAndStripSignature(sess *session.Session, cmdName string, args []string, result any) {
	if cmdName != "read" && cmdName != "focus" {
		return
	}
	m, ok := result.(map[string]any)
	if !ok {
		return
	}
	sig, hasSig := m["_signature"].(string)
	delete(m, "_signature") // always strip, even if empty
	if !hasSig || sig == "" {
		return
	}
	_, symbol := extractFileSymbol(args)
	if symbol == "" {
		return
	}
	file, _ := extractFileSymbol(args)
	key := file + ":" + symbol
	// Use a placeholder op ID — recordOp hasn't run yet.
	// We'll use "r?" and it gets overwritten on the next read anyway.
	sess.RecordAssumption(key, sig, "r?")
}

// recordOp extracts file/symbol from args and records an op in the session log.
// Also handles build state tracking.
func recordOp(sess *session.Session, cmdName string, args []string, flags map[string]any, result any, ok bool) {
	file, symbol := extractFileSymbol(args)
	// Enrich symbol from --in flag if not already set (e.g. edit main.go --in hello)
	if symbol == "" {
		if inFlag, ok := flags["in"].(string); ok && inFlag != "" {
			symbol = inFlag
		}
	}
	action, kind := classifyOp(cmdName, flags, result, ok)
	sess.RecordOp(cmdName, file, symbol, action, kind, ok)

	// Multi-file rename can modify files beyond the primary
	// target. Record extra ops so GetDirtyFiles includes them in checkpoints.
	if ok && cmdName == "rename" {
		extraFiles := multiFileEdits(cmdName, result, file)
		for _, f := range extraFiles {
			sess.RecordOp(cmdName, f, "", action, kind, true)
		}
	}

	if !ok {
		return
	}

	m, isMap := result.(map[string]any)
	if !isMap {
		return
	}

	// Update assumption op ID now that we have the real one
	if (cmdName == "read" || cmdName == "focus") && symbol != "" {
		key := file + ":" + symbol
		ops := sess.GetRecentOps(1)
		if len(ops) > 0 {
			sess.UpdateAssumptionOpID(key, ops[0].OpID)
		}
	}

	// Track build state
	switch cmdName {
	case "edit", "write":
		status, _ := m["status"].(string)
		if status == "applied" || status == "applied_index_stale" {
			sess.RecordEdit()
		}
	case "verify":
		if status, sOk := m["status"].(string); sOk {
			sess.RecordVerify(status)
		}
	}
}

// multiFileEdits returns extra files modified by multi-file commands (rename, changesig)
// that are not the primary target file. This ensures GetDirtyFiles tracks them for checkpoints.
func multiFileEdits(cmdName string, result any, primaryFile string) []string {
	switch cmdName {
	case "rename":
		if r, ok := result.(*output.RenameResult); ok {
			var extra []string
			for _, f := range r.FilesChanged {
				if f != primaryFile {
					extra = append(extra, f)
				}
			}
			return extra
		}
	}
	return nil
}

// extractFileSymbol parses file and optional symbol from command args.
func extractFileSymbol(args []string) (file, symbol string) {
	if len(args) == 0 {
		return "", ""
	}
	arg := args[0]
	if idx := strings.IndexByte(arg, ':'); idx > 0 {
		return arg[:idx], arg[idx+1:]
	}
	return arg, ""
}

// classifyOp determines the action and display kind for an operation.
func classifyOp(cmd string, flags map[string]any, result any, ok bool) (action, kind string) {
	if !ok {
		return cmd + "_failed", cmd + "_failed"
	}
	switch cmd {
	case "read":
		if _, hasSig := flags["signatures"]; hasSig {
			return "read_signatures", "signatures_read"
		}
		if _, hasSkel := flags["skeleton"]; hasSkel {
			return "read_skeleton", "skeleton_read"
		}
		return "read_symbol", "symbol_read"
	case "edit":
		if _, hasDel := flags["delete"]; hasDel {
			return "delete", "symbol_deleted"
		}
		return "replace_text", "text_replaced"
	case "write":
		return "write_file", "file_written"
	case "search":
		return "search", "search"
	case "verify":
		// Check verify result status
		if m, mOk := result.(map[string]any); mOk {
			if status, sOk := m["status"].(string); sOk && status == "passed" {
				return "verify", "verify_passed"
			}
		}
		return "verify", "verify_failed"
	case "rename":
		return "rename", "renamed"
	case "map":
		return "map", "map_viewed"
	default:
		return cmd, cmd
	}
}
