package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"path/filepath"
	"strconv"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
)

// setOptBool sets a boolean flag if the pointer is non-nil and true.
func setOptBool(flags map[string]any, key string, v *bool) {
	if v != nil && *v {
		flags[key] = true
	}
}

// setOptInt sets an integer flag if the pointer is non-nil.
func setOptInt(flags map[string]any, key string, v *int) {
	if v != nil {
		flags[key] = *v
	}
}

// setOptStr sets a string flag if the pointer is non-nil and non-empty.
func setOptStr(flags map[string]any, key string, v *string) {
	if v != nil && *v != "" {
		flags[key] = *v
	}
}

// doAssert is a post-edit assertion that checks a condition.
type doAssert struct {
	SymbolExists string `json:"symbol_exists,omitempty"` // file:Symbol or bare Symbol must exist
	SymbolAbsent string `json:"symbol_absent,omitempty"` // file:Symbol or bare Symbol must NOT exist
	TextPresent  string `json:"text_present,omitempty"`  // text must be present in file (requires File)
	TextAbsent   string `json:"text_absent,omitempty"`   // text must NOT be present in file (requires File)
	File         string `json:"file,omitempty"`           // file context for text assertions
}

// doParams holds the parsed params for batch operations.
type doParams struct {
	Reads           []doRead   `json:"reads"`
	Queries         []doQuery  `json:"queries"`
	Edits           []doEdit   `json:"edits"`
	Writes          []doWrite  `json:"writes"`
	Renames         []doRename `json:"renames"`
	PostEditReads   []doRead   `json:"post_edit_reads,omitempty"`   // reads that follow edits in CLI order
	PostEditQueries []doQuery  `json:"post_edit_queries,omitempty"` // queries that follow edits in CLI order
	Budget          *int       `json:"budget"`
	DryRun          *bool      `json:"dry_run"`
	Verify          any        `json:"verify"`
	ReadAfterEdit   *bool      `json:"read_after_edit,omitempty"`
	Atomic          *bool      `json:"atomic,omitempty"`
	Assertions      []doAssert `json:"assertions,omitempty"`

	// ReadQueryOrder preserves the interleaved CLI order of reads and queries.
	// Each entry is "r<N>" for Reads[N] or "q<N>" for Queries[N].
	// PostEditOrder is the same for post-edit reads and queries.
	ReadQueryOrder []string `json:"read_query_order,omitempty"`
	PostEditOrder  []string `json:"post_edit_order,omitempty"`
}

type doRead struct {
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	Budget     *int   `json:"budget,omitempty"`
	Signatures *bool  `json:"signatures,omitempty"`
	Skeleton   *bool  `json:"skeleton,omitempty"`
	Lines      string `json:"lines,omitempty"`
	Depth      *int   `json:"depth,omitempty"`
	StartLine  *int   `json:"start_line,omitempty"`
	EndLine    *int   `json:"end_line,omitempty"`
	Symbols    *bool  `json:"symbols,omitempty"`
	Expand     string `json:"expand,omitempty"`
	NoExpand   *bool  `json:"no_expand,omitempty"`
	Full       *bool  `json:"full,omitempty"`
}

// doQuery is a generalized read-only command for use in batch operations.
// Cmd selects the operation: search, refs, map, read, diff.
type doQuery struct {
	Cmd string `json:"cmd"` // search, refs, map, read, diff

	// Shared
	Budget *int    `json:"budget,omitempty"`
	File   *string `json:"file,omitempty"`
	Symbol *string `json:"symbol,omitempty"`
	Full   *bool   `json:"full,omitempty"`

	// search
	Pattern *string `json:"pattern,omitempty"`
	Body    *bool   `json:"body,omitempty"`
	Text    *bool   `json:"text,omitempty"`
	Regex   *bool   `json:"regex,omitempty"`
	Include any     `json:"include,omitempty"`
	Exclude any     `json:"exclude,omitempty"`
	Context *int    `json:"context,omitempty"`
	Limit   *int    `json:"limit,omitempty"`
	Group   *bool   `json:"group,omitempty"`
	In      *string `json:"in,omitempty"`

	Callers    *bool   `json:"callers,omitempty"`
	Deps       *bool   `json:"deps,omitempty"`
	Signatures *bool   `json:"signatures,omitempty"`
	Impact     *bool   `json:"impact,omitempty"`
	Chain      *string `json:"chain,omitempty"`
	Depth      *int    `json:"depth,omitempty"`

	// map / orient
	BodySearch *string `json:"body_search,omitempty"` // --body: filter symbols whose body contains text
	Dir        *string `json:"dir,omitempty"`
	Glob       *string `json:"glob,omitempty"`
	Type       *string `json:"type,omitempty"`
	Grep       *string `json:"grep,omitempty"`
	Lang       *string `json:"lang,omitempty"`
	Locals     *bool   `json:"locals,omitempty"`
}

type doEdit struct {
	File        string `json:"file"`
	OldText     string `json:"old_text,omitempty"`
	NewText     string `json:"new_text,omitempty"`
	Symbol      string `json:"symbol,omitempty"`
	StartLine   *int   `json:"start_line,omitempty"`
	EndLine     *int   `json:"end_line,omitempty"`
	All         *bool  `json:"all,omitempty"`
	In          string `json:"in,omitempty"`
	Where       string `json:"where,omitempty"`
	DryRun      *bool  `json:"dry_run,omitempty"`
	ExpectHash  string `json:"expect_hash,omitempty"`
	RefreshHash *bool  `json:"refresh_hash,omitempty"`
	Delete      *bool  `json:"delete,omitempty"`
	InsertAt    *int   `json:"insert_at,omitempty"`
	Fuzzy       *bool  `json:"fuzzy,omitempty"`
	ReadBack    *bool  `json:"read_back,omitempty"`
	// Write/create flags (when no OldText, acts as write)
	Content  string  `json:"content,omitempty"`
	Inside   *string `json:"inside,omitempty"`
	After    *string `json:"after,omitempty"`
	Append   *bool   `json:"append,omitempty"`
	Mkdir    *bool   `json:"mkdir,omitempty"`
	Verify   *bool   `json:"verify,omitempty"`
	NoVerify *bool   `json:"no_verify,omitempty"`
}

type doWrite struct {
	File    string  `json:"file"`
	Content string  `json:"content"`
	Mkdir   *bool   `json:"mkdir,omitempty"`
	After   *string `json:"after,omitempty"`
	Inside  *string `json:"inside,omitempty"`
	Append  *bool   `json:"append,omitempty"`
	DryRun  *bool   `json:"dry_run,omitempty"`
}

type doRename struct {
	OldName string   `json:"old_name"`
	NewName string   `json:"new_name"`
	DryRun  *bool    `json:"dry_run,omitempty"`
	Word    *bool    `json:"word,omitempty"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
	Budget  *int     `json:"budget,omitempty"`
}

// Batch known-key sets — derived from the canonical registry in cmdspec.
var (
	doKnownKeys     = cmdspec.DoBatchKeys()
	doEditKnownKeys = cmdspec.EditBatchKeys()
	doReadKnownKeys = cmdspec.ReadBatchKeys()
)

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

// parseDo parses and validates batch params, returning warnings for unknown/missing fields.
func parseDo(raw json.RawMessage) (doParams, []string, error) {
	var p doParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, nil, err
	}

	var warnings []string

	// Detect unknown top-level keys and sub-object fields
	var rawMap map[string]json.RawMessage
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
		for section, known := range map[string]map[string]bool{
			"reads": doReadKnownKeys,
			"edits": doEditKnownKeys,
		} {
			if rd, ok := rawMap[section]; ok {
				warnings = append(warnings, checkSubObjectFields(rd, section, known)...)
			}
		}
	}

	// Validate required fields
	for i, r := range p.Reads {
		if r.File == "" {
			warnings = append(warnings, fmt.Sprintf("reads[%d]: missing required field \"file\"", i))
		}
	}
	for i, e := range p.Edits {
		if e.File == "" && e.Where == "" {
			warnings = append(warnings, fmt.Sprintf("edits[%d]: missing required field \"file\" (or \"where\")", i))
		}
	}
	for i, w := range p.Writes {
		if w.File == "" {
			warnings = append(warnings, fmt.Sprintf("writes[%d]: missing required field \"file\"", i))
		}
	}
	for i, r := range p.Renames {
		if r.OldName == "" || r.NewName == "" {
			warnings = append(warnings, fmt.Sprintf("renames[%d]: missing required field \"old_name\" or \"new_name\"", i))
		}
	}

	return p, warnings, nil
}

// executeReadsAndQueries dispatches reads and queries interleaved in CLI order.
func executeReadsAndQueries(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams) {
	dispatchInterleavedOps(ctx, db, sess, env, p.Reads, p.Queries, p.ReadQueryOrder, p.Budget, false)
}

// executePostEditReadsAndQueries dispatches post-edit reads and queries interleaved in CLI order.
func executePostEditReadsAndQueries(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams) {
	dispatchInterleavedOps(ctx, db, sess, env, p.PostEditReads, p.PostEditQueries, p.PostEditOrder, p.Budget, true)
}

// dispatchInterleavedOps merges reads and queries into a single DispatchMulti call,
// preserving CLI order from the order tags.
func dispatchInterleavedOps(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, reads []doRead, queries []doQuery, order []string, budget *int, forceFullRead bool) {
	// Distribute top-level budget to queries that lack individual budgets.
	if budget != nil && len(queries) > 0 {
		n := len(reads) + len(queries)
		perOp := *budget * 2 / (n + 1)
		if perOp > *budget {
			perOp = *budget
		}
		if perOp < 50 {
			perOp = 50
		}
		for i := range queries {
			if queries[i].Budget == nil {
				b := perOp
				queries[i].Budget = &b
			}
		}
	}

	// Normalize queries.
	for i := range queries {
		normalizeQueryCmd(&queries[i])
	}

	// If no order tags, fall back to reads-then-queries (JSON API compat).
	if len(order) == 0 {
		for i := range reads {
			order = append(order, fmt.Sprintf("r%d", i))
		}
		for i := range queries {
			order = append(order, fmt.Sprintf("q%d", i))
		}
	}

	// Build interleaved MultiCmd slice.
	cmds := make([]dispatch.MultiCmd, len(order))
	for i, tag := range order {
		if tag[0] == 'r' {
			idx, _ := strconv.Atoi(tag[1:])
			cmds[i] = buildReadCmd(reads[idx], forceFullRead)
		} else {
			idx, _ := strconv.Atoi(tag[1:])
			cmds[i] = queryToMultiCmd(queries[idx])
		}
	}

	var budgetOpt []int
	if budget != nil {
		budgetOpt = []int{*budget}
	}
	results := dispatch.DispatchMulti(ctx, db, cmds, budgetOpt...)
	addMultiResultOps(env, sess, cmds, results, "")
}

// executeReads dispatches read operations and adds results as ops on the envelope.
func buildReadCmd(r doRead, forceFullRead bool) dispatch.MultiCmd {
	args := []string{r.File}
	if r.Symbol != "" {
		args = []string{r.File + ":" + r.Symbol}
	}
	if r.StartLine != nil && r.EndLine != nil && r.Symbol == "" {
		args = []string{r.File, strconv.Itoa(*r.StartLine), strconv.Itoa(*r.EndLine)}
	}
	flags := map[string]any{}
	if forceFullRead {
		flags["full"] = true
	}
	setOptInt(flags, "budget", r.Budget)
	setOptBool(flags, "signatures", r.Signatures)
	setOptBool(flags, "skeleton", r.Skeleton)
	setOptBool(flags, "symbols", r.Symbols)
	setOptBool(flags, "full", r.Full)
	if r.Expand != "" {
		flags["expand"] = r.Expand
	}
	setOptInt(flags, "depth", r.Depth)
	if r.Lines != "" {
		flags["lines"] = r.Lines
	}
	if r.StartLine != nil && r.EndLine == nil {
		flags["start_line"] = *r.StartLine
	}
	if r.EndLine != nil && r.StartLine == nil {
		flags["end_line"] = *r.EndLine
	}
	return dispatch.MultiCmd{Cmd: "read", Args: args, Flags: flags}
}

func executeReads(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams) {
	dispatchReads(ctx, db, sess, env, p.Reads, p.Budget, false, "r")
}

// executePostEditReads runs reads that were placed after edits in CLI order.
// These see post-edit file state since edits have already been committed.
func executePostEditReads(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams) {
	dispatchReads(ctx, db, sess, env, p.PostEditReads, p.Budget, true, "pr")
}

func dispatchReads(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, reads []doRead, budget *int, forceFullRead bool, prefix string) {
	cmds := make([]dispatch.MultiCmd, len(reads))
	for i, r := range reads {
		cmds[i] = buildReadCmd(r, forceFullRead)
	}
	var budgetOpt []int
	if budget != nil {
		budgetOpt = []int{*budget}
	}
	results := dispatch.DispatchMulti(ctx, db, cmds, budgetOpt...)
	addMultiResultOps(env, sess, cmds, results, prefix)
}

// executeQueries dispatches query operations and adds results as ops on the envelope.
// Follows the same normalize-then-build-then-dispatch pattern as executeReads.
func executeQueries(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams) {
	// Distribute top-level budget to queries that lack individual budgets.
	if p.Budget != nil && len(p.Queries) > 0 {
		n := len(p.Queries)
		perQuery := *p.Budget * 2 / (n + 1)
		if perQuery > *p.Budget {
			perQuery = *p.Budget
		}
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

	// 1. Normalize all queries: set Cmd, apply defaults
	for i := range p.Queries {
		normalizeQueryCmd(&p.Queries[i])
	}

	// 2. Build MultiCmds and partition into dispatch vs diff vs error
	allCmds := make([]dispatch.MultiCmd, len(p.Queries))
	allResults := make([]dispatch.MultiResult, len(p.Queries))
	var dispatchIdxs, diffIdxs []int

	for i, q := range p.Queries {
		if q.Cmd == "diff" {
			allCmds[i] = dispatch.MultiCmd{Cmd: "diff"}
			diffIdxs = append(diffIdxs, i)
		} else if q.Cmd == "" {
			allCmds[i] = dispatch.MultiCmd{}
			allResults[i] = dispatch.MultiResult{
				OK:    false,
				Error: `query requires a "cmd" field (search, map, read)`,
			}
		} else {
			allCmds[i] = queryToMultiCmd(q)
			dispatchIdxs = append(dispatchIdxs, i)
		}
	}

	// 3. Dispatch
	if len(dispatchIdxs) > 0 {
		cmds := make([]dispatch.MultiCmd, len(dispatchIdxs))
		for ci, qi := range dispatchIdxs {
			cmds[ci] = allCmds[qi]
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

	// Handle diff queries (session-based, not dispatched)
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

	// 4. Post-process results
	addMultiResultOps(env, sess, allCmds, allResults, "")
}

// executeWrites dispatches write operations and adds results as ops on the envelope.
// Returns whether any write failed.
func executeWrites(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams, dryRun bool) bool {
	anyFailed := false
	for i, w := range p.Writes {
		opID := fmt.Sprintf("w%d", i)
		flags := map[string]any{"content": w.Content}
		setOptBool(flags, "mkdir", w.Mkdir)
		setOptBool(flags, "append", w.Append)
		setOptStr(flags, "after", w.After)
		setOptStr(flags, "inside", w.Inside)
		if dryRun {
			flags["dry_run"] = true
		}
		result, err := dispatch.Dispatch(ctx, db, "write", []string{w.File}, flags)
		if err != nil {
			env.AddFailedOpWithCode(opID, "write", classifyError(err), err.Error())
			anyFailed = true
		} else {
			env.AddOp(opID, "write", result)
			if !dryRun {
				sess.InvalidateFile(w.File)
			}
		}
	}
	return anyFailed
}

// executeEdits dispatches edit operations individually via DispatchMulti
// (same path as standalone `edr edit`), and emits per-edit ops.
// Returns (editsFailed, allNoop).
func executeEdits(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, p *doParams, warnings *[]string) (bool, bool) {
	dryRun := p.DryRun != nil && *p.DryRun

	// Invalidate session cache for all files being edited.
	for _, e := range p.Edits {
		if e.File != "" {
			sess.InvalidateForEdit("edit", []string{e.File})
		}
	}

	cmds := make([]dispatch.MultiCmd, len(p.Edits))
	for i, e := range p.Edits {
		var args []string
		if e.Where != "" {
			// --where mode: no file arg, dispatch resolves from index
			args = nil
		} else {
			args = []string{e.File}
			if e.Symbol != "" {
				args = []string{e.File, e.Symbol}
			}
		}
		flags := map[string]any{}
		if e.Where != "" {
			flags["where"] = e.Where
		}
		if e.OldText != "" {
			flags["old_text"] = e.OldText
		}
		if e.NewText != "" || e.OldText != "" || e.Symbol != "" || (e.StartLine != nil && e.EndLine != nil) || e.Where != "" {
			flags["new_text"] = e.NewText
		}
		setOptInt(flags, "start_line", e.StartLine)
		setOptInt(flags, "end_line", e.EndLine)
		setOptBool(flags, "all", e.All)
		setOptBool(flags, "delete", e.Delete)
		setOptBool(flags, "fuzzy", e.Fuzzy)
		setOptBool(flags, "read_back", e.ReadBack)
		setOptInt(flags, "insert_at", e.InsertAt)
		if e.In != "" {
			flags["in"] = e.In
		}
		if e.ExpectHash != "" {
			flags["expect_hash"] = e.ExpectHash
		}
		setOptBool(flags, "refresh_hash", e.RefreshHash)
		if dryRun {
			flags["dry_run"] = true
		}
		cmds[i] = dispatch.MultiCmd{Cmd: "edit", Args: args, Flags: flags}
	}

	// Atomic mode: validate all edits first with dry-run, then apply
	isAtomic := p.Atomic != nil && *p.Atomic && !dryRun
	if isAtomic {
		// Phase 1: validate all edits with dry-run
		dryRunCmds := make([]dispatch.MultiCmd, len(cmds))
		for i, cmd := range cmds {
			dryRunCmds[i] = dispatch.MultiCmd{
				Cmd: cmd.Cmd, Args: cmd.Args,
				Flags: maps.Clone(cmd.Flags),
			}
			dryRunCmds[i].Flags["dry_run"] = true
		}
		dryResults := dispatch.DispatchMulti(ctx, db, dryRunCmds)
		for i, r := range dryResults {
			if !r.OK {
				// Any validation failure: abort all edits
				for j := range cmds {
					opID := fmt.Sprintf("e%d", j)
					if j == i {
						env.AddFailedOpWithCode(opID, "edit", classifyErrorMsg(r.Error), r.Error)
					} else {
						env.AddSkippedOp(opID, "edit", "atomic batch aborted due to failed edit")
					}
				}
				return true, false
			}
		}
		// Phase 2: all validated — now apply for real
	}

	results := dispatch.DispatchMulti(ctx, db, cmds)

	// Emit per-edit ops and check for failures/noops.
	anyFailed := false
	allNoop := true
	for i, r := range results {
		opID := fmt.Sprintf("e%d", i)

		if !r.OK {
			env.AddFailedOpWithCode(opID, "edit", classifyErrorMsg(r.Error), r.Error)
			anyFailed = true
			allNoop = false
			continue
		}

		// Apply session post-processing
		result := r.Result
		data, _ := json.Marshal(result)
		text := sess.PostProcess("edit", cmds[i].Args, cmds[i].Flags, result, string(data))
		if text != string(data) {
			var newResult any
			if json.Unmarshal([]byte(text), &newResult) == nil {
				result = newResult
			}
		}

		// Check noop status
		if m, ok := result.(map[string]any); ok {
			if st, _ := m["status"].(string); st != "noop" {
				allNoop = false
			}
		} else {
			allNoop = false
		}

		env.AddOp(opID, "edit", result)
	}

	if allNoop && len(p.Edits) == 0 {
		allNoop = false
	}

	return anyFailed, allNoop
}

// executeVerify dispatches the verify command and sets verify on the envelope.
func executeVerify(ctx context.Context, db index.SymbolStore, env *output.Envelope, p *doParams) {
	verifyFlags := map[string]any{}

	// Collect edited/written file paths so verify can scope to relevant packages
	var editedFiles []string
	for _, e := range p.Edits {
		editedFiles = append(editedFiles, e.File)
	}
	for _, w := range p.Writes {
		editedFiles = append(editedFiles, w.File)
	}
	if len(editedFiles) > 0 {
		verifyFlags["files"] = editedFiles
	}
	if cmd, ok := p.Verify.(string); ok && cmd != "" {
		if cmd == "test" || cmd == "build" {
			verifyFlags["level"] = cmd
		} else {
			verifyFlags["command"] = cmd
		}
	} else if m, ok := p.Verify.(map[string]any); ok {
		if cmd, ok := m["command"].(string); ok && cmd != "" {
			verifyFlags["command"] = cmd
		}
		if level, ok := m["level"].(string); ok && level != "" {
			verifyFlags["level"] = level
		}
		if timeout, ok := m["timeout"]; ok {
			verifyFlags["timeout"] = timeout
		}
	}
	result, err := dispatch.Dispatch(ctx, db, "verify", []string{}, verifyFlags)
	if err != nil {
		env.SetVerify(map[string]any{"status": "failed", "error": err.Error()})
		return
	}
	env.SetVerify(result)
}

// handleDo dispatches batch operations and builds an *output.Envelope directly.
func handleDo(ctx context.Context, db index.SymbolStore, sess *session.Session, env *output.Envelope, raw json.RawMessage) error {
	ctx = index.WithSourceCache(ctx)
	output.SetRoot(db.Root())

	p, warnings, err := parseDo(raw)
	if err != nil {
		return err
	}

	hasReads := len(p.Reads) > 0
	hasQueries := len(p.Queries) > 0
	hasEdits := len(p.Edits) > 0
	hasWrites := len(p.Writes) > 0
	hasRenames := len(p.Renames) > 0
	hasVerify := p.Verify != nil && p.Verify != false

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

	if !hasReads && !hasQueries && !hasEdits && !hasWrites && !hasRenames && !hasVerify {
		env.AddError("empty_request", "request requires at least one of: reads, queries, edits, writes, renames, verify")
		return nil
	}

	// 1. Reads and queries (interleaved in CLI order)
	if hasReads || hasQueries {
		executeReadsAndQueries(ctx, db, sess, env, &p)
	}

	// Auto-checkpoint before mutations (rolling cap of 3)
	if (hasEdits || hasWrites || hasRenames) && sess != nil {
		isDry := p.DryRun != nil && *p.DryRun
		if !isDry {
			dirtyFiles := sess.GetDirtyFiles()
			// Include target files so first-edit-in-session is undoable
			targetSet := make(map[string]bool)
			for _, f := range dirtyFiles {
				targetSet[f] = true
			}
			for _, e := range p.Edits {
				if e.File != "" && !targetSet[e.File] {
					dirtyFiles = append(dirtyFiles, e.File)
					targetSet[e.File] = true
				}
			}
			for _, w := range p.Writes {
				if w.File != "" && !targetSet[w.File] {
					dirtyFiles = append(dirtyFiles, w.File)
					targetSet[w.File] = true
				}
			}
			sessDir := filepath.Join(db.EdrDir(), "sessions")
			if _, err := sess.CreateAutoCheckpoint(sessDir, db.Root(), "batch", dirtyFiles); err != nil {
				fmt.Fprintf(os.Stderr, "edr: checkpoint failed: %v\n", err)
			}
		}
	}

	// Pre-promote per-edit dry_run to batch level before writes execute.
	if p.DryRun == nil || !*p.DryRun {
		for _, e := range p.Edits {
			if e.DryRun != nil && *e.DryRun {
				t := true
				p.DryRun = &t
				break
			}
		}
	}
	isDryRun := p.DryRun != nil && *p.DryRun

	// 3. Edits (atomic via transaction — run before writes so failures gate writes)
	editsFailed := false
	allNoop := false
	if hasEdits {
		editsFailed, allNoop = executeEdits(ctx, db, sess, env, &p, &warnings)
	}

	// 4. Renames
	if hasRenames {
		for i, r := range p.Renames {
			opID := fmt.Sprintf("n%d", i)
			sess.InvalidateForEdit("rename", []string{r.OldName, r.NewName})
			renameFlags := map[string]any{}
			if r.DryRun != nil && *r.DryRun {
				renameFlags["dry_run"] = true
			}
			if r.Word != nil && *r.Word {
				renameFlags["word"] = true
			}
			if len(r.Include) > 0 {
				renameFlags["include"] = r.Include
			}
			if len(r.Exclude) > 0 {
				renameFlags["exclude"] = r.Exclude
			}
			result, err := dispatch.Dispatch(ctx, db, "rename", []string{r.OldName, r.NewName}, renameFlags)
			if err != nil {
				env.AddFailedOpWithCode(opID, "rename", classifyError(err), err.Error())
			} else {
				env.AddOp(opID, "rename", result)
			}
		}
	}

	// 5. Writes (skip if edits failed)
	if hasWrites && editsFailed {
		for i := range p.Writes {
			env.AddSkippedOp(fmt.Sprintf("w%d", i), "write", "edits failed")
		}
	} else if hasWrites {
		executeWrites(ctx, db, sess, env, &p, isDryRun)
	}

	// 5b. Post-edit reads and queries (ops that followed edits in CLI order)
	hasPostEditOps := len(p.PostEditReads) > 0 || len(p.PostEditQueries) > 0
	if !editsFailed && hasPostEditOps {
		if isDryRun {
			env.AddError("warning", "dry-run: post-edit reads show pre-edit state (edits were not applied)")
		}
		executePostEditReadsAndQueries(ctx, db, sess, env, &p)
	}

	// 5c. Legacy --read-after-edit (auto-reads edited files with --signatures)
	if !editsFailed && (hasEdits || hasWrites) && p.ReadAfterEdit != nil && *p.ReadAfterEdit && !isDryRun {
		editedFiles := make(map[string]bool)
		for _, e := range p.Edits {
			editedFiles[e.File] = true
		}
		for _, w := range p.Writes {
			editedFiles[w.File] = true
		}
		var readCmds []dispatch.MultiCmd
		for f := range editedFiles {
			readCmds = append(readCmds, dispatch.MultiCmd{
				Cmd:   "read",
				Args:  []string{f},
				Flags: map[string]any{"signatures": true, "full": true},
			})
		}
		if len(readCmds) > 0 {
			var budgetOpt []int
			if p.Budget != nil {
				budgetOpt = []int{*p.Budget}
			}
			results := dispatch.DispatchMulti(ctx, db, readCmds, budgetOpt...)
			addMultiResultOps(env, sess, readCmds, results, "pr")
		}
	}

	// 5d. Assertions — post-edit checks; rollback edits if any assertion fails
	if len(p.Assertions) > 0 && !editsFailed && !isDryRun {
		if executeAssertions(ctx, db, env, p.Assertions) {
			// Assertion failed — rollback to pre-edit checkpoint
			sessDir := filepath.Join(db.EdrDir(), "sessions")
			cpID := session.LatestAutoCheckpoint(sessDir)
			if cpID != "" && sess != nil {
				dirtyFiles := sess.GetDirtyFiles()
				restored, _, _, restoreErr := sess.RestoreCheckpoint(sessDir, db.Root(), cpID, false, dirtyFiles)
				if restoreErr == nil {
					env.AddError("assertion_rollback",
						fmt.Sprintf("assertion failed; rolled back %d file(s) to checkpoint %s", len(restored), cpID))
				} else {
					env.AddError("rollback_failed",
						fmt.Sprintf("assertion failed and rollback failed: %v", restoreErr))
				}
			}
		}
	}

	// 6. Verify — skip for failed edits, dry-run, or all-noop (#19)
	if editsFailed && hasVerify {
		env.SetVerify(map[string]any{"status": "skipped", "reason": "edits failed"})
	} else if isDryRun && hasVerify {
		env.SetVerify(map[string]any{"status": "skipped", "reason": "dry run"})
	} else if allNoop && hasVerify {
		env.SetVerify(map[string]any{"status": "skipped", "reason": "no-op edit"})
	} else if hasVerify {
		executeVerify(ctx, db, env, &p)
	}

	// Record verify result in session build state
	if hasVerify {
		if vm, ok := env.Verify.(map[string]any); ok {
			if status, ok := vm["status"].(string); ok && status != "skipped" {
				sess.RecordVerify(status)
			}
		}
	}

	// Add warnings as envelope errors
	for _, w := range warnings {
		env.Errors = append(env.Errors, output.OpError{Code: "warning", Message: w})
	}

	env.ComputeSummary()
	env.ComputeOK()

	return nil
}

// executeAssertions runs post-edit assertions and adds results to the envelope.
func executeAssertions(ctx context.Context, db index.SymbolStore, env *output.Envelope, assertions []doAssert) (anyFailed bool) {
	for i, a := range assertions {
		opID := fmt.Sprintf("a%d", i)
		result := map[string]any{"type": "assert"}

		switch {
		case a.SymbolExists != "":
			result["check"] = "symbol_exists"
			result["target"] = a.SymbolExists
			_, err := dispatch.Dispatch(ctx, db, "focus", []string{a.SymbolExists}, map[string]any{"budget": 1, "no_expand": true})
			if err != nil {
				result["passed"] = false
				result["message"] = fmt.Sprintf("symbol %q not found", a.SymbolExists)
			} else {
				result["passed"] = true
			}

		case a.SymbolAbsent != "":
			result["check"] = "symbol_absent"
			result["target"] = a.SymbolAbsent
			_, err := dispatch.Dispatch(ctx, db, "focus", []string{a.SymbolAbsent}, map[string]any{"budget": 1, "no_expand": true})
			if err != nil {
				result["passed"] = true // not found = absent = pass
			} else {
				result["passed"] = false
				result["message"] = fmt.Sprintf("symbol %q still exists (expected absent)", a.SymbolAbsent)
			}

		case a.TextPresent != "" && a.File != "":
			result["check"] = "text_present"
			result["target"] = a.TextPresent
			result["file"] = a.File
			file, err := db.ResolvePath(a.File)
			if err != nil {
				result["passed"] = false
				result["error"] = err.Error()
			} else {
				data, err := os.ReadFile(file)
				if err != nil {
					result["passed"] = false
					result["error"] = err.Error()
				} else if strings.Contains(string(data), a.TextPresent) {
					result["passed"] = true
				} else {
					result["passed"] = false
					result["message"] = fmt.Sprintf("text %q not found in %s", a.TextPresent, a.File)
				}
			}

		case a.TextAbsent != "" && a.File != "":
			result["check"] = "text_absent"
			result["target"] = a.TextAbsent
			result["file"] = a.File
			file, err := db.ResolvePath(a.File)
			if err != nil {
				result["passed"] = true // can't read = absent = pass
			} else {
				data, err := os.ReadFile(file)
				if err != nil {
					result["passed"] = true
				} else if strings.Contains(string(data), a.TextAbsent) {
					result["passed"] = false
					result["message"] = fmt.Sprintf("text %q still present in %s (expected absent)", a.TextAbsent, a.File)
				} else {
					result["passed"] = true
				}
			}

		default:
			result["passed"] = false
			result["message"] = "invalid assertion: must specify symbol_exists, symbol_absent, text_present, or text_absent"
		}

		if passed, ok := result["passed"].(bool); ok && !passed {
			anyFailed = true
		}
		env.AddOp(opID, "assert", result)
	}
	return anyFailed
}

// inferQueryCmd determines the public command from populated fields when cmd is omitted.
// Returns public command names: search, map, read.
func inferQueryCmd(q doQuery) string {
	if q.Pattern != nil && *q.Pattern != "" {
		return "search"
	}
	if (q.Dir != nil && *q.Dir != "") || (q.Grep != nil && *q.Grep != "") || (q.Locals != nil && *q.Locals) {
		return "map"
	}
	if q.File != nil && *q.File != "" {
		return "read"
	}
	if q.Symbol != nil && *q.Symbol != "" {
		return "read"
	}
	return "" // will be caught as error below
}

// normalizeQueryCmd mutates a doQuery in-place: infers Cmd from fields if empty, and applies
// a default budget when Cmd was inferred. Returns whether Cmd was inferred.
func normalizeQueryCmd(q *doQuery) bool {
	inferred := false

	switch q.Cmd {
	case "":
		q.Cmd = inferQueryCmd(*q)
		inferred = true
	}

	// When cmd is inferred and no budget is set, apply a default cap
	if inferred && q.Budget == nil {
		defaultBudget := 200
		q.Budget = &defaultBudget
	}

	return inferred
}

// queryToMultiCmd converts a doQuery to a dispatch.MultiCmd. Assumes Cmd is already set
// (call normalizeQueryCmd first for legacy/inference handling).
func queryToMultiCmd(q doQuery) dispatch.MultiCmd {
	cmd := q.Cmd
	args := []string{}
	flags := map[string]any{}

	if q.Budget != nil {
		flags["budget"] = *q.Budget
	}
	if q.Full != nil && *q.Full {
		flags["full"] = true
	}

	switch cmd {
	case "read":
		if q.File != nil {
			f := *q.File
			if q.Symbol != nil && *q.Symbol != "" {
				f += ":" + *q.Symbol
			}
			args = []string{f}
		} else if q.Symbol != nil && *q.Symbol != "" {
			// Symbol-only query → read symbol
			args = []string{*q.Symbol}
		}
		if q.Signatures != nil && *q.Signatures {
			flags["signatures"] = true
		}
		if q.Depth != nil {
			flags["depth"] = *q.Depth
		}

	case "search":
		if q.Pattern == nil || *q.Pattern == "" {
			return dispatch.MultiCmd{Cmd: "search", Args: []string{}, Flags: flags}
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
		if q.Limit != nil {
			flags["limit"] = *q.Limit
		}
		if q.Group != nil && !*q.Group {
			flags["no_group"] = true
		}
		if q.In != nil && *q.In != "" {
			flags["in"] = *q.In
		}

	case "refs":
		// Legacy: refs is no longer a command. Route to focus with expand.
		cmd = "focus"
		if q.File != nil && q.Symbol != nil && *q.Symbol != "" {
			args = []string{*q.File + ":" + *q.Symbol}
		} else if q.Symbol != nil {
			args = []string{*q.Symbol}
		} else if q.File != nil {
			args = []string{*q.File}
		}
		if q.Callers != nil && *q.Callers {
			flags["expand"] = "callers"
		} else if q.Deps != nil && *q.Deps {
			flags["expand"] = "deps"
		}
		if q.Depth != nil {
			flags["depth"] = *q.Depth
		}
		if q.Callers != nil && *q.Callers {
			flags["callers"] = true
		}
		if q.Deps != nil && *q.Deps {
			flags["deps"] = true
		}
		if q.Body != nil && *q.Body {
			flags["body"] = true
		}
		if q.Signatures != nil && *q.Signatures {
			flags["signatures"] = true
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
		if q.Lang != nil && *q.Lang != "" {
			flags["lang"] = *q.Lang
		}
		if q.BodySearch != nil && *q.BodySearch != "" {
			flags["body"] = *q.BodySearch
		}
		if q.Locals != nil && *q.Locals {
			flags["locals"] = true
		}
	}

	return dispatch.MultiCmd{Cmd: cmd, Args: args, Flags: flags}
}

// addMultiResultOps applies session post-processing to each result and adds them as ops.
// prefix is the op_id prefix (e.g. "r" for reads). If empty, uses the first char of the command.
func addMultiResultOps(env *output.Envelope, sess *session.Session, cmds []dispatch.MultiCmd, results []dispatch.MultiResult, prefix string) {
	// Track counters per prefix for op_id generation
	counters := map[string]int{}

	for i, r := range results {
		cmdName := r.Cmd
		if cmdName == "" {
			cmdName = cmds[i].Cmd
		}
		p := prefix
		if p == "" {
			p = string(cmdName[0])
		}
		opID := fmt.Sprintf("%s%d", p, counters[p])
		counters[p]++

		if !r.OK {
			env.AddFailedOpWithCode(opID, cmdName, classifyErrorMsg(r.Error), r.Error)
			recordOp(sess, cmdName, cmds[i].Args, cmds[i].Flags, nil, false)
			continue
		}
		if r.Result == nil {
			env.AddOp(opID, cmdName, map[string]any{})
			recordOp(sess, cmdName, cmds[i].Args, cmds[i].Flags, nil, true)
			continue
		}

		// Extract and strip internal signature before serialization
		extractAndStripSignature(sess, cmdName, cmds[i].Args, r.Result)

		// Apply session post-processing
		data, err := json.Marshal(r.Result)
		if err != nil {
			env.AddOp(opID, cmdName, r.Result)
			recordOp(sess, cmdName, cmds[i].Args, cmds[i].Flags, r.Result, true)
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
		result := r.Result
		newText := sess.PostProcess(cmd, cArgs, flags, r.Result, string(data))
		if newText != string(data) {
			var newResult any
			if json.Unmarshal([]byte(newText), &newResult) == nil {
				result = newResult
			}
		}

		env.AddOp(opID, cmdName, result)
		recordOp(sess, cmdName, cArgs, flags, result, true)
	}
}

// suggestField finds the closest known field name by Levenshtein distance.
func suggestField(input string, known map[string]bool) string {
	best := ""
	bestDist := 3 // only suggest if distance <= 2
	for key := range known {
		d := cmdspec.Levenshtein(input, key)
		if d < bestDist {
			bestDist = d
			best = key
		}
	}
	return best
}
