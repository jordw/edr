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
	Type string `json:"type"`
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
			Name:        "edr_plan",
			Description: ToolDesc["plan"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"reads":   {Type: "array", Description: P("reads"), Items: &mcpPropItems{Type: "object"}},
					"queries": {Type: "array", Description: P("queries"), Items: &mcpPropItems{Type: "object"}},
					"edits":   {Type: "array", Description: P("edits"), Items: &mcpPropItems{Type: "object"}},
					"writes":  {Type: "array", Description: P("writes"), Items: &mcpPropItems{Type: "object"}},
					"budget":  {Type: "integer", Description: P("budget")},
					"dry_run": {Type: "boolean", Description: P("dry_run")},
					"verify":  {Description: P("verify")},
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
			Name:        "edr_edit",
			Description: ToolDesc["edit"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"file":       {Type: "string", Description: P("file")},
					"new_text":   {Type: "string", Description: P("new_text")},
					"old_text":   {Type: "string", Description: P("old_text")},
					"symbol":     {Type: "string", Description: P("symbol")},
					"start_line": {Type: "integer", Description: P("start_line")},
					"end_line":   {Type: "integer", Description: P("end_line")},
					"regex":      {Type: "boolean", Description: P("regex")},
					"all":        {Type: "boolean", Description: P("all")},
					"dry_run":    {Type: "boolean", Description: P("dry_run")},
				},
				Required: []string{"file", "new_text"},
			},
		},
		{
			Name:        "edr_write",
			Description: ToolDesc["write"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"file":    {Type: "string", Description: P("file")},
					"content": {Type: "string", Description: P("content")},
					"mkdir":   {Type: "boolean", Description: P("mkdir")},
					"append":  {Type: "boolean", Description: P("append")},
					"after":   {Type: "string", Description: P("after")},
					"inside":  {Type: "string", Description: P("inside")},
				},
				Required: []string{"file", "content"},
			},
		},
		{
			Name:        "edr_search",
			Description: ToolDesc["search"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"pattern": {Type: "string", Description: P("pattern")},
					"budget":  {Type: "integer", Description: P("budget")},
					"body":    {Type: "boolean", Description: P("body")},
					"text":    {Type: "boolean", Description: P("text")},
					"regex":   {Type: "boolean", Description: P("regex")},
					"include": {Description: P("include")},
					"exclude": {Description: P("exclude")},
					"context": {Type: "integer", Description: P("context")},
				},
				Required: []string{"pattern"},
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
		{
			Name:        "edr_explore",
			Description: ToolDesc["explore"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"symbol":     {Type: "string", Description: P("symbol")},
					"file":       {Type: "string", Description: P("file")},
					"budget":     {Type: "integer", Description: P("budget")},
					"body":       {Type: "boolean", Description: P("body")},
					"callers":    {Type: "boolean", Description: P("callers")},
					"deps":       {Type: "boolean", Description: P("deps")},
					"gather":     {Type: "boolean", Description: P("gather")},
					"signatures": {Type: "boolean", Description: P("signatures")},
				},
				Required: []string{"symbol"},
			},
		},
		{
			Name:        "edr_refs",
			Description: ToolDesc["refs"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"symbol": {Type: "string", Description: P("symbol")},
					"file":   {Type: "string", Description: P("file")},
					"impact": {Type: "boolean", Description: P("impact")},
					"depth":  {Type: "integer", Description: P("depth")},
					"chain":  {Type: "string", Description: P("chain")},
				},
				Required: []string{"symbol"},
			},
		},
		{
			Name:        "edr_find",
			Description: ToolDesc["find"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"pattern": {Type: "string", Description: P("pattern")},
					"dir":     {Type: "string", Description: P("dir")},
					"budget":  {Type: "integer", Description: P("budget")},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "edr_rename",
			Description: ToolDesc["rename"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"old_name": {Type: "string", Description: P("old_name")},
					"new_name": {Type: "string", Description: P("new_name")},
					"dry_run":  {Type: "boolean", Description: P("dry_run")},
					"scope":    {Type: "string", Description: P("scope")},
				},
				Required: []string{"old_name", "new_name"},
			},
		},
		{
			Name:        "edr_verify",
			Description: ToolDesc["verify"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"command": {Type: "string", Description: P("command")},
					"timeout": {Type: "integer", Description: P("timeout")},
				},
			},
		},
		{
			Name:        "edr_init",
			Description: ToolDesc["init"],
			InputSchema: mcpSchema{
				Type:       "object",
				Properties: map[string]mcpProp{},
			},
		},
		{
			Name:        "edr_diff",
			Description: ToolDesc["diff"],
			InputSchema: mcpSchema{
				Type: "object",
				Properties: map[string]mcpProp{
					"file":   {Type: "string", Description: P("file")},
					"symbol": {Type: "string", Description: P("symbol")},
				},
				Required: []string{"file"},
			},
		},
	}
}


// --- Tool routing ---

// routeTool converts typed tool params into the (cmd, args, flags) tuple for dispatch.
// Returns isSessionCmd=true for commands handled at the session layer (get-diff).
func routeTool(toolName string, raw json.RawMessage) (cmd string, args []string, flags map[string]any, isSessionCmd bool, err error) {
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

	case "edr_edit":
		var p struct {
			File      string  `json:"file"`
			NewText   string  `json:"new_text"`
			OldText   *string `json:"old_text"`
			Symbol    *string `json:"symbol"`
			StartLine *int    `json:"start_line"`
			EndLine   *int    `json:"end_line"`
			Regex     *bool   `json:"regex"`
			All       *bool   `json:"all"`
			DryRun    *bool   `json:"dry_run"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "edit"
		if p.Symbol != nil && *p.Symbol != "" {
			args = []string{p.File, *p.Symbol}
		} else {
			args = []string{p.File}
		}
		flags["new_text"] = p.NewText
		if p.OldText != nil {
			flags["old_text"] = *p.OldText
		}
		if p.StartLine != nil {
			flags["start_line"] = *p.StartLine
		}
		if p.EndLine != nil {
			flags["end_line"] = *p.EndLine
		}
		if p.Regex != nil && *p.Regex {
			flags["regex"] = true
		}
		if p.All != nil && *p.All {
			flags["all"] = true
		}
		if p.DryRun != nil && *p.DryRun {
			flags["dry_run"] = true
		}

	case "edr_write":
		var p struct {
			File    string  `json:"file"`
			Content string  `json:"content"`
			Mkdir   *bool   `json:"mkdir"`
			Append  *bool   `json:"append"`
			After   *string `json:"after"`
			Inside  *string `json:"inside"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "write"
		args = []string{p.File}
		flags["content"] = p.Content
		if p.Mkdir != nil && *p.Mkdir {
			flags["mkdir"] = true
		}
		if p.Append != nil && *p.Append {
			flags["append"] = true
		}
		if p.After != nil && *p.After != "" {
			flags["after"] = *p.After
		}
		if p.Inside != nil && *p.Inside != "" {
			flags["inside"] = *p.Inside
		}

	case "edr_search":
		var p struct {
			Pattern string `json:"pattern"`
			Budget  *int   `json:"budget"`
			Body    *bool  `json:"body"`
			Text    *bool  `json:"text"`
			Regex   *bool  `json:"regex"`
			Include any    `json:"include"`
			Exclude any    `json:"exclude"`
			Context *int   `json:"context"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "search"
		args = []string{p.Pattern}
		if p.Budget != nil {
			flags["budget"] = *p.Budget
		}
		if p.Body != nil && *p.Body {
			flags["body"] = true
		}
		if p.Text != nil && *p.Text {
			flags["text"] = true
		}
		if p.Regex != nil && *p.Regex {
			flags["regex"] = true
		}
		if p.Include != nil {
			flags["include"] = p.Include
		}
		if p.Exclude != nil {
			flags["exclude"] = p.Exclude
		}
		if p.Context != nil {
			flags["context"] = *p.Context
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

	case "edr_explore":
		var p struct {
			Symbol     string  `json:"symbol"`
			File       *string `json:"file"`
			Budget     *int    `json:"budget"`
			Body       *bool   `json:"body"`
			Callers    *bool   `json:"callers"`
			Deps       *bool   `json:"deps"`
			Gather     *bool   `json:"gather"`
			Signatures *bool   `json:"signatures"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "explore"
		if p.File != nil && *p.File != "" {
			args = []string{*p.File, p.Symbol}
		} else {
			args = []string{p.Symbol}
		}
		if p.Budget != nil {
			flags["budget"] = *p.Budget
		}
		if p.Body != nil && *p.Body {
			flags["body"] = true
		}
		if p.Callers != nil && *p.Callers {
			flags["callers"] = true
		}
		if p.Deps != nil && *p.Deps {
			flags["deps"] = true
		}
		if p.Gather != nil && *p.Gather {
			flags["gather"] = true
		}
		if p.Signatures != nil && *p.Signatures {
			flags["signatures"] = true
		}

	case "edr_refs":
		var p struct {
			Symbol string  `json:"symbol"`
			File   *string `json:"file"`
			Impact *bool   `json:"impact"`
			Depth  *int    `json:"depth"`
			Chain  *string `json:"chain"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "refs"
		hasChain := p.Chain != nil && *p.Chain != ""
		if hasChain {
			// runCallChain expects [fromSymbol, toSymbol] — dispatch appends chain target.
			// Always pass just [symbol] here; file disambiguation doesn't apply to call chains
			// since runCallChain resolves symbols by name.
			args = []string{p.Symbol}
			flags["chain"] = *p.Chain
		} else if p.File != nil && *p.File != "" {
			args = []string{*p.File, p.Symbol}
		} else {
			args = []string{p.Symbol}
		}
		if p.Impact != nil && *p.Impact {
			flags["impact"] = true
		}
		if p.Depth != nil {
			flags["depth"] = *p.Depth
		}

	case "edr_find":
		var p struct {
			Pattern string  `json:"pattern"`
			Dir     *string `json:"dir"`
			Budget  *int    `json:"budget"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "find"
		args = []string{p.Pattern}
		if p.Dir != nil && *p.Dir != "" {
			flags["dir"] = *p.Dir
		}
		if p.Budget != nil {
			flags["budget"] = *p.Budget
		}

	case "edr_rename":
		var p struct {
			OldName string  `json:"old_name"`
			NewName string  `json:"new_name"`
			DryRun  *bool   `json:"dry_run"`
			Scope   *string `json:"scope"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "rename"
		args = []string{p.OldName, p.NewName}
		if p.DryRun != nil && *p.DryRun {
			flags["dry_run"] = true
		}
		if p.Scope != nil && *p.Scope != "" {
			flags["scope"] = *p.Scope
		}

	case "edr_verify":
		var p struct {
			Command *string `json:"command"`
			Timeout *int    `json:"timeout"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "verify"
		if p.Command != nil && *p.Command != "" {
			flags["command"] = *p.Command
		}
		if p.Timeout != nil {
			flags["timeout"] = *p.Timeout
		}

	case "edr_init":
		cmd = "init"

	case "edr_diff":
		var p struct {
			File   string  `json:"file"`
			Symbol *string `json:"symbol"`
		}
		if err = json.Unmarshal(raw, &p); err != nil {
			return
		}
		cmd = "get-diff"
		args = []string{p.File}
		if p.Symbol != nil && *p.Symbol != "" {
			args = append(args, *p.Symbol)
		}
		isSessionCmd = true

	case "edr_plan":
		// Handled specially — see handlePlan()
		cmd = "plan"

	default:
		err = fmt.Errorf("unknown tool: %s", toolName)
	}
	return
}

// planParams holds the parsed params for edr_plan.
type planParams struct {
	Reads   []planRead  `json:"reads"`
	Queries []planQuery `json:"queries"`
	Edits   []planEdit  `json:"edits"`
	Writes  []planWrite `json:"writes"`
	Budget  *int        `json:"budget"`
	DryRun  *bool       `json:"dry_run"`
	Verify  any         `json:"verify"`
}

type planRead struct {
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	Budget     *int   `json:"budget,omitempty"`
	Signatures *bool  `json:"signatures,omitempty"`
	Depth      *int   `json:"depth,omitempty"`
}

// planQuery is a generalized read-only command for use in edr_plan.
// Cmd selects the operation: search, explore, refs, map, find, read (default).
type planQuery struct {
	Cmd string `json:"cmd"` // search, explore, refs, map, find, read

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

type planEdit struct {
	File      string `json:"file"`
	OldText   string `json:"old_text,omitempty"`
	NewText   string `json:"new_text,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	StartLine *int   `json:"start_line,omitempty"`
	EndLine   *int   `json:"end_line,omitempty"`
}

type planWrite struct {
	File    string  `json:"file"`
	Content string  `json:"content"`
	Mkdir   *bool   `json:"mkdir,omitempty"`
	After   *string `json:"after,omitempty"`
	Inside  *string `json:"inside,omitempty"`
	Append  *bool   `json:"append,omitempty"`
}

// handlePlan dispatches edr_plan (batch reads + atomic edits).
func handlePlan(ctx context.Context, db *index.DB, sess *session.Session, raw json.RawMessage) (string, error) {
	var p planParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", err
	}

	hasReads := len(p.Reads) > 0
	hasQueries := len(p.Queries) > 0
	hasEdits := len(p.Edits) > 0
	hasWrites := len(p.Writes) > 0
	hasVerify := p.Verify != nil && p.Verify != false

	if !hasReads && !hasQueries && !hasEdits && !hasWrites && !hasVerify {
		return `{"error": "edr_plan requires at least one of: reads, queries, edits, writes, verify"}`, nil
	}

	var parts []string

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

	// 2. Dispatch generalized queries via DispatchMulti
	if hasQueries {
		cmds := make([]dispatch.MultiCmd, len(p.Queries))
		for i, q := range p.Queries {
			cmds[i] = planQueryToMultiCmd(q)
		}
		var budgetOpt []int
		if p.Budget != nil {
			budgetOpt = []int{*p.Budget}
		}
		results := dispatch.DispatchMulti(ctx, db, cmds, budgetOpt...)
		text := postProcessMultiResults(sess, cmds, results)
		parts = append(parts, fmt.Sprintf(`"queries":%s`, text))
	}

	// 3. Dispatch writes sequentially (before edits, so new files can be edited)
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

	// 4. Dispatch edits via edit-plan (atomic)
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

	// 5. Run verification if requested
	if hasVerify {
		verifyFlags := map[string]any{}
		// verify: true uses auto-detect; verify: "command" uses custom command
		if cmd, ok := p.Verify.(string); ok && cmd != "" {
			verifyFlags["command"] = cmd
		}
		result, err := dispatch.Dispatch(ctx, db, "verify", []string{}, verifyFlags)
		if err != nil {
			parts = append(parts, fmt.Sprintf(`"verify":{"error":%q}`, err.Error()))
		} else {
			data, _ := json.Marshal(result)
			parts = append(parts, fmt.Sprintf(`"verify":%s`, string(data)))
		}
	}

	return "{" + strings.Join(parts, ",") + "}", nil
}


// planQueryToMultiCmd converts a generalized planQuery into a dispatch.MultiCmd.
func planQueryToMultiCmd(q planQuery) dispatch.MultiCmd {
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

			cmd, cmdArgs, flags, isSessionCmd, err := routeTool(params.Name, params.Arguments)
			if err != nil {
				enc.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &jsonRPCError{Code: -32602, Message: err.Error()},
				})
				continue
			}

			// Session-layer commands (get-diff)
			if isSessionCmd {
				result := sess.GetDiff(cmdArgs)
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

			// Handle edr_plan specially
			if cmd == "plan" {
				planText, planErr := handlePlan(ctx, db, sess, params.Arguments)
				if planErr != nil {
					text = fmt.Sprintf(`{"error": %q}`, planErr.Error())
				} else {
					text = planText
				}
			} else {
				if session.EditCommands[cmd] || cmd == "init" {
					sess.InvalidateForEdit(cmd, cmdArgs)
				}

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
