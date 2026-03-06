package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
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
read-symbol, expand, xrefs, gather.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		db, err := index.OpenDB(root)
		if err != nil {
			return fmt.Errorf("open db: %w", err)
		}
		defer db.Close()

		ctx := context.Background()
		enc := json.NewEncoder(os.Stdout)
		scanner := bufio.NewScanner(os.Stdin)

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

			result, err := dispatch.Dispatch(ctx, db, req.Cmd, req.Args, req.Flags)
			if err != nil {
				resp := BatchResponse{
					ID:    req.ID,
					OK:    false,
					Error: err.Error(),
				}
				enc.Encode(resp)
				continue
			}

			resp := BatchResponse{
				ID:     req.ID,
				OK:     true,
				Result: result,
			}
			enc.Encode(resp)
		}

		if err := scanner.Err(); err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		return nil
	},
}
