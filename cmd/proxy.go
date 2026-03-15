package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/output"
	"github.com/spf13/cobra"
)

var proxyRequestCounter uint64

// findSocket checks if a running serve socket is available.
// Returns the socket path if found, empty string otherwise.
func findSocket(cmd *cobra.Command) string {
	// Check --no-proxy flag
	if noProxy, _ := cmd.Flags().GetBool("no-proxy"); noProxy {
		return ""
	}

	root := getRoot(cmd)
	sockPath := filepath.Join(root, ".edr", "serve.sock")

	info, err := os.Stat(sockPath)
	if err != nil {
		return "" // socket doesn't exist
	}

	// Verify it's a socket
	if info.Mode()&os.ModeSocket == 0 {
		return "" // not a socket file
	}

	return sockPath
}

// normalizeFlags converts cobra-style hyphenated flag names to underscore convention
// used by the batch JSON protocol (e.g., "old-text" → "old_text", "dry-run" → "dry_run").
func normalizeFlags(flags map[string]any) map[string]any {
	out := make(map[string]any, len(flags))
	for k, v := range flags {
		out[strings.ReplaceAll(k, "-", "_")] = v
	}
	return out
}

// cmdToDoJSON converts a CLI command invocation into batch JSON for the serve protocol.
func cmdToDoJSON(cmdName string, args []string, flags map[string]any) json.RawMessage {
	flags = normalizeFlags(flags)

	spec := cmdspec.ByName(cmdName)
	if spec == nil {
		// Unknown command — can't proxy
		return nil
	}

	switch spec.Category {
	case cmdspec.CatRead:
		return readCmdToJSON(cmdName, args, flags)
	case cmdspec.CatWrite:
		return writeCmdToJSON(cmdName, args, flags)
	case cmdspec.CatGlobalMutate:
		return mutateCmdToJSON(cmdName, args, flags)
	case cmdspec.CatMeta:
		return metaCmdToJSON(cmdName, args, flags)
	}
	return nil
}

// readCmdToJSON converts read-category commands (read, search, map, explore, refs, find) to batch JSON.
func readCmdToJSON(cmdName string, args []string, flags map[string]any) json.RawMessage {
	switch cmdName {
	case "read":
		reads := make([]map[string]any, 0, len(args))
		// Each arg could be a file, file:symbol, or file start end
		if len(args) >= 3 {
			// Could be: file start end
			r := map[string]any{"file": args[0]}
			// Try to interpret as line range
			if isNumeric(args[1]) && isNumeric(args[2]) {
				r["start_line"] = parseIntArg(args[1])
				r["end_line"] = parseIntArg(args[2])
			} else {
				// Multiple files
				for _, a := range args {
					reads = append(reads, fileArgToRead(a, flags))
				}
				return marshalBatch(map[string]any{"reads": reads})
			}
			copyReadFlags(r, flags)
			reads = append(reads, r)
		} else if len(args) == 2 {
			// Could be: file symbol or file start (not typical)
			r := fileArgToRead(args[0], flags)
			if r["symbol"] == nil || r["symbol"] == "" {
				r["symbol"] = args[1]
			}
			reads = append(reads, r)
		} else if len(args) == 1 {
			reads = append(reads, fileArgToRead(args[0], flags))
		}
		if len(reads) == 0 {
			return nil
		}
		return marshalBatch(map[string]any{"reads": reads})

	case "search", "map", "explore", "refs", "find":
		q := map[string]any{"cmd": cmdName}
		// Copy all flags
		for k, v := range flags {
			q[k] = v
		}
		// Map positional args
		switch cmdName {
		case "search":
			if len(args) >= 1 {
				q["pattern"] = args[0]
			}
		case "map":
			if len(args) >= 1 {
				q["file"] = args[0]
			}
		case "explore":
			if len(args) == 2 {
				q["file"] = args[0]
				q["symbol"] = args[1]
			} else if len(args) == 1 {
				q["symbol"] = args[0]
			}
		case "refs":
			if len(args) == 2 {
				q["file"] = args[0]
				q["symbol"] = args[1]
			} else if len(args) == 1 {
				q["symbol"] = args[0]
			}
		case "find":
			if len(args) >= 1 {
				q["pattern"] = args[0]
			}
		}
		return marshalBatch(map[string]any{"queries": []map[string]any{q}})
	}
	return nil
}

// writeCmdToJSON converts write-category commands (edit, write) to batch JSON.
func writeCmdToJSON(cmdName string, args []string, flags map[string]any) json.RawMessage {
	switch cmdName {
	case "edit":
		e := map[string]any{}
		if len(args) >= 1 {
			e["file"] = args[0]
		}
		if len(args) >= 2 {
			e["symbol"] = args[1]
		}
		for k, v := range flags {
			e[k] = v
		}
		return marshalBatch(map[string]any{"edits": []map[string]any{e}})

	case "write":
		w := map[string]any{}
		if len(args) >= 1 {
			w["file"] = args[0]
		}
		for k, v := range flags {
			w[k] = v
		}
		return marshalBatch(map[string]any{"writes": []map[string]any{w}})
	}
	return nil
}

// mutateCmdToJSON converts global-mutate commands (rename, init, edit-plan) to batch JSON.
func mutateCmdToJSON(cmdName string, args []string, flags map[string]any) json.RawMessage {
	switch cmdName {
	case "rename":
		r := map[string]any{}
		if len(args) >= 2 {
			r["old_name"] = args[0]
			r["new_name"] = args[1]
		}
		for k, v := range flags {
			r[k] = v
		}
		return marshalBatch(map[string]any{"renames": []map[string]any{r}})

	case "init":
		return marshalBatch(map[string]any{"init": true})

	case "edit-plan":
		// edit-plan is complex — pass through edits flag
		batch := map[string]any{}
		if edits, ok := flags["edits"]; ok {
			batch["edits"] = edits
		}
		if dryRun, ok := flags["dry_run"]; ok {
			batch["dry_run"] = dryRun
		}
		return marshalBatch(batch)
	}
	return nil
}

// metaCmdToJSON converts meta commands (verify) to batch JSON.
func metaCmdToJSON(cmdName string, args []string, flags map[string]any) json.RawMessage {
	switch cmdName {
	case "verify":
		batch := map[string]any{"verify": true}
		if cmd, ok := flags["command"]; ok {
			batch["verify"] = cmd
		}
		return marshalBatch(batch)
	}
	return nil
}

// proxyViaSocket sends a batch request over a Unix socket and returns the result.
func proxyViaSocket(sockPath string, batchJSON json.RawMessage) (json.RawMessage, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("connect to server: %w", err)
	}
	defer conn.Close()

	// Build request envelope
	reqID := fmt.Sprintf("cli-%d", atomic.AddUint64(&proxyRequestCounter, 1))
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(batchJSON, &envelope); err != nil {
		return nil, fmt.Errorf("invalid batch JSON: %w", err)
	}

	// Add request_id
	reqIDJSON, _ := json.Marshal(reqID)
	envelope["request_id"] = json.RawMessage(reqIDJSON)

	line, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Send request
	line = append(line, '\n')
	if _, err := conn.Write(line); err != nil {
		return nil, fmt.Errorf("write to server: %w", err)
	}

	// Close write side to signal we're done sending
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}

	// Read response
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read from server: %w", err)
		}
		return nil, fmt.Errorf("no response from server")
	}

	var resp serveResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !resp.OK {
		if resp.Error != nil {
			return nil, fmt.Errorf("server error [%s]: %s", resp.Error.Code, resp.Error.Message)
		}
		return nil, fmt.Errorf("server returned error")
	}

	return resp.Result, nil
}

// extractResultForCmd unwraps a batch response to the single relevant section.
func extractResultForCmd(cmdName string, fullResult json.RawMessage) json.RawMessage {
	spec := cmdspec.ByName(cmdName)
	if spec == nil {
		return fullResult
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(fullResult, &m); err != nil {
		return fullResult
	}

	switch spec.Category {
	case cmdspec.CatRead:
		if cmdName == "read" {
			return unwrapArraySection(m, "reads")
		}
		return unwrapArraySection(m, "queries")

	case cmdspec.CatWrite:
		if cmdName == "edit" {
			if edits, ok := m["edits"]; ok {
				return edits
			}
		} else if cmdName == "write" {
			return unwrapArraySection(m, "writes")
		}

	case cmdspec.CatGlobalMutate:
		if cmdName == "rename" {
			return unwrapArraySection(m, "renames")
		}
		if cmdName == "init" {
			if init, ok := m["init"]; ok {
				return init
			}
		}
		if cmdName == "edit-plan" {
			if edits, ok := m["edits"]; ok {
				return edits
			}
		}

	case cmdspec.CatMeta:
		if cmdName == "verify" {
			if verify, ok := m["verify"]; ok {
				return verify
			}
		}
	}

	return fullResult
}

// unwrapArraySection extracts a section from a batch response and returns the first element
// if the array has exactly one entry.
func unwrapArraySection(m map[string]json.RawMessage, key string) json.RawMessage {
	section, ok := m[key]
	if !ok {
		return nil
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(section, &arr); err != nil || len(arr) != 1 {
		return section
	}

	// For queries, unwrap the inner result
	var item map[string]json.RawMessage
	if err := json.Unmarshal(arr[0], &item); err == nil {
		if result, ok := item["result"]; ok {
			return result
		}
	}
	return arr[0]
}

// proxyCmd orchestrates the full proxy flow: flags → batch JSON → socket → extract → print.
func proxyCmd(cmd *cobra.Command, sockPath, cmdName string, args []string, flags map[string]any) error {
	batchJSON := cmdToDoJSON(cmdName, args, flags)
	if batchJSON == nil {
		return fmt.Errorf("cannot proxy command %q", cmdName)
	}

	result, err := proxyViaSocket(sockPath, batchJSON)
	if err != nil {
		return err
	}

	extracted := extractResultForCmd(cmdName, result)
	output.Print(extracted)
	return nil
}

// --- helpers ---

func fileArgToRead(arg string, flags map[string]any) map[string]any {
	r := map[string]any{}
	// Check for file:symbol syntax
	for i := len(arg) - 1; i > 0; i-- {
		if arg[i] == ':' {
			r["file"] = arg[:i]
			r["symbol"] = arg[i+1:]
			copyReadFlags(r, flags)
			return r
		}
		if arg[i] == '/' || arg[i] == '\\' {
			break // path separator before colon means no symbol
		}
	}
	r["file"] = arg
	copyReadFlags(r, flags)
	return r
}

func copyReadFlags(r map[string]any, flags map[string]any) {
	for _, k := range []string{"budget", "signatures", "depth", "symbols", "full"} {
		if v, ok := flags[k]; ok {
			r[k] = v
		}
	}
}

func marshalBatch(m map[string]any) json.RawMessage {
	data, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func isNumeric(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseIntArg(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
