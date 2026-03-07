package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
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
			if err := db.WithWriteLock(func() error {
				_, _, err := index.IndexRepo(ctx, db)
				return err
			}); err != nil {
				fmt.Fprintf(os.Stderr, "edr: initial indexing failed: %v\n", err)
			}
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

// UnmarshalJSON handles MCP clients that may send args as a single string
// instead of an array, or flags as a JSON string instead of an object.
func (e *edrToolArgs) UnmarshalJSON(data []byte) error {
	// Use a raw struct to avoid infinite recursion
	var raw struct {
		Cmd   string          `json:"cmd"`
		Args  json.RawMessage `json:"args"`
		Flags json.RawMessage `json:"flags"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Cmd = raw.Cmd

	// Parse args: accept string or []string
	if len(raw.Args) > 0 {
		if raw.Args[0] == '"' {
			var s string
			if err := json.Unmarshal(raw.Args, &s); err == nil {
				e.Args = []string{s}
			}
		} else {
			json.Unmarshal(raw.Args, &e.Args)
		}
	}

	// Parse flags: accept string (double-encoded JSON) or object
	if len(raw.Flags) > 0 {
		if raw.Flags[0] == '"' {
			var s string
			if err := json.Unmarshal(raw.Flags, &s); err == nil {
				json.Unmarshal([]byte(s), &e.Flags)
			}
		} else {
			json.Unmarshal(raw.Flags, &e.Flags)
		}
	}
	return nil
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Post-process multi results ---

// postProcessMultiResults applies session post-processing to each sub-result.
func postProcessMultiResults(sess *session.Session, cmds []dispatch.MultiCmd, results []dispatch.MultiResult) string {
	type processedResult struct {
		Cmd    string `json:"cmd"`
		OK     bool   `json:"ok"`
		Result any    `json:"result,omitempty"`
		Error  string `json:"error,omitempty"`
	}

	processed := make([]processedResult, len(results))
	for i, r := range results {
		processed[i] = processedResult{Cmd: r.Cmd, OK: r.OK, Error: r.Error}
		if !r.OK || r.Result == nil {
			continue
		}

		data, err := json.Marshal(r.Result)
		if err != nil {
			processed[i].Result = r.Result
			continue
		}

		cmd := cmds[i].Cmd
		flags := cmds[i].Flags
		if flags == nil {
			flags = map[string]any{}
		}
		cArgs := cmds[i].Args
		if cArgs == nil {
			cArgs = []string{}
		}

		newText := sess.PostProcess(cmd, cArgs, flags, r.Result, string(data))
		if newText != string(data) {
			var newResult any
			if err := json.Unmarshal([]byte(newText), &newResult); err == nil {
				processed[i].Result = newResult
			} else {
				processed[i].Result = r.Result
			}
		} else {
			processed[i].Result = r.Result
		}
	}

	out, _ := json.Marshal(processed)
	return string(out)
}

// --- server loop ---

func serveMCP(db *index.DB) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 4*1024*1024)
	enc := json.NewEncoder(os.Stdout)

	ctx := context.Background()
	sess := session.New()

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Skip Content-Length headers from LSP-style framed transports
		if len(line) > 15 && (line[0] == 'C' || line[0] == 'c') &&
			strings.HasPrefix(strings.ToLower(string(line[:16])), "content-length:") {
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
			continue

		case "tools/list":
			enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: map[string]any{
					"tools": []mcpTool{{
						Name:        "edr",
						Description: "Your default tool for ALL file operations. Use this instead of Read, Edit, Write, Grep, Glob. All commands use {cmd, args, flags}.\n\n" +
							"COMMANDS AND FLAGS:\n\n" +
							"read: Read files/symbols. args: [\"file\"] or [\"file:symbol\"] or multiple. flags: {budget, symbols (bool), signatures (bool), depth (int), full (bool)}\n" +
							"  Ex: {cmd:\"read\", args:[\"src/main.go\"]}  {cmd:\"read\", args:[\"src/main.go:MyFunc\"], flags:{budget:300}}\n\n" +
							"write: Create/overwrite files. args: [\"file\"]. flags: {content: \"file contents\", mkdir (bool), append (bool), after: \"symbol\", inside: \"Container\"}\n" +
							"  Ex: {cmd:\"write\", args:[\"foo.yml\"], flags:{content:\"key: value\\n\"}}  {cmd:\"write\", args:[\"f.go\"], flags:{inside:\"MyStruct\", content:\"NewField int\"}}\n\n" +
							"edit: Edit files. args: [\"file\"] or [\"file\", \"symbol\"]. flags: {old_text, new_text, regex (bool), all (bool), start_line, end_line, dry_run (bool)}\n" +
							"  Ex: {cmd:\"edit\", args:[\"f.go\"], flags:{old_text:\"old code\", new_text:\"new code\"}}\n\n" +
							"search: Search symbols or text. args: [\"pattern\"]. flags: {body (bool), text (bool), regex (bool), include, exclude, context (int), budget}\n" +
							"map: Repo/file symbol map. args: [] or [\"file\"]. flags: {budget, dir, glob, type, grep}\n" +
							"explore: Symbol info. args: [\"symbol\"] or [\"file\",\"symbol\"]. flags: {body (bool), callers (bool), deps (bool), gather (bool), signatures (bool), budget}\n" +
							"refs: Find references. args: [\"symbol\"]. flags: {impact (bool), depth (int), chain: \"target\"}\n" +
							"find: Find files by glob. args: [\"pattern\"]. flags: {dir, budget}\n" +
							"rename: Cross-file rename. args: [\"oldName\",\"newName\"]. flags: {dry_run (bool), scope: \"glob\"}\n" +
							"edit-plan: Atomic multi-edit. flags: {edits: [{file, old_text, new_text}, ...], dry_run (bool)}\n" +
							"verify: Run build/typecheck. flags: {command, timeout}\n" +
							"multi: Batch commands. flags: {commands: [{cmd, args, flags}, ...]}\n" +
							"get-diff: Get stored diff from last large edit. args: [\"file\"]\n\n" +
							"All edits return {ok, file, hash}. Re-reads return deltas if unchanged. Small edit diffs (<=20 lines) inline; large diffs stored (use get-diff).",
						InputSchema: mcpSchema{
							Type: "object",
							Properties: map[string]mcpProp{
								"cmd": {
									Type:        "string",
									Description: "Command name (e.g. read, edit, search, map, explore, refs, write, find, rename, verify)",
								},
								"args": {
									Type:        "array",
									Description: "Positional arguments",
									Items:       &mcpPropItems{Type: "string"},
								},
								"flags": {
									Type:        "object",
									Description: "Flags (e.g. {\"budget\": 500, \"body\": true, \"content\": \"new file contents\", \"old_text\": \"find\", \"new_text\": \"replace\"})",
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

			// Session-layer commands (handled before dispatch)
			if args.Cmd == "get-diff" {
				result := sess.GetDiff(args.Args)
				data, _ := json.Marshal(result)
				enc.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result: mcpToolResult{
						Content: []mcpContent{{Type: "text", Text: string(data)}},
					},
				})
				continue
			}

			// Check for stale files before dispatching
			if stale, _ := index.HasStaleFiles(ctx, db); stale {
				if err := db.WithWriteLock(func() error {
					_, _, err := index.IndexRepo(ctx, db)
					return err
				}); err != nil {
					fmt.Fprintf(os.Stderr, "edr: re-index failed: %v\n", err)
				}
			}

			var text string
			if args.Cmd == "multi" {
				var cmds []dispatch.MultiCmd
				if rawCmds, ok := args.Flags["commands"]; ok {
					raw, _ := json.Marshal(rawCmds)
					json.Unmarshal(raw, &cmds)
				}
				if len(cmds) == 0 {
					text = `{"error": "multi requires flags.commands array"}`
				} else {
					results := dispatch.DispatchMulti(ctx, db, cmds)
					for _, c := range cmds {
						if session.EditCommands[c.Cmd] {
							sess.InvalidateForEdit(c.Cmd, c.Args)
						}
					}
					text = postProcessMultiResults(sess, cmds, results)
				}
			} else {
				if session.EditCommands[args.Cmd] || args.Cmd == "init" {
					sess.InvalidateForEdit(args.Cmd, args.Args)
				}

				result, err := dispatch.Dispatch(ctx, db, args.Cmd, args.Args, args.Flags)
				if err != nil {
					if ambErr := asAmbiguousError(err); ambErr != nil {
						data, _ := json.Marshal(ambErr)
						text = string(data)
					} else {
						text = fmt.Sprintf(`{"error": %q}`, err.Error())
					}
				} else {
					data, _ := json.Marshal(result)
					text = string(data)

					// Apply post-processing pipeline (slim edits, delta reads, body stripping)
					text = sess.PostProcess(args.Cmd, args.Args, args.Flags, result, text)

					// Working-set dedup for read commands (after post-processing)
					if session.ReadCommands[args.Cmd] {
						key := sess.CacheKey(args.Cmd, args.Args, args.Flags)
						if sess.Check(key, text) {
							text = fmt.Sprintf(`{"cached":true,"message":"identical to previous response for %s %s"}`, args.Cmd, strings.Join(args.Args, " "))
						}
					}
				}
			}

			enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mcpToolResult{
					Content: []mcpContent{{Type: "text", Text: text}},
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

// ambiguousResult is the structured JSON response for ambiguous symbol errors.
type ambiguousResult struct {
	Error      string               `json:"error"`
	Symbol     string               `json:"symbol"`
	Candidates []ambiguousCandidate `json:"candidates"`
	Hint       string               `json:"hint"`
}

type ambiguousCandidate struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Type string `json:"type"`
}

func asAmbiguousError(err error) *ambiguousResult {
	var ambErr *index.AmbiguousSymbolError
	if !errors.As(err, &ambErr) {
		return nil
	}
	r := &ambiguousResult{
		Error:  "ambiguous",
		Symbol: ambErr.Name,
		Hint:   "use [file] <symbol> to disambiguate",
	}
	for _, c := range ambErr.Candidates {
		r.Candidates = append(r.Candidates, ambiguousCandidate{
			File: output.Rel(c.File),
			Line: int(c.StartLine),
			Type: c.Type,
		})
	}
	return r
}
