package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
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

Batch equivalent: edr do with reads/queries/edits/writes arrays.

Input format:
  {"id": "1", "cmd": "search", "args": ["Parse"], "flags": {"budget": 500}}
  {"id": "2", "cmd": "map", "args": ["internal/edit/edit.go"], "flags": {}}

Output format:
  {"id": "1", "ok": true, "result": [...]}
  {"id": "2", "ok": true, "result": [...]}

Supported commands: read, write, edit, map, search, explore, refs, rename,
find, edit-plan, verify, init.

For edit commands, pass replacement code via "new_text" in flags.
For write commands, pass file content via "content" or "new_text" in flags.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndexQuiet(cmd)
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

			if cmdspec.ModifiesState(req.Cmd) {
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

			if cmdspec.IsRead(req.Cmd) {
				key := sess.CacheKey(req.Cmd, req.Args, req.Flags)
				if sess.Check(key, text) {
					text = fmt.Sprintf(`{"cached":true,"message":"identical to previous response for %s %s"}`, req.Cmd, strings.Join(req.Args, " "))
				}
			}

			var out any
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				enc.Encode(BatchResponse{ID: req.ID, OK: false, Error: "invalid JSON output: " + err.Error()})
				continue
			}
			enc.Encode(BatchResponse{ID: req.ID, OK: true, Result: out})
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		return nil
	},
}
