package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"path/filepath"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(mcpCmd)
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Run as an MCP (Model Context Protocol) server over stdio",
	Long: `Starts edr as a long-running MCP server that communicates via
JSON-RPC 2.0 over stdin/stdout. Exposes typed tools for file operations.
The index database stays open across calls.`,
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
	Required   []string           `json:"required,omitempty"`
}

type mcpProp struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	// For array items
	Items *mcpPropItems `json:"items,omitempty"`
}

type mcpPropItems struct {
	Type        string             `json:"type"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]mcpProp `json:"properties,omitempty"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Tool definitions ---

func mcpTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "edr",
			Description: ToolDesc["do"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"reads": {Type: "array", Description: MP("reads"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"file":       {Type: "string"},
						"symbol":     {Type: "string"},
						"budget":     {Type: "integer", Description: MP("budget")},
						"signatures": {Type: "boolean", Description: MP("signatures")},
						"depth":      {Type: "integer", Description: MP("depth")},
						"start_line": {Type: "integer"},
						"end_line":   {Type: "integer"},
						"symbols":    {Type: "boolean", Description: MP("symbols")},
						"full":       {Type: "boolean", Description: MP("full")},
					}}},
					"queries": {Type: "array", Description: MP("queries"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"cmd":        {Type: "string"},
						"pattern":    {Type: "string"},
						"symbol":     {Type: "string"},
						"file":       {Type: "string"},
						"budget":     {Type: "integer", Description: MP("budget")},
						"body":       {Type: "boolean", Description: MP("body")},
						"text":       {Type: "boolean", Description: MP("text")},
						"regex":      {Type: "boolean", Description: MP("regex")},
						"include":    {Type: "string", Description: MP("include")},
						"exclude":    {Type: "string", Description: MP("exclude")},
						"context":    {Type: "integer", Description: MP("context")},
						"callers":    {Type: "boolean", Description: MP("callers")},
						"deps":       {Type: "boolean", Description: MP("deps")},
						"gather":     {Type: "boolean", Description: MP("gather")},
						"signatures": {Type: "boolean", Description: MP("signatures")},
						"impact":     {Type: "boolean", Description: MP("impact")},
						"chain":      {Type: "string", Description: MP("chain")},
						"depth":      {Type: "integer", Description: MP("depth")},
						"dir":        {Type: "string", Description: MP("dir")},
						"glob":       {Type: "string", Description: MP("glob")},
						"type":       {Type: "string", Description: MP("type")},
						"grep":       {Type: "string", Description: MP("grep")},
						"locals":     {Type: "boolean", Description: MP("locals")},
						"group":      {Type: "boolean", Description: MP("group")},
					}}},
					"edits": {Type: "array", Description: MP("edits"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"file":         {Type: "string"},
						"old_text":     {Type: "string"},
						"new_text":     {Type: "string"},
						"symbol":       {Type: "string"},
						"start_line":   {Type: "integer"},
						"end_line":     {Type: "integer"},
						"regex":        {Type: "boolean", Description: MP("regex")},
						"all":          {Type: "boolean", Description: MP("all")},
						"move":         {Type: "string", Description: MP("move")},
						"after":        {Type: "string", Description: MP("after")},
						"before":       {Type: "string", Description: MP("before")},
						"expect_hash":  {Type: "string", Description: MP("expect_hash")},
					}}},
					"writes": {Type: "array", Description: MP("writes"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"file":    {Type: "string"},
						"content": {Type: "string"},
						"mkdir":   {Type: "boolean", Description: MP("mkdir")},
						"after":   {Type: "string", Description: MP("after")},
						"inside":  {Type: "string", Description: MP("inside")},
						"append":  {Type: "boolean", Description: MP("append")},
					}}},
					"renames": {Type: "array", Description: MP("renames"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"old_name": {Type: "string"},
						"new_name": {Type: "string"},
						"dry_run":  {Type: "boolean", Description: MP("dry_run")},
						"scope":    {Type: "string", Description: MP("scope")},
					}}},
					"budget":          {Type: "integer", Description: MP("budget")},
					"dry_run":         {Type: "boolean", Description: MP("dry_run")},
					"verify":          {Description: MP("verify")},
					"init":            {Type: "boolean", Description: MP("init_flag")},
					"read_after_edit": {Type: "boolean", Description: MP("read_after_edit")},
				},
			},
		},
	}
}


// --- Tool routing ---

// routeTool converts typed tool params into the (cmd, args, flags) tuple for dispatch.
func routeTool(toolName string, raw json.RawMessage) (cmd string, args []string, flags map[string]any, err error) {
	flags = map[string]any{}
	args = []string{}

	switch toolName {
	case "edr":
		// Handled specially — see handleDo()
		cmd = "do"

	default:
		err = fmt.Errorf("unknown tool: %s", toolName)
	}
	return
}

// doParams holds the parsed params for edr_do.
type doParams struct {
	Reads         []doRead   `json:"reads"`
	Queries       []doQuery  `json:"queries"`
	Edits         []doEdit   `json:"edits"`
	Writes        []doWrite  `json:"writes"`
	Renames       []doRename `json:"renames"`
	Budget        *int       `json:"budget"`
	DryRun        *bool      `json:"dry_run"`
	Verify        any        `json:"verify"`
	Init          *bool      `json:"init"`
	ReadAfterEdit *bool      `json:"read_after_edit,omitempty"`
}

type doRead struct {
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	Budget     *int   `json:"budget,omitempty"`
	Signatures *bool  `json:"signatures,omitempty"`
	Depth      *int   `json:"depth,omitempty"`
	StartLine  *int   `json:"start_line,omitempty"`
	EndLine    *int   `json:"end_line,omitempty"`
	Symbols    *bool  `json:"symbols,omitempty"`
	Full       *bool  `json:"full,omitempty"`
}

// doQuery is a generalized read-only command for use in edr_do.
// Cmd selects the operation: search, explore, refs, map, find, diff, read (default).
type doQuery struct {
	Cmd string `json:"cmd"` // search, explore, refs, map, find, diff, read

	// Shared
	Budget *int    `json:"budget,omitempty"`
	File   *string `json:"file,omitempty"`
	Symbol *string `json:"symbol,omitempty"`

	// search
	Pattern *string `json:"pattern,omitempty"`
	Body    *bool   `json:"body,omitempty"`
	Text    *bool   `json:"text,omitempty"`
	Regex   *bool   `json:"regex,omitempty"`
	Include any     `json:"include,omitempty"`
	Exclude any     `json:"exclude,omitempty"`
	Context *int    `json:"context,omitempty"`
	Group   *bool   `json:"group,omitempty"`

	// explore
	Callers    *bool `json:"callers,omitempty"`
	Deps       *bool `json:"deps,omitempty"`
	Gather     *bool `json:"gather,omitempty"`
	Signatures *bool `json:"signatures,omitempty"`

	// refs
	Impact *bool   `json:"impact,omitempty"`
	Chain  *string `json:"chain,omitempty"`
	Depth  *int    `json:"depth,omitempty"`

	// map
	Dir    *string `json:"dir,omitempty"`
	Glob   *string `json:"glob,omitempty"`
	Type   *string `json:"type,omitempty"`
	Grep   *string `json:"grep,omitempty"`
	Locals *bool   `json:"locals,omitempty"`
}

type doEdit struct {
	File       string `json:"file"`
	OldText    string `json:"old_text,omitempty"`
	NewText    string `json:"new_text,omitempty"`
	Symbol     string `json:"symbol,omitempty"`
	StartLine  *int   `json:"start_line,omitempty"`
	EndLine    *int   `json:"end_line,omitempty"`
	Regex      *bool  `json:"regex,omitempty"`
	All        *bool  `json:"all,omitempty"`
	Move       string `json:"move,omitempty"`
	After      string `json:"after,omitempty"`
	Before     string `json:"before,omitempty"`
	DryRun     *bool  `json:"dry_run,omitempty"`
	ExpectHash string `json:"expect_hash,omitempty"`
}

type doWrite struct {
	File    string  `json:"file"`
	Content string  `json:"content"`
	Mkdir   *bool   `json:"mkdir,omitempty"`
	After   *string `json:"after,omitempty"`
	Inside  *string `json:"inside,omitempty"`
	Append  *bool   `json:"append,omitempty"`
}

type doRename struct {
	OldName string  `json:"old_name"`
	NewName string  `json:"new_name"`
	DryRun  *bool   `json:"dry_run,omitempty"`
	Scope   *string `json:"scope,omitempty"`
}

// doKnownKeys are the valid top-level keys for edr_do params.
var doKnownKeys = map[string]bool{
	"reads": true, "queries": true, "edits": true, "writes": true,
	"renames": true, "budget": true, "dry_run": true, "verify": true,
	"init": true, "read_after_edit": true,
}

// Known fields for sub-objects, used to warn on typos like "bodies" instead of "body".
var doQueryKnownKeys = map[string]bool{
	"cmd": true, "budget": true, "file": true, "symbol": true,
	"pattern": true, "body": true, "text": true, "regex": true,
	"include": true, "exclude": true, "context": true, "group": true,
	"callers": true, "deps": true, "gather": true, "signatures": true,
	"impact": true, "chain": true, "depth": true,
	"dir": true, "glob": true, "type": true, "grep": true, "locals": true,
}

var doEditKnownKeys = map[string]bool{
	"file": true, "old_text": true, "new_text": true, "symbol": true,
	"start_line": true, "end_line": true, "regex": true, "all": true,
	"move": true, "after": true, "before": true, "dry_run": true,
	"expect_hash": true,
}

var doWriteKnownKeys = map[string]bool{
	"file": true, "content": true, "mkdir": true, "after": true,
	"inside": true, "append": true, "new_text": true, "body": true,
}

var doRenameKnownKeys = map[string]bool{
	"old_name": true, "new_name": true, "dry_run": true, "scope": true,
}

var doReadKnownKeys = map[string]bool{
	"file": true, "symbol": true, "budget": true, "signatures": true,
	"depth": true, "start_line": true, "end_line": true, "symbols": true,
	"full": true,
}

// checkSubObjectFields validates fields in JSON sub-objects and returns warnings.
func checkSubObjectFields(raw json.RawMessage, section string, known map[string]bool) []string {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var warnings []string
	for i, item := range items {
		var m map[string]json.RawMessage
		if json.Unmarshal(item, &m) != nil {
			continue
		}
		for key := range m {
			if !known[key] {
				if suggestion := suggestField(key, known); suggestion != "" {
					warnings = append(warnings, fmt.Sprintf("%s[%d]: unknown field %q ignored (did you mean %q?)", section, i, key, suggestion))
				} else {
					warnings = append(warnings, fmt.Sprintf("%s[%d]: unknown field %q ignored", section, i, key))
				}
			}
		}
	}
	return warnings
}

// handleDo dispatches edr_do (batch reads/queries/edits/writes/renames/verify).
func handleDo(ctx context.Context, db *index.DB, sess *session.Session, tc *trace.Collector, raw json.RawMessage) (string, error) {
	var p doParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}

	// Detect unknown top-level keys and sub-object fields, warn on typos
	var rawMap map[string]json.RawMessage
	var warnings []string
	if json.Unmarshal(raw, &rawMap) == nil {
		for key := range rawMap {
			if !doKnownKeys[key] {
				if suggestion := suggestField(key, doKnownKeys); suggestion != "" {
					warnings = append(warnings, fmt.Sprintf("unknown field %q ignored (did you mean %q?)", key, suggestion))
				} else {
					warnings = append(warnings, fmt.Sprintf("unknown field %q ignored", key))
				}
			}
		}
		if rd, ok := rawMap["reads"]; ok {
			warnings = append(warnings, checkSubObjectFields(rd, "reads", doReadKnownKeys)...)
		}
		if q, ok := rawMap["queries"]; ok {
			warnings = append(warnings, checkSubObjectFields(q, "queries", doQueryKnownKeys)...)
		}
		if e, ok := rawMap["edits"]; ok {
			warnings = append(warnings, checkSubObjectFields(e, "edits", doEditKnownKeys)...)
		}
		if w, ok := rawMap["writes"]; ok {
			warnings = append(warnings, checkSubObjectFields(w, "writes", doWriteKnownKeys)...)
		}
		if r, ok := rawMap["renames"]; ok {
			warnings = append(warnings, checkSubObjectFields(r, "renames", doRenameKnownKeys)...)
		}
	}

	hasInit := p.Init != nil && *p.Init
	hasReads := len(p.Reads) > 0
	hasQueries := len(p.Queries) > 0
	hasEdits := len(p.Edits) > 0
	hasWrites := len(p.Writes) > 0
	hasRenames := len(p.Renames) > 0
	hasVerify := p.Verify != nil && p.Verify != false

	// Trace: begin call and defer finish
	sess.ResetStats()
	cb := tc.BeginCall()
	cb.SetRequest(len(p.Reads), len(p.Queries), len(p.Edits), len(p.Writes), len(p.Renames), hasVerify, hasInit, p.Budget)

	// Warn if reads and mutations target the same file(s)
	if hasReads && (hasEdits || hasWrites) {
		readFiles := make(map[string]bool)
		for _, r := range p.Reads {
			readFiles[r.File] = true
		}
		for _, e := range p.Edits {
			if readFiles[e.File] {
				warnings = append(warnings, "reads and edits target the same file; reads reflect pre-edit state")
				break
			}
		}
		for _, w := range p.Writes {
			if readFiles[w.File] {
				warnings = append(warnings, "reads and writes target the same file; reads reflect pre-write state")
				break
			}
		}
	}

	if !hasInit && !hasReads && !hasQueries && !hasEdits && !hasWrites && !hasRenames && !hasVerify {
		errMsg := `"error":"edr_do requires at least one of: reads, queries, edits, writes, renames, verify, init"`
		if len(warnings) > 0 {
			warnJSON, _ := json.Marshal(warnings)
			return fmt.Sprintf(`{%s,"warnings":%s}`, errMsg, warnJSON), nil
		}
		return fmt.Sprintf(`{%s}`, errMsg), nil
	}

	var parts []string

	// 0. Force re-index if requested
	if hasInit {
		sess.InvalidateForEdit("init", []string{})
		if err := db.WithWriteLock(func() error {
			_, _, err := index.IndexRepo(ctx, db)
			return err
		}); err != nil {
			parts = append(parts, fmt.Sprintf(`"init":{"ok":false,"error":%q}`, err.Error()))
		} else {
			parts = append(parts, fmt.Sprintf(`"init":{"ok":true,"version":%q}`, Version+"+"+BuildHash))
		}
	}

	// 1. Dispatch reads via DispatchMulti
	if hasReads {
		cmds := make([]dispatch.MultiCmd, len(p.Reads))
		for i, r := range p.Reads {
			readArgs := []string{r.File}
			if r.Symbol != "" {
				readArgs = []string{r.File + ":" + r.Symbol}
			}
			// If start_line + end_line provided for a single file read, fold into args
			if r.StartLine != nil && r.EndLine != nil && r.Symbol == "" {
				readArgs = []string{r.File, strconv.Itoa(*r.StartLine), strconv.Itoa(*r.EndLine)}
			}
			readFlags := map[string]any{}
			if r.Budget != nil {
				readFlags["budget"] = *r.Budget
			}
			if r.Signatures != nil && *r.Signatures {
				readFlags["signatures"] = true
			}
			if r.Depth != nil {
				readFlags["depth"] = *r.Depth
			}
			if r.StartLine != nil && r.EndLine == nil {
				readFlags["start_line"] = *r.StartLine
			}
			if r.EndLine != nil && r.StartLine == nil {
				readFlags["end_line"] = *r.EndLine
			}
			if r.Symbols != nil && *r.Symbols {
				readFlags["symbols"] = true
			}
			if r.Full != nil && *r.Full {
				readFlags["full"] = true
			}
			cmds[i] = dispatch.MultiCmd{Cmd: "read", Args: readArgs, Flags: readFlags}
		}
		var budgetOpt []int
		if p.Budget != nil {
			budgetOpt = []int{*p.Budget}
		}
		results := dispatch.DispatchMulti(ctx, db, cmds, budgetOpt...)
		text := postProcessMultiResults(sess, cmds, results)
		parts = append(parts, fmt.Sprintf(`"reads":%s`, text))
	}

	// 2. Dispatch generalized queries via DispatchMulti (+ diff queries inline)
	if hasQueries {
		// Distribute top-level budget to queries that lack individual budgets
		if p.Budget != nil && len(p.Queries) > 0 {
			perQuery := *p.Budget / len(p.Queries)
			if perQuery < 50 {
				perQuery = 50
			}
			for i := range p.Queries {
				if p.Queries[i].Budget == nil {
					b := perQuery
					p.Queries[i].Budget = &b
				}
			}
		}

		// Partition into dispatch queries and diff queries
		type indexedResult struct {
			idx    int
			result dispatch.MultiResult
		}
		var dispatchIdxs []int
		var diffIdxs []int
		var errorIdxs []int
		inferredCmds := make(map[int]string) // index → inferred cmd name
		for i, q := range p.Queries {
			if q.Cmd == "diff" {
				diffIdxs = append(diffIdxs, i)
			} else {
				mc, wasInferred := doQueryToMultiCmd(q)
				if mc.Cmd == "" {
					errorIdxs = append(errorIdxs, i)
				} else {
					if wasInferred {
						inferredCmds[i] = mc.Cmd
					}
					dispatchIdxs = append(dispatchIdxs, i)
				}
			}
		}

		// Build ordered results array
		allResults := make([]dispatch.MultiResult, len(p.Queries))

		// Fill in errors for queries missing cmd
		for _, qi := range errorIdxs {
			allResults[qi] = dispatch.MultiResult{
				Cmd:   "",
				OK:    false,
				Error: `query requires a "cmd" field (search, map, explore, refs, find, diff)`,
			}
		}

		// Dispatch regular queries
		if len(dispatchIdxs) > 0 {
			cmds := make([]dispatch.MultiCmd, len(dispatchIdxs))
			for ci, qi := range dispatchIdxs {
				cmds[ci], _ = doQueryToMultiCmd(p.Queries[qi])
			}
			var budgetOpt []int
			if p.Budget != nil {
				budgetOpt = []int{*p.Budget}
			}
			results := dispatch.DispatchMulti(ctx, db, cmds, budgetOpt...)
			for ci, qi := range dispatchIdxs {
				allResults[qi] = results[ci]
			}
		}

		// Handle diff queries via session
		for _, qi := range diffIdxs {
			q := p.Queries[qi]
			var diffArgs []string
			if q.File != nil {
				diffArgs = append(diffArgs, *q.File)
			}
			if q.Symbol != nil && *q.Symbol != "" {
				diffArgs = append(diffArgs, *q.Symbol)
			}
			diffResult := sess.GetDiff(diffArgs)
			allResults[qi] = dispatch.MultiResult{
				Cmd:    "diff",
				OK:     diffResult["error"] == nil,
				Result: diffResult,
			}
			if errMsg, ok := diffResult["error"].(string); ok {
				allResults[qi].Error = errMsg
			}
		}

		// Build cmds array for post-processing (need full list for session)
		allCmds := make([]dispatch.MultiCmd, len(p.Queries))
		for i, q := range p.Queries {
			if q.Cmd == "diff" {
				allCmds[i] = dispatch.MultiCmd{Cmd: "diff"}
			} else {
				allCmds[i], _ = doQueryToMultiCmd(q)
			}
		}

		// Annotate inferred_cmd on results where cmd was auto-inferred
		for qi, inferredCmd := range inferredCmds {
			allResults[qi].InferredCmd = inferredCmd
		}

		text := postProcessMultiResults(sess, allCmds, allResults)
		parts = append(parts, fmt.Sprintf(`"queries":%s`, text))

		// Trace: record query events
		for _, r := range allResults {
			resultBytes := 0
			if r.Result != nil {
				if d, err := json.Marshal(r.Result); err == nil {
					resultBytes = len(d)
				}
			}
			cb.AddQueryEvent(r.Cmd, r.OK, resultBytes)
		}
	}

	// 3. Dispatch writes sequentially (before renames/edits, so new files can be edited)
	if hasWrites {
		var writeResults []map[string]any
		for _, w := range p.Writes {
			flags := map[string]any{"content": w.Content}
			if w.Mkdir != nil && *w.Mkdir {
				flags["mkdir"] = true
			}
			if w.After != nil && *w.After != "" {
				flags["after"] = *w.After
			}
			if w.Inside != nil && *w.Inside != "" {
				flags["inside"] = *w.Inside
			}
			if w.Append != nil && *w.Append {
				flags["append"] = true
			}
			result, err := dispatch.Dispatch(ctx, db, "write", []string{w.File}, flags)
			if err != nil {
				writeResults = append(writeResults, map[string]any{"file": w.File, "ok": false, "error": err.Error()})
			} else {
				writeResults = append(writeResults, map[string]any{"file": w.File, "ok": true, "result": result})
			}
		}
		data, _ := json.Marshal(writeResults)
		parts = append(parts, fmt.Sprintf(`"writes":%s`, string(data)))
	}

	// 4. Dispatch renames (clears session state)
	if hasRenames {
		var renameResults []map[string]any
		for _, r := range p.Renames {
			sess.InvalidateForEdit("rename", []string{r.OldName, r.NewName})
			renameFlags := map[string]any{}
			if r.DryRun != nil && *r.DryRun {
				renameFlags["dry_run"] = true
			}
			if r.Scope != nil && *r.Scope != "" {
				renameFlags["scope"] = *r.Scope
			}
			result, err := dispatch.Dispatch(ctx, db, "rename", []string{r.OldName, r.NewName}, renameFlags)
			if err != nil {
				renameResults = append(renameResults, map[string]any{
					"old_name": r.OldName, "new_name": r.NewName, "ok": false, "error": err.Error(),
				})
			} else {
				renameResults = append(renameResults, map[string]any{
					"old_name": r.OldName, "new_name": r.NewName, "ok": true, "result": result,
				})
			}
		}
		data, _ := json.Marshal(renameResults)
		parts = append(parts, fmt.Sprintf(`"renames":%s`, string(data)))
	}

	// 5. Dispatch edits via edit-plan (atomic)
	editsFailed := false
	if hasEdits {
		editFlags := map[string]any{}

		// Promote per-edit dry_run to batch level (edits are atomic, so
		// dry_run only makes sense for the whole batch). This prevents
		// agents from accidentally mutating when they intended to preview.
		if p.DryRun == nil || !*p.DryRun {
			for _, e := range p.Edits {
				if e.DryRun != nil && *e.DryRun {
					t := true
					p.DryRun = &t
					warnings = append(warnings, "per-edit dry_run promoted to batch level (edits are atomic)")
					break
				}
			}
		}

		editsRaw := make([]map[string]any, len(p.Edits))
		for i, e := range p.Edits {
			m := map[string]any{"file": e.File}
			if e.OldText != "" {
				m["old_text"] = e.OldText
			}
			// Always include new_text when an edit mode is active.
			// Empty new_text with old_text/symbol/line-range = deletion.
			if e.NewText != "" || e.OldText != "" || e.Symbol != "" || (e.StartLine != nil && e.EndLine != nil) {
				m["new_text"] = e.NewText
			}
			if e.Symbol != "" {
				m["symbol"] = e.Symbol
			}
			if e.StartLine != nil {
				m["start_line"] = *e.StartLine
			}
			if e.EndLine != nil {
				m["end_line"] = *e.EndLine
			}
			if e.Regex != nil && *e.Regex {
				m["regex"] = true
			}
			if e.All != nil && *e.All {
				m["all"] = true
			}
			if e.Move != "" {
				m["move"] = e.Move
			}
			if e.After != "" {
				m["after"] = e.After
			}
			if e.Before != "" {
				m["before"] = e.Before
			}
			if e.ExpectHash != "" {
				m["expect_hash"] = e.ExpectHash
			}
			editsRaw[i] = m
		}
		editFlags["edits"] = editsRaw
		if p.DryRun != nil && *p.DryRun {
			editFlags["dry_run"] = true
		}

		sess.InvalidateForEdit("edit-plan", []string{})

		result, err := dispatch.Dispatch(ctx, db, "edit-plan", []string{}, editFlags)
		if err != nil {
			editsFailed = true
			if ambErr := asAmbiguousError(err); ambErr != nil {
				data, _ := json.Marshal(ambErr)
				parts = append(parts, fmt.Sprintf(`"edits":%s`, string(data)))
			} else {
				parts = append(parts, fmt.Sprintf(`"edits":{"error":%q}`, err.Error()))
			}
			// Trace: record failed edits
			for _, e := range p.Edits {
				cb.AddEditEvent(e.File, 0, "", "", false)
			}
		} else {
			data, _ := json.Marshal(result)
			text := sess.PostProcess("edit-plan", []string{}, editFlags, result, string(data))
			parts = append(parts, fmt.Sprintf(`"edits":%s`, text))

			// Trace: extract per-file edit events from result
			traceEditEvents(cb, result)
		}
	}

	// 5b. Post-edit reads — return edited file contents to save a round trip
	if editsFailed && p.ReadAfterEdit != nil && *p.ReadAfterEdit {
		parts = append(parts, `"post_edit_reads":"skipped: edits failed"`)
	} else if (hasEdits || hasWrites) && p.ReadAfterEdit != nil && *p.ReadAfterEdit && (p.DryRun == nil || !*p.DryRun) {
		editedFiles := make(map[string]bool)
		for _, e := range p.Edits {
			editedFiles[e.File] = true
		}
		for _, w := range p.Writes {
			editedFiles[w.File] = true
		}
		var readCmds []dispatch.MultiCmd
		for f := range editedFiles {
			readCmds = append(readCmds, dispatch.MultiCmd{Cmd: "read", Args: []string{f}, Flags: map[string]any{"full": true}})
		}
		if len(readCmds) > 0 {
			var budgetOpt []int
			if p.Budget != nil {
				budgetOpt = []int{*p.Budget}
			}
			results := dispatch.DispatchMulti(ctx, db, readCmds, budgetOpt...)
			text := postProcessMultiResults(sess, readCmds, results)
			parts = append(parts, fmt.Sprintf(`"post_edit_reads":%s`, text))
		}
	}

	// 6. Run verification if requested
	if editsFailed && hasVerify {
		parts = append(parts, `"verify":"skipped: edits failed"`)
	} else if hasVerify {
		verifyFlags := map[string]any{}
		// verify: true uses auto-detect; verify: "test"/"build" sets level; verify: "command" uses custom command
		if cmd, ok := p.Verify.(string); ok && cmd != "" {
			if cmd == "test" || cmd == "build" {
				verifyFlags["level"] = cmd
			} else {
				verifyFlags["command"] = cmd
			}
		}
		result, err := dispatch.Dispatch(ctx, db, "verify", []string{}, verifyFlags)
		if err != nil {
			parts = append(parts, fmt.Sprintf(`"verify":{"error":%q}`, err.Error()))
			cb.AddVerifyEvent("", false, 0, 0)
		} else {
			data, _ := json.Marshal(result)
			parts = append(parts, fmt.Sprintf(`"verify":%s`, string(data)))

			// Trace: extract verify event
			traceVerifyEvent(cb, result)
		}
	}

	if len(warnings) > 0 {
		warnJSON, _ := json.Marshal(warnings)
		parts = append(parts, fmt.Sprintf(`"warnings":%s`, warnJSON))
	}

	result := "{" + strings.Join(parts, ",") + "}"

	// Hard cap: if response exceeds 100KB, drop sections from the end until
	// it fits. This guarantees valid JSON unlike raw byte slicing.
	const maxResponseBytes = 100_000
	wasTruncated := false
	if len(result) > maxResponseBytes {
		wasTruncated = true
		fullSize := len(result)
		var dropped []string
		// Drop sections from the end (last added = least critical) until it fits.
		// Keep at least one section to return something useful.
		for len(parts) > 1 {
			// Extract the key name from the last part (format: "key":value)
			last := parts[len(parts)-1]
			keyEnd := strings.Index(last, "\":")
			key := "unknown"
			if keyEnd > 0 && last[0] == '"' {
				key = last[1:keyEnd]
			}
			dropped = append(dropped, key)
			parts = parts[:len(parts)-1]
			candidate := "{" + strings.Join(parts, ",") + "}"
			if len(candidate) <= maxResponseBytes-200 { // leave room for metadata
				break
			}
		}
		droppedJSON, _ := json.Marshal(dropped)
		meta := fmt.Sprintf(`"truncated":true,"truncated_reason":"response exceeded %d bytes (%d actual)","sections_dropped":%s`,
			maxResponseBytes, fullSize, droppedJSON)
		result = "{" + meta + "," + strings.Join(parts, ",") + "}"
	}

	// Trace: record session stats and finish
	dr, bd, se := sess.GetStats()
	cb.SetSessionStats(dr, bd, se)
	cb.Finish(len(result), wasTruncated, len(warnings))

	return result, nil
}

// traceEditEvents extracts per-file edit results and records them on the CallBuilder.
func traceEditEvents(cb *trace.CallBuilder, result any) {
	if cb == nil {
		return
	}
	m, ok := result.(map[string]any)
	if !ok {
		return
	}

	editOK := true
	if ok, exists := m["ok"].(bool); exists {
		editOK = ok
	}

	linesChanged := 0
	if lc, ok := m["lines_changed"].(float64); ok {
		linesChanged = int(lc)
	}

	// edit-plan results have a "hashes" map: {file: hash}
	if hashes, ok := m["hashes"].(map[string]any); ok {
		for file, hash := range hashes {
			hashStr, _ := hash.(string)
			cb.AddEditEvent(file, linesChanged, "", hashStr, editOK)
		}
	} else if file, ok := m["file"].(string); ok {
		hash, _ := m["hash"].(string)
		cb.AddEditEvent(file, linesChanged, "", hash, editOK)
	}
}

// traceVerifyEvent extracts verify result and records it on the CallBuilder.
func traceVerifyEvent(cb *trace.CallBuilder, result any) {
	if cb == nil {
		return
	}
	m, ok := result.(map[string]any)
	if !ok {
		return
	}
	command, _ := m["command"].(string)
	verifyOK := true
	if ok, exists := m["ok"].(bool); exists {
		verifyOK = ok
	}
	durationMs := 0
	if d, ok := m["duration_ms"].(float64); ok {
		durationMs = int(d)
	}
	outputBytes := 0
	if o, ok := m["output"].(string); ok {
		outputBytes = len(o)
	}
	cb.AddVerifyEvent(command, verifyOK, durationMs, outputBytes)
}

// doQueryToMultiCmd converts a generalized doQuery into a dispatch.MultiCmd.
// inferQueryCmd guesses the intended cmd from populated fields when cmd is omitted.
func inferQueryCmd(q doQuery) string {
	if q.Pattern != nil && *q.Pattern != "" {
		return "search"
	}
	if (q.Callers != nil && *q.Callers) || (q.Deps != nil && *q.Deps) || (q.Gather != nil && *q.Gather) {
		return "explore"
	}
	if q.Impact != nil && *q.Impact {
		return "refs"
	}
	if q.Chain != nil && *q.Chain != "" {
		return "refs"
	}
	if (q.Dir != nil && *q.Dir != "") || (q.Grep != nil && *q.Grep != "") || (q.Locals != nil && *q.Locals) {
		return "map"
	}
	if q.File != nil && *q.File != "" {
		return "read"
	}
	if q.Symbol != nil && *q.Symbol != "" {
		return "explore"
	}
	return "" // will be caught as error below
}

// doQueryToMultiCmd converts a doQuery to a dispatch.MultiCmd.
// Returns the MultiCmd and whether the cmd was inferred (not explicitly set).
func doQueryToMultiCmd(q doQuery) (dispatch.MultiCmd, bool) {
	cmd := q.Cmd
	inferred := false
	if cmd == "" {
		cmd = inferQueryCmd(q)
		inferred = true
	}

	// When cmd is inferred and no budget is set, apply a default cap
	if inferred && q.Budget == nil {
		defaultBudget := 200
		q.Budget = &defaultBudget
	}

	args := []string{}
	flags := map[string]any{}

	if q.Budget != nil {
		flags["budget"] = *q.Budget
	}

	switch cmd {
	case "read":
		if q.File != nil {
			f := *q.File
			if q.Symbol != nil && *q.Symbol != "" {
				f += ":" + *q.Symbol
			}
			args = []string{f}
		}
		if q.Signatures != nil && *q.Signatures {
			flags["signatures"] = true
		}
		if q.Depth != nil {
			flags["depth"] = *q.Depth
		}

	case "search":
		if q.Pattern == nil || *q.Pattern == "" {
			return dispatch.MultiCmd{Cmd: "search", Args: []string{}, Flags: flags}, inferred
		}
		args = []string{*q.Pattern}
		if q.Body != nil && *q.Body {
			flags["body"] = true
		}
		if q.Text != nil && *q.Text {
			flags["text"] = true
		}
		if q.Regex != nil && *q.Regex {
			flags["regex"] = true
		}
		if q.Include != nil {
			flags["include"] = q.Include
		}
		if q.Exclude != nil {
			flags["exclude"] = q.Exclude
		}
		if q.Context != nil {
			flags["context"] = *q.Context
		}
		// Default group=true for text search via MCP (saves tokens by grouping matches by file)
		isTextSearch := (q.Text != nil && *q.Text) ||
			(q.Regex != nil && *q.Regex) ||
			q.Include != nil || q.Exclude != nil ||
			(q.Context != nil && *q.Context > 0)
		if q.Group != nil && *q.Group {
			flags["group"] = true
		} else if isTextSearch && q.Group == nil {
			flags["group"] = true
		}

	case "explore":
		if q.Symbol != nil {
			args = []string{*q.Symbol}
		}
		if q.File != nil && *q.File != "" {
			args = append([]string{*q.File}, args...)
		}
		if q.Body != nil && *q.Body {
			flags["body"] = true
		}
		if q.Callers != nil && *q.Callers {
			flags["callers"] = true
		}
		if q.Deps != nil && *q.Deps {
			flags["deps"] = true
		}
		if q.Gather != nil && *q.Gather {
			flags["gather"] = true
		}
		if q.Signatures != nil && *q.Signatures {
			flags["signatures"] = true
		}

	case "refs":
		if q.Symbol != nil {
			args = []string{*q.Symbol}
		}
		if q.File != nil && *q.File != "" {
			args = append([]string{*q.File}, args...)
		}
		if q.Impact != nil && *q.Impact {
			flags["impact"] = true
		}
		if q.Chain != nil && *q.Chain != "" {
			flags["chain"] = *q.Chain
		}
		if q.Depth != nil {
			flags["depth"] = *q.Depth
		}

	case "map":
		if q.File != nil && *q.File != "" {
			args = []string{*q.File}
		}
		if q.Dir != nil && *q.Dir != "" {
			flags["dir"] = *q.Dir
		}
		if q.Glob != nil && *q.Glob != "" {
			flags["glob"] = *q.Glob
		}
		if q.Type != nil && *q.Type != "" {
			flags["type"] = *q.Type
		}
		if q.Grep != nil && *q.Grep != "" {
			flags["grep"] = *q.Grep
		}
		if q.Locals != nil && *q.Locals {
			flags["locals"] = true
		}

	case "find":
		if q.Pattern != nil {
			args = []string{*q.Pattern}
		}
		if q.Dir != nil && *q.Dir != "" {
			flags["dir"] = *q.Dir
		}
	}

	return dispatch.MultiCmd{Cmd: cmd, Args: args, Flags: flags}, inferred
}



// --- Post-process multi results ---

// postProcessMultiResults applies session post-processing to each sub-result.
func postProcessMultiResults(sess *session.Session, cmds []dispatch.MultiCmd, results []dispatch.MultiResult) string {
	type processedResult struct {
		Cmd         string `json:"cmd"`
		OK          bool   `json:"ok"`
		InferredCmd string `json:"inferred_cmd,omitempty"`
		Result      any    `json:"result,omitempty"`
		Error       string `json:"error,omitempty"`
	}

	processed := make([]processedResult, len(results))
	for i, r := range results {
		processed[i] = processedResult{Cmd: r.Cmd, OK: r.OK, Error: r.Error, InferredCmd: r.InferredCmd}
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

	// Start trace collector (non-fatal if it fails)
	tc := trace.NewCollector(filepath.Join(db.Root(), ".edr"), Version+"+"+BuildHash)
	defer func() {
		if tc != nil {
			tc.Close()
		}
	}()

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
			enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &jsonRPCError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		switch req.Method {
		case "initialize":
			// Include binary start time so clients can detect stale servers after rebuild
			serverVersion := Version + "+" + BuildHash
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
						Version: serverVersion,
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
					"tools": mcpTools(),
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

			cmd, cmdArgs, flags, err := routeTool(params.Name, params.Arguments)
			if err != nil {
				enc.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &jsonRPCError{Code: -32602, Message: err.Error()},
				})
				continue
			}

			// Check for stale files before dispatching.
			// Re-check inside the lock to avoid duplicate re-indexing when
			// concurrent requests both see stale before either acquires the lock.
			if stale, _ := index.HasStaleFiles(ctx, db); stale {
				if err := db.WithWriteLock(func() error {
					if still, _ := index.HasStaleFiles(ctx, db); !still {
						return nil // another goroutine already re-indexed
					}
					_, _, err := index.IndexRepo(ctx, db)
					return err
				}); err != nil {
					fmt.Fprintf(os.Stderr, "edr: re-index failed: %v\n", err)
				}
			}

			var text string

			// Handle edr_do specially
			if cmd == "do" {
				doText, doErr := handleDo(ctx, db, sess, tc, params.Arguments)
				if doErr != nil {
					text = fmt.Sprintf(`{"error": %q}`, doErr.Error())
				} else {
					text = doText
				}
			} else {
				result, err := dispatch.Dispatch(ctx, db, cmd, cmdArgs, flags)
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
					text = sess.PostProcess(cmd, cmdArgs, flags, result, text)

					// Working-set dedup for read commands (after post-processing)
					if session.ReadCommands[cmd] {
						key := sess.CacheKey(cmd, cmdArgs, flags)
						if sess.Check(key, text) {
							text = fmt.Sprintf(`{"cached":true,"message":"identical to previous response for %s %s"}`, cmd, strings.Join(cmdArgs, " "))
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
		Hint:   "use file param to disambiguate",
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

// suggestField finds the closest known field name by Levenshtein distance.
func suggestField(input string, known map[string]bool) string {
	best := ""
	bestDist := 3 // only suggest if distance <= 2
	for key := range known {
		d := fieldLevenshtein(input, key)
		if d < bestDist {
			bestDist = d
			best = key
		}
	}
	return best
}

func fieldLevenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			ins := curr[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			m := ins
			if del < m {
				m = del
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
