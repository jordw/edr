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
			Name:        "edr_do",
			Description: ToolDesc["do"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"reads": {Type: "array", Description: P("reads"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"file":       {Type: "string", Description: "File path (required)"},
						"symbol":     {Type: "string", Description: P("symbol")},
						"budget":     {Type: "integer", Description: P("budget")},
						"signatures": {Type: "boolean", Description: P("signatures")},
						"depth":      {Type: "integer", Description: P("depth")},
					}}},
					"queries": {Type: "array", Description: P("queries"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"cmd":        {Type: "string", Description: "Command: search, explore, refs, map, find, diff"},
						"pattern":    {Type: "string", Description: P("pattern")},
						"symbol":     {Type: "string", Description: P("symbol")},
						"file":       {Type: "string", Description: P("file")},
						"budget":     {Type: "integer", Description: P("budget")},
						"body":       {Type: "boolean", Description: P("body")},
						"text":       {Type: "boolean", Description: P("text")},
						"regex":      {Type: "boolean", Description: P("regex")},
						"include":    {Type: "string", Description: P("include")},
						"exclude":    {Type: "string", Description: P("exclude")},
						"context":    {Type: "integer", Description: P("context")},
						"callers":    {Type: "boolean", Description: P("callers")},
						"deps":       {Type: "boolean", Description: P("deps")},
						"gather":     {Type: "boolean", Description: P("gather")},
						"signatures": {Type: "boolean", Description: P("signatures")},
						"impact":     {Type: "boolean", Description: P("impact")},
						"chain":      {Type: "string", Description: P("chain")},
						"depth":      {Type: "integer", Description: P("depth")},
						"dir":        {Type: "string", Description: P("dir")},
						"glob":       {Type: "string", Description: P("glob")},
						"type":       {Type: "string", Description: P("type")},
						"grep":       {Type: "string", Description: P("grep")},
						"locals":     {Type: "boolean", Description: P("locals")},
					}}},
					"edits": {Type: "array", Description: P("edits"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"file":       {Type: "string", Description: "File path (required)"},
						"old_text":   {Type: "string", Description: P("old_text")},
						"new_text":   {Type: "string", Description: P("new_text")},
						"symbol":     {Type: "string", Description: P("symbol")},
						"start_line": {Type: "integer", Description: P("start_line")},
						"end_line":   {Type: "integer", Description: P("end_line")},
						"regex":      {Type: "boolean", Description: P("regex")},
						"all":        {Type: "boolean", Description: P("all")},
					}}},
					"writes": {Type: "array", Description: P("writes"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"file":    {Type: "string", Description: "File path (required)"},
						"content": {Type: "string", Description: P("content")},
						"mkdir":   {Type: "boolean", Description: P("mkdir")},
						"after":   {Type: "string", Description: P("after")},
						"inside":  {Type: "string", Description: P("inside")},
						"append":  {Type: "boolean", Description: P("append")},
					}}},
					"renames": {Type: "array", Description: P("renames"), Items: &mcpPropItems{Type: "object", Properties: map[string]mcpProp{
						"old_name": {Type: "string", Description: P("old_name")},
						"new_name": {Type: "string", Description: P("new_name")},
						"dry_run":  {Type: "boolean", Description: P("dry_run")},
						"scope":    {Type: "string", Description: P("scope")},
					}}},
					"budget":  {Type: "integer", Description: P("budget")},
					"dry_run": {Type: "boolean", Description: P("dry_run")},
					"verify":  {Description: P("verify")},
					"init":    {Type: "boolean", Description: P("init_flag")},
				},
			},
		},
		{
			Name:        "edr_read",
			Description: ToolDesc["read"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"files":      {Type: "array", Description: P("files"), Items: &mcpPropItems{Type: "string"}},
					"budget":     {Type: "integer", Description: P("budget")},
					"start_line": {Type: "integer", Description: P("start_line")},
					"end_line":   {Type: "integer", Description: P("end_line")},
					"symbols":    {Type: "boolean", Description: P("symbols")},
					"signatures": {Type: "boolean", Description: P("signatures")},
					"depth":      {Type: "integer", Description: P("depth")},
					"full":       {Type: "boolean", Description: P("full")},
				},
				Required: []string{"files"},
			},
		},
		{
			Name:        "edr_map",
			Description: ToolDesc["map"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"file":   {Type: "string", Description: P("file")},
					"budget": {Type: "integer", Description: P("budget")},
					"dir":    {Type: "string", Description: P("dir")},
					"glob":   {Type: "string", Description: P("glob")},
					"type":   {Type: "string", Description: P("type")},
					"grep":   {Type: "string", Description: P("grep")},
					"locals": {Type: "boolean", Description: P("locals")},
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
	case "edr_read":
		var p struct {
			Files      []string `json:"files"`
			Budget     *int     `json:"budget"`
			StartLine  *int     `json:"start_line"`
			EndLine    *int     `json:"end_line"`
			Symbols    *bool    `json:"symbols"`
			Signatures *bool    `json:"signatures"`
			Depth      *int     `json:"depth"`
			Full       *bool    `json:"full"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "read"
		// If single file with start/end lines, fold into args
		if len(p.Files) == 1 && p.StartLine != nil && p.EndLine != nil {
			args = []string{p.Files[0], strconv.Itoa(*p.StartLine), strconv.Itoa(*p.EndLine)}
		} else {
			args = p.Files
		}
		if p.Budget != nil {
			flags["budget"] = *p.Budget
		}
		if p.StartLine != nil && (len(p.Files) != 1 || p.EndLine == nil) {
			flags["start_line"] = *p.StartLine
		}
		if p.EndLine != nil && (len(p.Files) != 1 || p.StartLine == nil) {
			flags["end_line"] = *p.EndLine
		}
		if p.Symbols != nil && *p.Symbols {
			flags["symbols"] = true
		}
		if p.Signatures != nil && *p.Signatures {
			flags["signatures"] = true
		}
		if p.Depth != nil {
			flags["depth"] = *p.Depth
		}
		if p.Full != nil && *p.Full {
			flags["full"] = true
		}

	case "edr_map":
		var p struct {
			File   *string `json:"file"`
			Budget *int    `json:"budget"`
			Dir    *string `json:"dir"`
			Glob   *string `json:"glob"`
			Type   *string `json:"type"`
			Grep   *string `json:"grep"`
			Locals *bool   `json:"locals"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "map"
		if p.File != nil && *p.File != "" {
			args = []string{*p.File}
		}
		if p.Budget != nil {
			flags["budget"] = *p.Budget
		}
		if p.Dir != nil && *p.Dir != "" {
			flags["dir"] = *p.Dir
		}
		if p.Glob != nil && *p.Glob != "" {
			flags["glob"] = *p.Glob
		}
		if p.Type != nil && *p.Type != "" {
			flags["type"] = *p.Type
		}
		if p.Grep != nil && *p.Grep != "" {
			flags["grep"] = *p.Grep
		}
		if p.Locals != nil && *p.Locals {
			flags["locals"] = true
		}

	case "edr_do":
		// Handled specially — see handleDo()
		cmd = "do"

	default:
		err = fmt.Errorf("unknown tool: %s", toolName)
	}
	return
}

// doParams holds the parsed params for edr_do.
type doParams struct {
	Reads   []doRead   `json:"reads"`
	Queries []doQuery  `json:"queries"`
	Edits   []doEdit   `json:"edits"`
	Writes  []doWrite  `json:"writes"`
	Renames []doRename `json:"renames"`
	Budget  *int       `json:"budget"`
	DryRun  *bool      `json:"dry_run"`
	Verify  any        `json:"verify"`
	Init    *bool      `json:"init"`
}

type doRead struct {
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	Budget     *int   `json:"budget,omitempty"`
	Signatures *bool  `json:"signatures,omitempty"`
	Depth      *int   `json:"depth,omitempty"`
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
	File      string `json:"file"`
	OldText   string `json:"old_text,omitempty"`
	NewText   string `json:"new_text,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	StartLine *int   `json:"start_line,omitempty"`
	EndLine   *int   `json:"end_line,omitempty"`
	Regex     *bool  `json:"regex,omitempty"`
	All       *bool  `json:"all,omitempty"`
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
	"init": true,
}

// handleDo dispatches edr_do (batch reads/queries/edits/writes/renames/verify).
func handleDo(ctx context.Context, db *index.DB, sess *session.Session, raw json.RawMessage) (string, error) {
	var p doParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}

	// Detect unknown top-level keys and warn
	var rawMap map[string]json.RawMessage
	var warnings []string
	if json.Unmarshal(raw, &rawMap) == nil {
		for key := range rawMap {
			if !doKnownKeys[key] {
				warnings = append(warnings, fmt.Sprintf("unknown field %q ignored", key))
			}
		}
	}

	hasInit := p.Init != nil && *p.Init
	hasReads := len(p.Reads) > 0
	hasQueries := len(p.Queries) > 0
	hasEdits := len(p.Edits) > 0
	hasWrites := len(p.Writes) > 0
	hasRenames := len(p.Renames) > 0
	hasVerify := p.Verify != nil && p.Verify != false

	if !hasInit && !hasReads && !hasQueries && !hasEdits && !hasWrites && !hasRenames && !hasVerify {
		return `{"error": "edr_do requires at least one of: reads, queries, edits, writes, renames, verify, init"}`, nil
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
			parts = append(parts, `"init":{"ok":true}`)
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
		// Partition into dispatch queries and diff queries
		type indexedResult struct {
			idx    int
			result dispatch.MultiResult
		}
		var dispatchIdxs []int
		var diffIdxs []int
		for i, q := range p.Queries {
			if q.Cmd == "diff" {
				diffIdxs = append(diffIdxs, i)
			} else {
				dispatchIdxs = append(dispatchIdxs, i)
			}
		}

		// Build ordered results array
		allResults := make([]dispatch.MultiResult, len(p.Queries))

		// Dispatch regular queries
		if len(dispatchIdxs) > 0 {
			cmds := make([]dispatch.MultiCmd, len(dispatchIdxs))
			for ci, qi := range dispatchIdxs {
				cmds[ci] = doQueryToMultiCmd(p.Queries[qi])
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
				allCmds[i] = doQueryToMultiCmd(q)
			}
		}
		text := postProcessMultiResults(sess, allCmds, allResults)
		parts = append(parts, fmt.Sprintf(`"queries":%s`, text))
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
	if hasEdits {
		editFlags := map[string]any{}
		editsRaw := make([]map[string]any, len(p.Edits))
		for i, e := range p.Edits {
			m := map[string]any{"file": e.File}
			if e.OldText != "" {
				m["old_text"] = e.OldText
			}
			if e.NewText != "" {
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
			editsRaw[i] = m
		}
		editFlags["edits"] = editsRaw
		if p.DryRun != nil && *p.DryRun {
			editFlags["dry_run"] = true
		}

		sess.InvalidateForEdit("edit-plan", []string{})

		result, err := dispatch.Dispatch(ctx, db, "edit-plan", []string{}, editFlags)
		if err != nil {
			if ambErr := asAmbiguousError(err); ambErr != nil {
				data, _ := json.Marshal(ambErr)
				parts = append(parts, fmt.Sprintf(`"edits":%s`, string(data)))
			} else {
				parts = append(parts, fmt.Sprintf(`"edits":{"error":%q}`, err.Error()))
			}
		} else {
			data, _ := json.Marshal(result)
			text := sess.PostProcess("edit-plan", []string{}, editFlags, result, string(data))
			parts = append(parts, fmt.Sprintf(`"edits":%s`, text))
		}
	}

	// 6. Run verification if requested
	if hasVerify {
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
		} else {
			data, _ := json.Marshal(result)
			parts = append(parts, fmt.Sprintf(`"verify":%s`, string(data)))
		}
	}

	if len(warnings) > 0 {
		warnJSON, _ := json.Marshal(warnings)
		parts = append(parts, fmt.Sprintf(`"warnings":%s`, warnJSON))
	}

	return "{" + strings.Join(parts, ",") + "}", nil
}


// doQueryToMultiCmd converts a generalized doQuery into a dispatch.MultiCmd.
func doQueryToMultiCmd(q doQuery) dispatch.MultiCmd {
	cmd := q.Cmd
	if cmd == "" {
		cmd = "read"
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
		if q.Pattern != nil {
			args = []string{*q.Pattern}
		}
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

	return dispatch.MultiCmd{Cmd: cmd, Args: args, Flags: flags}
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
						Version: Version + "+" + BuildHash,
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

			// Handle edr_do specially
			if cmd == "do" {
				doText, doErr := handleDo(ctx, db, sess, params.Arguments)
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
