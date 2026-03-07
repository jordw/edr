package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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

// --- working-set tracking ---

// mcpWorkingSet tracks content hashes of previously sent responses to avoid
// re-sending identical content in the same MCP session. Read-like commands
// are cached by a canonical key; edit commands invalidate affected files.
type mcpWorkingSet struct {
	responses map[string]string // canonical key → SHA-256 prefix of last response
}

func newMCPWorkingSet() *mcpWorkingSet {
	return &mcpWorkingSet{responses: make(map[string]string)}
}

// readCommands are commands whose responses can be cached.
var readCommands = map[string]bool{
	"read-file": true, "read-symbol": true, "symbols": true,
	"expand": true, "gather": true, "batch-read": true,
	"repo-map": true, "search": true, "search-text": true,
	"xrefs": true, "find-files": true,
}

// editCommands are commands that modify files and invalidate cache.
var editCommands = map[string]bool{
	"smart-edit": true, "replace-text": true, "replace-symbol": true,
	"replace-lines": true, "replace-span": true, "write-file": true,
	"append-file": true, "insert-after": true, "rename-symbol": true,
	"edit-plan": true,
}

func (ws *mcpWorkingSet) contentHash(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

// cacheKey builds a canonical key from a command invocation.
func (ws *mcpWorkingSet) cacheKey(cmd string, args []string, flags map[string]any) string {
	// Include cmd + args + sorted budget/body flags that affect output
	key := cmd + "\x00" + strings.Join(args, "\x00")
	// Include flags that affect output content
	for _, f := range []string{"budget", "body", "callers", "deps", "signatures", "context", "regex", "include", "exclude", "dir", "glob", "type", "grep", "symbols"} {
		if v, ok := flags[f]; ok {
			key += fmt.Sprintf("\x00%s=%v", f, v)
		}
	}
	return key
}

// check returns true if this response was already sent identically.
// If not cached or changed, records the new hash and returns false.
func (ws *mcpWorkingSet) check(key, responseText string) bool {
	h := ws.contentHash(responseText)
	if prev, ok := ws.responses[key]; ok && prev == h {
		return true // identical to last time
	}
	ws.responses[key] = h
	return false
}

// invalidateFile removes all cached entries that reference a file path.
func (ws *mcpWorkingSet) invalidateFile(file string) {
	for k := range ws.responses {
		// Cache keys start with "cmd\x00arg1\x00..." — check if file appears as arg
		if strings.Contains(k, file) {
			delete(ws.responses, k)
		}
	}
}

// invalidateForEdit handles cache invalidation for edit commands.
func (ws *mcpWorkingSet) invalidateForEdit(cmd string, args []string) {
	if cmd == "rename-symbol" {
		// Rename affects many files — clear everything
		ws.responses = make(map[string]string)
		return
	}
	// Most edit commands have the file as first arg
	if len(args) > 0 {
		ws.invalidateFile(args[0])
	}
	// init re-indexes everything
	if cmd == "init" {
		ws.responses = make(map[string]string)
	}
}

// --- server loop ---

func serveMCP(db *index.DB) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	ctx := context.Background()
	ws := newMCPWorkingSet()

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
						Description: "Your default tool for ALL file operations. Use cmd=multi with flags.commands=[{cmd,args,flags},...] to batch multiple commands in ONE call. Reading: read-file, read-symbol, search (--body), search-text, symbols, repo-map (--dir, --grep, --type, --glob), expand (--signatures), xrefs, gather (--signatures). Editing: smart-edit, replace-text, replace-symbol, replace-lines, replace-span, edit-plan (atomic multi-edit via flags.edits array). Creating: write-file, append-file, insert-after. Refactoring: rename-symbol (--dry-run), diff-preview. Analysis: impact (transitive callers), call-chain (path between symbols), verify (run build/typecheck). All edits return hash. See CLAUDE.md.",
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

			var text string
			if args.Cmd == "multi" {
				// Multi-command batch: extract commands from flags
				var cmds []dispatch.MultiCmd
				if rawCmds, ok := args.Flags["commands"]; ok {
					// Re-marshal and unmarshal to get proper typing
					raw, _ := json.Marshal(rawCmds)
					json.Unmarshal(raw, &cmds)
				}
				if len(cmds) == 0 {
					text = `{"error": "multi requires flags.commands array"}`
				} else {
					results := dispatch.DispatchMulti(ctx, db, cmds)
					// Invalidate for any edit commands in the batch
					for _, c := range cmds {
						if editCommands[c.Cmd] {
							ws.invalidateForEdit(c.Cmd, c.Args)
						}
					}
					data, _ := json.Marshal(results)
					text = string(data)
				}
			} else {
				// Invalidate cache for edit commands
				if editCommands[args.Cmd] || args.Cmd == "init" {
					ws.invalidateForEdit(args.Cmd, args.Args)
				}

				result, err := dispatch.Dispatch(ctx, db, args.Cmd, args.Args, args.Flags)
				if err != nil {
					text = fmt.Sprintf(`{"error": %q}`, err.Error())
				} else {
					data, _ := json.Marshal(result)
					text = string(data)

					// Working-set dedup for read commands
					if readCommands[args.Cmd] {
						key := ws.cacheKey(args.Cmd, args.Args, args.Flags)
						if ws.check(key, text) {
							text = fmt.Sprintf(`{"cached":true,"message":"identical to previous response for %s %s"}`, args.Cmd, strings.Join(args.Args, " "))
						}
					}
				}
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
