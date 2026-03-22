package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
)

// doParams holds the parsed params for batch operations.
type doParams struct {
	Reads          []doRead   `json:"reads"`
	Queries        []doQuery  `json:"queries"`
	Edits          []doEdit   `json:"edits"`
	Writes         []doWrite  `json:"writes"`
	Renames        []doRename `json:"renames"`
	PostEditReads  []doRead   `json:"post_edit_reads,omitempty"` // reads that follow edits in CLI order
	Budget         *int       `json:"budget"`
	DryRun         *bool      `json:"dry_run"`
	Verify         any        `json:"verify"`
	Reindex        *bool      `json:"reindex,omitempty"`
	Init           *bool      `json:"init,omitempty"` // legacy alias for reindex
	ReadAfterEdit  *bool      `json:"read_after_edit,omitempty"`
}

type doRead struct {
	File       string `json:"file"`
	Symbol     string `json:"symbol,omitempty"`
	Budget     *int   `json:"budget,omitempty"`
	Signatures *bool  `json:"signatures,omitempty"`
	Skeleton   *bool   `json:"skeleton,omitempty"`
	Lines      string `json:"lines,omitempty"`
	Depth      *int   `json:"depth,omitempty"`
	StartLine  *int   `json:"start_line,omitempty"`
	EndLine    *int   `json:"end_line,omitempty"`
	Symbols    *bool  `json:"symbols,omitempty"`
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

	// refs (includes former explore flags)
	Callers    *bool   `json:"callers,omitempty"`
	Deps       *bool   `json:"deps,omitempty"`
	Signatures *bool   `json:"signatures,omitempty"`
	Impact     *bool   `json:"impact,omitempty"`
	Chain      *string `json:"chain,omitempty"`
	Depth      *int    `json:"depth,omitempty"`

	// map
	Dir    *string `json:"dir,omitempty"`
	Glob   *string `json:"glob,omitempty"`
	Type   *string `json:"type,omitempty"`
	Grep   *string `json:"grep,omitempty"`
	Lang   *string `json:"lang,omitempty"`
	Locals *bool   `json:"locals,omitempty"`
}

type doEdit struct {
	File       string `json:"file"`
	OldText    string `json:"old_text,omitempty"`
	NewText    string `json:"new_text,omitempty"`
	Symbol     string `json:"symbol,omitempty"`
	StartLine  *int   `json:"start_line,omitempty"`
	EndLine    *int   `json:"end_line,omitempty"`
	All        *bool  `json:"all,omitempty"`
	In         string `json:"in,omitempty"`
	DryRun     *bool  `json:"dry_run,omitempty"`
	ExpectHash string `json:"expect_hash,omitempty"`
	Delete     *bool  `json:"delete,omitempty"`
	InsertAt   *int   `json:"insert_at,omitempty"`
	Fuzzy      *bool  `json:"fuzzy,omitempty"`
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
	OldName string  `json:"old_name"`
	NewName string  `json:"new_name"`
	DryRun  *bool   `json:"dry_run,omitempty"`
}

// Batch known-key sets — derived from the canonical registry in cmdspec.
var (
	doKnownKeys      = cmdspec.DoBatchKeys()
	doQueryKnownKeys = cmdspec.QueryBatchKeys()
	doEditKnownKeys  = cmdspec.EditBatchKeys()
	doWriteKnownKeys = cmdspec.WriteBatchKeys()
	doRenameKnownKeys = cmdspec.RenameBatchKeys()
	doReadKnownKeys  = cmdspec.ReadBatchKeys()
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
			"reads": doReadKnownKeys, "queries": doQueryKnownKeys,
			"edits": doEditKnownKeys, "writes": doWriteKnownKeys,
			"renames": doRenameKnownKeys,
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
		if e.File == "" {
			warnings = append(warnings, fmt.Sprintf("edits[%d]: missing required field \"file\"", i))
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

// executeReads dispatches read operations and adds results as ops on the envelope.
func executeReads(ctx context.Context, db *index.DB, sess *session.Session, env *output.Envelope, p *doParams) {
	cmds := make([]dispatch.MultiCmd, len(p.Reads))
	for i, r := range p.Reads {
		readArgs := []string{r.File}
		if r.Symbol != "" {
			readArgs = []string{r.File + ":" + r.Symbol}
		}
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
		if r.Skeleton != nil && *r.Skeleton {
			readFlags["skeleton"] = true
		}
		if r.Lines != "" {
			readFlags["lines"] = r.Lines
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
	addMultiResultOps(env, sess, cmds, results, "r")
}

// executePostEditReads runs reads that were placed after edits in CLI order.
// These see post-edit file state since edits have already been committed.
func executePostEditReads(ctx context.Context, db *index.DB, sess *session.Session, env *output.Envelope, p *doParams) {
	cmds := make([]dispatch.MultiCmd, len(p.PostEditReads))
	for i, r := range p.PostEditReads {
		readArgs := []string{r.File}
		if r.Symbol != "" {
			readArgs = []string{r.File + ":" + r.Symbol}
		}
		if r.StartLine != nil && r.EndLine != nil && r.Symbol == "" {
			readArgs = []string{r.File, strconv.Itoa(*r.StartLine), strconv.Itoa(*r.EndLine)}
		}
		readFlags := map[string]any{"full": true} // force full read (post-edit, skip session delta)
		if r.Budget != nil {
			readFlags["budget"] = *r.Budget
		}
		if r.Signatures != nil && *r.Signatures {
			readFlags["signatures"] = true
		}
		if r.Skeleton != nil && *r.Skeleton {
			readFlags["skeleton"] = true
		}
		if r.Lines != "" {
			readFlags["lines"] = r.Lines
		}
		if r.Depth != nil {
			readFlags["depth"] = *r.Depth
		}
		if r.Symbols != nil && *r.Symbols {
			readFlags["symbols"] = true
		}
		cmds[i] = dispatch.MultiCmd{Cmd: "read", Args: readArgs, Flags: readFlags}
	}
	var budgetOpt []int
	if p.Budget != nil {
		budgetOpt = []int{*p.Budget}
	}
	results := dispatch.DispatchMulti(ctx, db, cmds, budgetOpt...)
	addMultiResultOps(env, sess, cmds, results, "pr")
}

// executeQueries dispatches query operations and adds results as ops on the envelope.
// Follows the same normalize-then-build-then-dispatch pattern as executeReads.
func executeQueries(ctx context.Context, db *index.DB, sess *session.Session, env *output.Envelope, p *doParams, cb *trace.CallBuilder) {
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
				Error: `query requires a "cmd" field (search, map, refs, read, diff)`,
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

	// Trace query events
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

// executeWrites dispatches write operations and adds results as ops on the envelope.
// Returns whether any write failed.
func executeWrites(ctx context.Context, db *index.DB, sess *session.Session, env *output.Envelope, p *doParams, dryRun bool) bool {
	anyFailed := false
	for i, w := range p.Writes {
		opID := fmt.Sprintf("w%d", i)
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
func executeEdits(ctx context.Context, db *index.DB, sess *session.Session, env *output.Envelope, p *doParams, warnings *[]string, cb *trace.CallBuilder) (bool, bool) {
	dryRun := p.DryRun != nil && *p.DryRun

	sess.InvalidateForEdit("edit", []string{})

	cmds := make([]dispatch.MultiCmd, len(p.Edits))
	for i, e := range p.Edits {
		args := []string{e.File}
		if e.Symbol != "" {
			args = []string{e.File, e.Symbol}
		}
		flags := map[string]any{}
		if e.OldText != "" {
			flags["old_text"] = e.OldText
		}
		if e.NewText != "" || e.OldText != "" || e.Symbol != "" || (e.StartLine != nil && e.EndLine != nil) {
			flags["new_text"] = e.NewText
		}
		if e.StartLine != nil {
			flags["start_line"] = *e.StartLine
		}
		if e.EndLine != nil {
			flags["end_line"] = *e.EndLine
		}
		if e.All != nil && *e.All {
			flags["all"] = true
		}
		if e.In != "" {
			flags["in"] = e.In
		}
		if e.ExpectHash != "" {
			flags["expect_hash"] = e.ExpectHash
		}
		if e.Delete != nil && *e.Delete {
			flags["delete"] = true
		}
		if e.InsertAt != nil {
			flags["insert_at"] = *e.InsertAt
		}
		if e.Fuzzy != nil && *e.Fuzzy {
			flags["fuzzy"] = true
		}
		if dryRun {
			flags["dry_run"] = true
		}
		cmds[i] = dispatch.MultiCmd{Cmd: "edit", Args: args, Flags: flags}
	}

	results := dispatch.DispatchMulti(ctx, db, cmds)

	// Emit per-edit ops and check for failures/noops.
	anyFailed := false
	allNoop := true
	for i, r := range results {
		opID := fmt.Sprintf("e%d", i)

		if !r.OK {
			cb.AddEditEvent(p.Edits[i].File, 0, "", "", false)
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

		// Trace
		traceEditEvents(cb, result)

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
func executeVerify(ctx context.Context, db *index.DB, env *output.Envelope, p *doParams, cb *trace.CallBuilder) {
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
		cb.AddVerifyEvent("", false, 0, 0)
		env.SetVerify(map[string]any{"status": "failed", "error": err.Error()})
		return
	}
	traceVerifyEvent(cb, result)
	env.SetVerify(result)
}

// handleDo dispatches batch operations and builds an *output.Envelope directly.
func handleDo(ctx context.Context, db *index.DB, sess *session.Session, tc *trace.Collector, env *output.Envelope, raw json.RawMessage) error {
	ctx = index.WithSourceCache(ctx)
	output.SetRoot(db.Root())

	p, warnings, err := parseDo(raw)
	if err != nil {
		return err
	}

	hasInit := (p.Init != nil && *p.Init) || (p.Reindex != nil && *p.Reindex)
	hasReads := len(p.Reads) > 0
	hasQueries := len(p.Queries) > 0
	hasEdits := len(p.Edits) > 0
	hasWrites := len(p.Writes) > 0
	hasRenames := len(p.Renames) > 0
	hasVerify := p.Verify != nil && p.Verify != false

	// Trace: begin call
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
		env.AddError("empty_request", "request requires at least one of: reads, queries, edits, writes, renames, verify, init")
		return nil
	}

	// 0. Reindex
	if hasInit {
		sess.InvalidateForEdit("reindex", []string{})
		if err := db.WithWriteLock(func() error {
			_, _, err := index.IndexRepo(ctx, db)
			return err
		}); err != nil {
			env.AddFailedOp("i0", "reindex", err.Error())
		} else {
			env.AddOp("i0", "reindex", map[string]any{"version": Version + "+" + BuildHash})
		}
	}

	// 1. Reads
	if hasReads {
		executeReads(ctx, db, sess, env, &p)
	}

	// 2. Queries
	if hasQueries {
		executeQueries(ctx, db, sess, env, &p, cb)
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
		editsFailed, allNoop = executeEdits(ctx, db, sess, env, &p, &warnings, cb)
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

	// 5b. Post-edit reads (reads that followed edits in CLI order)
	if !editsFailed && len(p.PostEditReads) > 0 {
		if isDryRun {
			env.AddError("warning", "dry-run: post-edit reads show pre-edit state (edits were not applied)")
		}
		executePostEditReads(ctx, db, sess, env, &p)
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

	// 6. Verify — skip for failed edits, dry-run, or all-noop (#19)
	if editsFailed && hasVerify {
		env.SetVerify(map[string]any{"status": "skipped", "reason": "edits failed"})
	} else if isDryRun && hasVerify {
		env.SetVerify(map[string]any{"status": "skipped", "reason": "dry run"})
	} else if allNoop && hasVerify {
		env.SetVerify(map[string]any{"status": "skipped", "reason": "no-op edit"})
	} else if hasVerify {
		executeVerify(ctx, db, env, &p, cb)
	}

	// Add warnings as envelope errors
	for _, w := range warnings {
		env.Errors = append(env.Errors, output.OpError{Code: "warning", Message: w})
	}

	env.ComputeOK()

	// Trace: record session stats and finish
	resultData, _ := json.Marshal(env)
	dr, bd, se := sess.GetStats()
	cb.SetSessionStats(dr, bd, se)
	cb.Finish(len(resultData), false, len(warnings))

	return nil
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
	if status, exists := m["status"].(string); exists {
		verifyOK = status == "passed"
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

// inferQueryCmd determines the public command from populated fields when cmd is omitted.
// Only returns public command names: search, refs, map, read.
func inferQueryCmd(q doQuery) string {
	if q.Pattern != nil && *q.Pattern != "" {
		return "search"
	}
	if (q.Callers != nil && *q.Callers) || (q.Deps != nil && *q.Deps) {
		return "refs"
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
		return "refs"
	}
	return "" // will be caught as error below
}

// normalizeQueryCmd mutates a doQuery in-place: translates legacy command names
// (explore→refs/read, find→search), infers Cmd from fields if empty, and applies
// a default budget when Cmd was inferred. Returns whether Cmd was inferred.
func normalizeQueryCmd(q *doQuery) bool {
	inferred := false

	// Translate legacy internal command names to public commands
	switch q.Cmd {
	case "explore":
		if (q.Callers != nil && *q.Callers) || (q.Deps != nil && *q.Deps) {
			q.Cmd = "refs"
		} else {
			q.Cmd = "read"
		}
	case "find":
		q.Cmd = "search"
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
			continue
		}
		if r.Result == nil {
			env.AddOp(opID, cmdName, map[string]any{})
			continue
		}

		// Apply session post-processing
		data, err := json.Marshal(r.Result)
		if err != nil {
			env.AddOp(opID, cmdName, r.Result)
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
	}
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
