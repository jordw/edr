package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(mcpCmd)
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as an MCP (Model Context Protocol) server over stdio",
	Long: `Starts edr as a long-running MCP server that communicates via
JSON-RPC 2.0 over stdin/stdout. Exposes a single "edr" tool that
wraps all edr commands. The index database stays open across calls.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		db, err := index.OpenDB(root)
		if err != nil {
			return err
		}
		defer db.Close()

		output.SetRoot(db.Root())

		// Auto-index if needed
		ctx := context.Background()
		files, _, _ := db.Stats(ctx)
		if files == 0 {
			index.IndexRepo(ctx, db)
		}

		return serveMCP(db)
	},
}

// --- JSON-RPC types ---

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- MCP-specific types ---

type mcpInitResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      mcpServerInfo  `json:"serverInfo"`
}

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpTool struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	InputSchema mcpSchema `json:"inputSchema"`
}

type mcpSchema struct {
	Type       string             `json:"type"`
	Properties map[string]mcpProp `json:"properties"`
	Required   []string           `json:"required"`
}

type mcpProp struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	// For array items
	Items *mcpPropItems `json:"items,omitempty"`
}

type mcpPropItems struct {
	Type string `json:"type"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type edrToolArgs struct {
	Cmd   string         `json:"cmd"`
	Args  []string       `json:"args"`
	Flags map[string]any `json:"flags"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- server loop ---

func serveMCP(db *index.DB) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	ctx := context.Background()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}

		switch req.Method {
		case "initialize":
			enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mcpInitResult{
					ProtocolVersion: "2024-11-05",
					Capabilities: map[string]any{
						"tools": map[string]any{},
					},
					ServerInfo: mcpServerInfo{
						Name:    "edr",
						Version: "0.1.0",
					},
				},
			})

		case "notifications/initialized":
			// No response needed for notifications
			continue

		case "tools/list":
			enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"tools": []mcpTool{{
						Name:        "edr",
						Description: "Your default tool for ALL file operations. Reading: read-file, read-symbol, search (--body), search-text, symbols, repo-map, expand, xrefs, gather. Editing: smart-edit (read+diff+replace in one call), replace-text, replace-symbol, replace-lines, replace-span. Creating: write-file, append-file, insert-after. Refactoring: rename-symbol (--dry-run), diff-preview. All edits return new hash for chaining. See CLAUDE.md for full docs.",
						InputSchema: mcpSchema{
							Type: "object",
							Properties: map[string]mcpProp{
								"cmd": {
									Type:        "string",
									Description: "Command name (e.g. search, expand, replace-symbol)",
								},
								"args": {
									Type:        "array",
									Description: "Positional arguments",
									Items:       &mcpPropItems{Type: "string"},
								},
								"flags": {
									Type:        "object",
									Description: "Flags (e.g. {\"budget\": 500, \"body\": true, \"replacement\": \"new code\"})",
								},
							},
							Required: []string{"cmd"},
						},
					}},
				},
			})

		case "tools/call":
			var params mcpToolCallParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				enc.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &jsonRPCError{Code: -32602, Message: err.Error()},
				})
				continue
			}

			var args edrToolArgs
			if err := json.Unmarshal(params.Arguments, &args); err != nil {
				enc.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &jsonRPCError{Code: -32602, Message: err.Error()},
				})
				continue
			}

			if args.Args == nil {
				args.Args = []string{}
			}
			if args.Flags == nil {
				args.Flags = map[string]any{}
			}

			// Check for stale files before dispatching
			if stale, _ := index.HasStaleFiles(ctx, db); stale {
				index.IndexRepo(ctx, db)
			}

			result, err := dispatch.Dispatch(ctx, db, args.Cmd, args.Args, args.Flags)

			var text string
			if err != nil {
				text = fmt.Sprintf(`{"error": %q}`, err.Error())
			} else {
				data, _ := json.Marshal(result)
				text = string(data)
			}

			enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mcpToolResult{
					Content: []mcpContent{{
						Type: "text",
						Text: text,
					}},
				},
			})

		default:
			if req.ID != nil {
				enc.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &jsonRPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
				})
			}
		}
	}

	return scanner.Err()
}
