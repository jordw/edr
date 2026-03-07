package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/session"
	"github.com/spf13/cobra"
)

// BatchRequest is the JSON structure read from stdin, one per line.
type BatchRequest struct {
	ID    string         `json:"id"`
	Cmd   string         `json:"cmd"`
	Args  []string       `json:"args"`
	Flags map[string]any `json:"flags"`
}

// BatchResponse is the JSON structure written to stdout, one per line.
type BatchResponse struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func init() {
	rootCmd.AddCommand(batchCmd)
}

var batchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Process multiple commands from stdin (JSONL)",
	Long: `Reads JSON commands from stdin, one per line (JSONL), and writes JSON
responses to stdout, one per line. The index database is opened once and
reused across all commands in the batch.

Input format:
  {"id": "1", "cmd": "search", "args": ["Parse"], "flags": {"budget": 500}}
  {"id": "2", "cmd": "symbols", "args": ["internal/edit/edit.go"], "flags": {}}

Output format:
  {"id": "1", "ok": true, "result": [...]}
  {"id": "2", "ok": true, "result": [...]}

Supported commands: init, repo-map, search, search-text, symbols,
read-symbol, read-file, expand, xrefs, gather, find-files, batch-read,
replace-symbol, replace-span, replace-text, replace-lines, write-file,
append-file, insert-after, smart-edit, rename-symbol,
diff-preview, diff-preview-span.

For edit commands, pass the replacement code via the "replacement" flag
(or "content" for write-file/append-file/insert-after) in the JSON object.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return fmt.Errorf("open db: %w", err)
		}
		defer db.Close()

		ctx := context.Background()
		enc := json.NewEncoder(os.Stdout)
		scanner := bufio.NewScanner(os.Stdin)
		sess := session.New()

		// Increase scanner buffer for large input lines (1 MB).
		scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue // skip blank lines
			}

			var req BatchRequest
			if err := json.Unmarshal(line, &req); err != nil {
				resp := BatchResponse{
					ID:    "",
					OK:    false,
					Error: fmt.Sprintf("invalid JSON: %v", err),
				}
				enc.Encode(resp)
				continue
			}

			if req.Flags == nil {
				req.Flags = map[string]any{}
			}

			// Session-layer command
			if req.Cmd == "get-diff" {
				result := sess.GetDiff(req.Args)
				enc.Encode(BatchResponse{ID: req.ID, OK: true, Result: result})
				continue
			}

			if session.EditCommands[req.Cmd] || req.Cmd == "init" {
				sess.InvalidateForEdit(req.Cmd, req.Args)
			}

			result, err := dispatch.Dispatch(ctx, db, req.Cmd, req.Args, req.Flags)
			if err != nil {
				enc.Encode(BatchResponse{ID: req.ID, OK: false, Error: err.Error()})
				continue
			}

			// Apply session post-processing
			data, _ := json.Marshal(result)
			text := string(data)
			text = sess.PostProcess(req.Cmd, req.Args, req.Flags, result, text)

			if session.ReadCommands[req.Cmd] {
				key := sess.CacheKey(req.Cmd, req.Args, req.Flags)
				if sess.Check(key, text) {
					text = fmt.Sprintf(`{"cached":true,"message":"identical to previous response for %s %s"}`, req.Cmd, strings.Join(req.Args, " "))
				}
			}

			var out any
			json.Unmarshal([]byte(text), &out)
			enc.Encode(BatchResponse{ID: req.ID, OK: true, Result: out})
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		return nil
	},
}
