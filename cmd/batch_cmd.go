package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/hints"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
	"github.com/spf13/cobra"
)

// IsBatchFlag returns true if arg is a batch operation flag.
func IsBatchFlag(arg string) bool {
	switch arg {
	case "-r", "--read", "-s", "--search", "-e", "--edit", "-w", "--write",
		"-V", "--verify", "--no-verify":
		return true
	}
	return false
}

var batchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Execute batched operations",
	Long: `Execute multiple operations in a single batch. Operations are specified
as ordered flags: -r (read), -s (search), -e (edit), -w (write), -V (verify).

Modifier flags apply to the preceding operation. Verify runs automatically
when edits are present (use --no-verify to skip).

When EDR_SESSION is set, session optimizations (delta reads, body dedup)
persist across calls via .edr/sessions/<id>.json.

Examples:
  edr -r cmd/root.go --signatures -s "handleRequest"
  edr -e cmd/root.go --old-text "oldFunc" --new-text "newFunc"
  edr -r file.go:Symbol --signatures -e file.go --old-text "x" --new-text "y" -V`,
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Handle --help/-h since DisableFlagParsing swallows it
		for _, a := range args {
			if a == "--help" || a == "-h" {
				return cmd.Help()
			}
		}
		return runBatch(args)
	},
}

func init() {
	batchCmd.Hidden = true
	rootCmd.AddCommand(batchCmd)
}

// --- pointer helpers ---

func bp(b bool) *bool     { return &b }
func ip(i int) *int       { return &i }
func sp(s string) *string { return &s }

// --- batch state machine ---

type batchOp int

const (
	opNone batchOp = iota
	opRead
	opSearch
	opEdit
	opWrite
)

type batchState struct {
	reads         []doRead
	queries       []doQuery
	edits         []doEdit
	writes        []doWrite
	postEditReads []doRead // reads that follow edits/writes in CLI order

	currentOp    batchOp
	currentRead  doRead
	currentQuery doQuery
	currentEdit  doEdit
	currentWrite doWrite

	verifySet     bool
	verifyEnabled bool
	verifyCommand string
	verifyLevel   string
	verifyTimeout int

	dryRun        bool
	readAfterEdit bool
	atomic        bool
	seenMutation  bool // true after first -e or -w

	root      string
	stdinUsed bool
}

func (s *batchState) flush() {
	switch s.currentOp {
	case opRead:
		if s.currentRead.File != "" {
			if s.seenMutation {
				s.postEditReads = append(s.postEditReads, s.currentRead)
			} else {
				s.reads = append(s.reads, s.currentRead)
			}
		}
		s.currentRead = doRead{}
	case opSearch:
		s.queries = append(s.queries, s.currentQuery)
		s.currentQuery = doQuery{}
	case opEdit:
		if s.currentEdit.File != "" {
			s.edits = append(s.edits, s.currentEdit)
			s.seenMutation = true
		}
		s.currentEdit = doEdit{}
	case opWrite:
		if s.currentWrite.File != "" {
			s.writes = append(s.writes, s.currentWrite)
			s.seenMutation = true
		}
		s.currentWrite = doWrite{}
	}
	s.currentOp = opNone
}

func (s *batchState) toParams() doParams {
	s.flush()

	p := doParams{
		Reads:         s.reads,
		Queries:       s.queries,
		Edits:         s.edits,
		Writes:        s.writes,
		PostEditReads: s.postEditReads,
	}

	if s.dryRun {
		p.DryRun = bp(true)
	}
	if s.readAfterEdit {
		p.ReadAfterEdit = bp(true)
	}
	if s.atomic {
		p.Atomic = bp(true)
	}

	// Auto-verify when edits are present, unless explicitly disabled
	if s.verifySet {
		if s.verifyEnabled {
			if s.verifyCommand != "" || s.verifyLevel != "" || s.verifyTimeout > 0 {
				vm := map[string]any{}
				if s.verifyCommand != "" {
					vm["command"] = s.verifyCommand
				}
				if s.verifyLevel != "" {
					vm["level"] = s.verifyLevel
				}
				if s.verifyTimeout > 0 {
					vm["timeout"] = s.verifyTimeout
				}
				p.Verify = vm
			} else {
				p.Verify = true
			}
		}
	} else if len(s.edits) > 0 {
		if s.verifyLevel != "" || s.verifyTimeout > 0 {
			vm := map[string]any{}
			if s.verifyLevel != "" {
				vm["level"] = s.verifyLevel
			}
			if s.verifyTimeout > 0 {
				vm["timeout"] = s.verifyTimeout
			}
			p.Verify = vm
		} else {
			p.Verify = true
		}
	}

	return p
}

// --- argument parsing ---

func parseBatchArgs(args []string) (*batchState, error) {
	s := &batchState{}
	i := 0

	nextArg := func(flag string) (string, error) {
		i++
		if i >= len(args) {
			return "", fmt.Errorf("%s requires an argument", flag)
		}
		return args[i], nil
	}

	nextInt := func(flag string) (int, error) {
		val, err := nextArg(flag)
		if err != nil {
			return 0, err
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			return 0, fmt.Errorf("%s: invalid integer %q", flag, val)
		}
		return n, nil
	}

	resolveContent := func(flag, val string) (string, error) {
		if val == "-" {
			if s.stdinUsed {
				return "", fmt.Errorf("%s: stdin already consumed by a previous operation", flag)
			}
			s.stdinUsed = true
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return "", fmt.Errorf("%s: reading stdin: %w", flag, err)
			}
			return string(data), nil
		}
		if strings.HasPrefix(val, "@") {
			path := val[1:]
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("%s: reading %s: %w", flag, path, err)
			}
			return string(data), nil
		}
		return interpretEscapes(val), nil
	}

	for i < len(args) {
		arg := args[i]

		switch arg {
		// ── operations ──

		case "-r", "--read":
			s.flush()
			s.currentOp = opRead
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			file, sym := splitFileArg(val)
			r := doRead{File: file}
			if sym != "" {
				if start, end, err := parseLineRange(sym); err == nil {
					r.StartLine = ip(start)
					r.EndLine = ip(end)
				} else {
					r.Symbol = sym
				}
			}
			s.currentRead = r

		case "-s", "--search":
			s.flush()
			s.currentOp = opSearch
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery = doQuery{
				Cmd:     "search",
				Pattern: sp(val),
			}

		case "-e", "--edit":
			s.flush()
			s.currentOp = opEdit
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			file, sym := splitFileArg(val)
			s.currentEdit = doEdit{File: file, Symbol: sym}

		case "-w", "--write":
			s.flush()
			s.currentOp = opWrite
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentWrite = doWrite{File: val}

		case "-V", "--verify":
			s.verifySet = true
			s.verifyEnabled = true

		case "--no-verify":
			s.verifySet = true
			s.verifyEnabled = false

		// ── read modifiers ──

		case "--sig", "--signatures":
			if s.currentOp != opRead {
				return nil, fmt.Errorf("%s is only valid after -r", arg)
			}
			s.currentRead.Signatures = bp(true)

		case "--skeleton":
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--skeleton is only valid after -r")
			}
			s.currentRead.Skeleton = bp(true)

		case "--depth":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--depth is only valid after -r")
			}
			s.currentRead.Depth = ip(n)

		case "--budget":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opRead:
				s.currentRead.Budget = ip(n)
			case opSearch:
				s.currentQuery.Budget = ip(n)
			default:
				return nil, fmt.Errorf("--budget is only valid after -r or -s")
			}

		case "--lines":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			start, end, err := parseLineRange(val)
			if err != nil {
				return nil, fmt.Errorf("--lines: %w", err)
			}
			switch s.currentOp {
			case opRead:
				s.currentRead.StartLine = ip(start)
				s.currentRead.EndLine = ip(end)
			case opEdit:
				s.currentEdit.StartLine = ip(start)
				s.currentEdit.EndLine = ip(end)
			default:
				return nil, fmt.Errorf("--lines is only valid after -r or -e")
			}

		case "--full":
			switch s.currentOp {
			case opRead:
				s.currentRead.Full = bp(true)
			case opSearch:
				s.currentQuery.Full = bp(true)
			default:
				return nil, fmt.Errorf("--full is only valid after -r or -s")
			}

		case "--symbols":
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--symbols is only valid after -r")
			}
			s.currentRead.Symbols = bp(true)

		// ── search modifiers ──

		case "--body":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--body is only valid after -s")
			}
			s.currentQuery.Body = bp(true)

		case "--no-body":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--no-body is only valid after -s")
			}
			s.currentQuery.Body = bp(false)

		case "--include":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--include is only valid after -s")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Include = appendStringSlice(s.currentQuery.Include, val)

		case "--exclude":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--exclude is only valid after -s")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Exclude = appendStringSlice(s.currentQuery.Exclude, val)

		case "--text":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--text is only valid after -s")
			}
			s.currentQuery.Text = bp(true)

		case "--context":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--context is only valid after -s")
			}
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Context = ip(n)

		case "--limit":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--limit is only valid after -s")
			}
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Limit = ip(n)

		case "--in":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opSearch:
				s.currentQuery.In = sp(val)
			case opEdit:
				s.currentEdit.In = val
			default:
				return nil, fmt.Errorf("--in is only valid after -s or -e")
			}

		// ── search modifiers (regex) ──

		case "--regex":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--regex is only valid after -s")
			}
			s.currentQuery.Regex = bp(true)

		case "--no-group", "--no_group":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--no-group is only valid after -s")
			}
			s.currentQuery.Group = bp(false)

		// ── edit modifiers ──

		case "--old", "--old_text", "--old-text":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("%s is only valid after -e", arg)
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveContent(arg, val)
			if err != nil {
				return nil, err
			}
			s.currentEdit.OldText = resolved

		case "--new", "--new_text", "--new-text":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opEdit:
				resolved, err := resolveContent(arg, val)
				if err != nil {
					return nil, err
				}
				s.currentEdit.NewText = resolved
			case opWrite:
				resolved, err := resolveContent(arg, val)
				if err != nil {
					return nil, err
				}
				s.currentWrite.Content = resolved
			default:
				return nil, fmt.Errorf("%s is only valid after -e or -w", arg)
			}

		case "--all":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--all is only valid after -e")
			}
			s.currentEdit.All = bp(true)

		case "--delete":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--delete is only valid after -e")
			}
			s.currentEdit.Delete = bp(true)

		case "--insert-at", "--insert_at":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--insert-at is only valid after -e")
			}
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			s.currentEdit.InsertAt = ip(n)

		case "--fuzzy":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--fuzzy is only valid after -e")
			}
			s.currentEdit.Fuzzy = bp(true)

		case "--hash", "--expect-hash", "--expect_hash":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--hash is only valid after -e")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentEdit.ExpectHash = val

		case "--after":
			if s.currentOp != opWrite {
				return nil, fmt.Errorf("--after is only valid after -w")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentWrite.After = sp(val)

		case "--dry-run", "--dry_run":
			if s.currentOp == opEdit {
				s.currentEdit.DryRun = bp(true)
			} else {
				// Global dry-run: applies to all edits and writes
				s.dryRun = true
			}

		// ── write modifiers ──

		case "--content":
			if s.currentOp != opWrite {
				return nil, fmt.Errorf("--content is only valid after -w")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			resolved, err := resolveContent(arg, val)
			if err != nil {
				return nil, err
			}
			s.currentWrite.Content = resolved

		case "--inside":
			if s.currentOp != opWrite {
				return nil, fmt.Errorf("--inside is only valid after -w")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentWrite.Inside = sp(val)

		case "--mkdir", "--create-parents":
			if s.currentOp != opWrite {
				return nil, fmt.Errorf("--mkdir is only valid after -w")
			}
			s.currentWrite.Mkdir = bp(true)

		case "--append":
			if s.currentOp != opWrite {
				return nil, fmt.Errorf("--append is only valid after -w")
			}
			s.currentWrite.Append = bp(true)

		// ── verify modifiers ──

		case "--command":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.verifySet = true
			s.verifyEnabled = true
			s.verifyCommand = val

		case "--level":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.verifyLevel = val

		case "--timeout":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			s.verifyTimeout = n

		// ── global flags ──

		case "--read-after-edit":
			s.readAfterEdit = true

		case "--atomic":
			s.atomic = true

		case "--root":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.root = val

		case "--start-line", "--start_line":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opEdit:
				s.currentEdit.StartLine = ip(n)
			default:
				return nil, fmt.Errorf("%s is only valid after -e", arg)
			}

		case "--end-line", "--end_line":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opEdit:
				s.currentEdit.EndLine = ip(n)
			default:
				return nil, fmt.Errorf("%s is only valid after -e", arg)
			}

		case "--verbose":
			verbose = true

		default:
			return nil, suggestBatchFlag(arg, s.currentOp)
		}

		i++
	}

	return s, nil
}

// parseLineRange parses "N-M" into start and end line numbers.
func parseLineRange(s string) (int, int, error) {
	sep := "-"
	if strings.Contains(s, ":") {
		sep = ":"
	}
	parts := strings.SplitN(s, sep, 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected start:end format, got %q", s)
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start line %q", parts[0])
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end line %q", parts[1])
	}
	return start, end, nil
}

// splitFileArg splits "file:symbol" into file and symbol parts.
// Returns (file, "") if no symbol is present.
func splitFileArg(arg string) (string, string) {
	for i := len(arg) - 1; i > 0; i-- {
		if arg[i] == ':' {
			return arg[:i], arg[i+1:]
		}
		if arg[i] == '/' || arg[i] == '\\' {
			break // path separator before colon means no symbol
		}
	}
	return arg, ""
}

// --- execution ---

func runBatch(args []string) error {
	state, err := parseBatchArgs(args)
	if err != nil {
		return err
	}

	params := state.toParams()

	// Nothing to do
	if len(params.Reads) == 0 && len(params.Queries) == 0 &&
		len(params.Edits) == 0 && len(params.Writes) == 0 &&
		params.Verify == nil {
		return fmt.Errorf("no operations specified")
	}

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}

	// Resolve root — discover repo root by walking up for .edr/.git
	root := state.root
	if root == "" {
		wd, _ := os.Getwd()
		root = discoverRoot(wd)
	}

	// Only auto-index when mutations are present. Read-only batch
	// operations use the strict opener — same contract as standalone.
	hasMutations := len(params.Edits) > 0 || len(params.Writes) > 0
	var db *index.DB
	if hasMutations {
		db, err = openDBAndIndex(root, !verbose)
	} else {
		db, err = openDBStrictRoot(root)
	}
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	tc := trace.NewCollector(edrDir, Version)
	defer tc.Close()

	cmdName := inferBatchCommand(&params)
	env := output.NewEnvelope(cmdName)
	if err := handleDo(context.Background(), db, sess, tc, env, json.RawMessage(paramsJSON)); err != nil {
		return err
	}

	// Emit contextual hints to stderr
	emitBatchHints(sess, &params, env)

	output.PrintEnvelope(env)

	if !env.OK {
		return silentError{code: 1}
	}
	return nil
}
// inferBatchCommand returns the command name for the envelope.
// Single-type batches use the actual command name for parity with standalone.
// Mixed batches use "batch".
func inferBatchCommand(p *doParams) string {
	types := 0
	name := "batch"
	if len(p.Reads) > 0 {
		types++
		name = "read"
	}
	if len(p.Queries) > 0 {
		types++
		name = "search"
	}
	if len(p.Edits) > 0 {
		types++
		name = "edit"
	}
	if len(p.Writes) > 0 {
		types++
		name = "write"
	}
	if len(p.Renames) > 0 {
		types++
		name = "rename"
	}
	if types != 1 {
		return "batch"
	}
	return name
}

// appendStringSlice appends val to an existing include/exclude field.
// The field may be nil, a string, or []string; the result is always []string.
func appendStringSlice(existing any, val string) []string {
	switch v := existing.(type) {
	case nil:
		return []string{val}
	case string:
		return []string{v, val}
	case []string:
		return append(v, val)
	default:
		return []string{val}
	}
}

// silentError signals non-zero exit without printing an additional error message
// (the structured JSON response was already printed).
// Exit code is always 1 when ok:false (per spec: only exit codes 0 and 1).
type silentError struct{ code int }

func (e silentError) Error() string { return "" }
func (e silentError) ExitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
}


// emitBatchHints builds hints.Ops from the batch params and emits contextual
// suggestions to stderr. Hint keys are recorded in the session to avoid repeats.
func emitBatchHints(sess *session.Session, p *doParams, env *output.Envelope) {
	var ops []hints.Op

	for _, r := range p.Reads {
		flags := make(map[string]bool)
		if r.Signatures != nil && *r.Signatures {
			flags["sig"] = true
		}
		if r.Skeleton != nil && *r.Skeleton {
			flags["skeleton"] = true
		}
		if r.Budget != nil {
			flags["budget"] = true
		}
		if r.Full != nil && *r.Full {
			flags["full"] = true
		}
		ops = append(ops, hints.Op{Kind: "read", Flags: flags})
	}

	for _, q := range p.Queries {
		cmd := q.Cmd
		if cmd == "" {
			cmd = inferQueryCmd(q)
		}
		flags := make(map[string]bool)
		meta := make(map[string]string)
		if q.Text != nil && *q.Text {
			flags["text"] = true
		}
		if q.Context != nil {
			flags["context"] = true
		}
		if q.Budget != nil {
			flags["budget"] = true
		}
		if q.Full != nil && *q.Full {
			flags["full"] = true
		}
		if q.Impact != nil && *q.Impact {
			flags["impact"] = true
		}
		if q.Chain != nil {
			flags["chain"] = true
		}
		if q.Type != nil {
			flags["type"] = true
		}
		if q.Grep != nil {
			flags["grep"] = true
		}
		// Count search results from the envelope
		if cmd == "search" {
			resultCount := countSearchResults(env)
			meta["results"] = strconv.Itoa(resultCount)
		}
		ops = append(ops, hints.Op{Kind: cmd, Flags: flags, Meta: meta})
	}

	for _, e := range p.Edits {
		flags := make(map[string]bool)
		if e.DryRun != nil && *e.DryRun {
			flags["dry_run"] = true
		}
		if e.All != nil && *e.All {
			flags["all"] = true
		}
		ops = append(ops, hints.Op{Kind: "edit", Flags: flags})
	}

	for _, w := range p.Writes {
		flags := make(map[string]bool)
		if w.Inside != nil {
			flags["inside"] = true
		}
		if w.After != nil {
			flags["after"] = true
		}
		ops = append(ops, hints.Op{Kind: "write", Flags: flags})
	}

	if len(ops) == 0 {
		return
	}

	ctx := hints.Context{
		Ops:       ops,
		IsBatch:   true,
		HasError:  !env.OK,
		SeenHints: sess.GetSeenHints(),
	}
	keys := hints.Emit(ctx)
	sess.RecordHints(keys)
}

// countSearchResults counts total matches across search ops in the envelope.
func countSearchResults(env *output.Envelope) int {
	total := 0
	for _, op := range env.Ops {
		if tm, ok := op["total_matches"]; ok {
			switch v := tm.(type) {
			case float64:
				total += int(v)
			case int:
				total += v
			}
		}
	}
	return total
}

// allBatchFlags returns every flag recognized by parseBatchArgs, grouped by
// which operation(s) they belong to. Used for "did you mean" suggestions.
var batchFlagOps = map[string][]string{
	"--sig":            {"-r"},
	"--signatures":     {"-r"},
	"--skeleton":       {"-r"},
	"--depth":          {"-r"},
	"--budget":         {"-r", "-s"},
	"--lines":          {"-r", "-e"},
	"--full":           {"-r", "-s"},
	"--symbols":        {"-r"},
	"--body":           {"-s"},
	"--no-body":        {"-s"},
	"--include":        {"-s"},
	"--exclude":        {"-s"},
	"--text":           {"-s"},
	"--context":        {"-s"},
	"--limit":          {"-s"},
	"--in":             {"-s", "-e"},
	"--regex":          {"-s"},
	"--old":            {"-e"},
	"--old-text":       {"-e"},
	"--new":            {"-e", "-w"},
	"--new-text":       {"-e", "-w"},
	"--all":            {"-e"},
	"--after":          {"-w"},
	"--content":        {"-w"},
	"--inside":         {"-w"},
	"--mkdir":          {"-w"},
	"--append":         {"-w"},
	"--dry-run":        {"-e"},
	"--start-line":     {"-e"},
	"--end-line":       {"-e"},
	"--command":        {"--verify"},
	"--read-after-edit": {"global"},
	"--root":           {"global"},
	"--verbose":        {"global"},
}

// suggestBatchFlag builds a helpful error for an unknown batch flag.
// Priority: (1) exact match on standalone command flag, (2) Levenshtein-close batch flag.
func suggestBatchFlag(flag string, currentOp batchOp) error {
	clean := strings.TrimLeft(flag, "-")

	// 1. Exact match on a standalone command flag (e.g. --dir → `edr map`).
	for _, spec := range cmdspec.Registry {
		for _, f := range spec.Flags {
			cliName := strings.ReplaceAll(f.Name, "_", "-")
			if cliName == clean || f.Alias == clean {
				return fmt.Errorf("unknown flag: %s (this flag is available on `edr %s`, not in batch mode)", flag, spec.Name)
			}
		}
	}

	// 2. Levenshtein match against known batch flags.
	bestFlag := ""
	bestDist := 3
	for known := range batchFlagOps {
		d := cmdspec.Levenshtein(strings.TrimLeft(known, "-"), clean)
		if d < bestDist {
			bestDist = d
			bestFlag = known
		}
	}
	if bestFlag != "" {
		ops := batchFlagOps[bestFlag]
		return fmt.Errorf("unknown flag: %s (did you mean %s? valid after %s)", flag, bestFlag, strings.Join(ops, " or "))
	}

	return fmt.Errorf("unknown flag: %s", flag)
}

func interpretEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
