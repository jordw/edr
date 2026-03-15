package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
  edr -r cmd/root.go --sig -s "handleRequest"
  edr -e cmd/root.go --old "oldFunc" --new "newFunc"
  edr -r file.go:Symbol --sig -e file.go --old "x" --new "y" -V`,
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
	reads   []doRead
	queries []doQuery
	edits   []doEdit
	writes  []doWrite

	currentOp    batchOp
	currentRead  doRead
	currentQuery doQuery
	currentEdit  doEdit
	currentWrite doWrite

	verifySet     bool
	verifyEnabled bool
	verifyCommand string

	dryRun        bool
	readAfterEdit bool

	root      string
	stdinUsed bool
}

func (s *batchState) flush() {
	switch s.currentOp {
	case opRead:
		if s.currentRead.File != "" {
			s.reads = append(s.reads, s.currentRead)
		}
		s.currentRead = doRead{}
	case opSearch:
		s.queries = append(s.queries, s.currentQuery)
		s.currentQuery = doQuery{}
	case opEdit:
		if s.currentEdit.File != "" {
			s.edits = append(s.edits, s.currentEdit)
		}
		s.currentEdit = doEdit{}
	case opWrite:
		if s.currentWrite.File != "" {
			s.writes = append(s.writes, s.currentWrite)
		}
		s.currentWrite = doWrite{}
	}
	s.currentOp = opNone
}

func (s *batchState) toParams() doParams {
	s.flush()

	p := doParams{
		Reads:   s.reads,
		Queries: s.queries,
		Edits:   s.edits,
		Writes:  s.writes,
	}

	if s.dryRun {
		p.DryRun = bp(true)
	}
	if s.readAfterEdit {
		p.ReadAfterEdit = bp(true)
	}

	// Auto-verify when edits are present, unless explicitly disabled
	if s.verifySet {
		if s.verifyEnabled {
			if s.verifyCommand != "" {
				p.Verify = s.verifyCommand
			} else {
				p.Verify = true
			}
		}
	} else if len(s.edits) > 0 {
		p.Verify = true
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
		return val, nil
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
			s.currentRead = doRead{File: file, Symbol: sym}

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
				Body:    bp(true), // body on by default
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
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--full is only valid after -r")
			}
			s.currentRead.Full = bp(true)

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

		// ── search modifiers (regex) ──

		case "--regex":
			if s.currentOp != opSearch {
				return nil, fmt.Errorf("--regex is only valid after -s")
			}
			s.currentQuery.Regex = bp(true)

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

		case "--mkdir":
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

		// ── global flags ──

		case "--read-after-edit":
			s.readAfterEdit = true

		case "--root":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.root = val

		default:
			return nil, fmt.Errorf("unknown flag: %s", arg)
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

	// Resolve root
	root := state.root
	if root == "" {
		root, _ = os.Getwd()
	}

	db, err := openDBWithRoot(root, true)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir)
	defer saveSess()

	tc := trace.NewCollector(edrDir, Version)
	defer tc.Close()

	result, err := handleDo(context.Background(), db, sess, tc, json.RawMessage(paramsJSON))
	if err != nil {
		return err
	}
	output.Print(json.RawMessage(result))
	return batchResultError(json.RawMessage(result))
}

// batchResultError checks if any operation in the batch result failed,
// returning a silent error (already printed) to trigger non-zero exit.
func batchResultError(result json.RawMessage) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(result, &m); err != nil {
		return nil
	}

	// Check reads array for ok:false
	if reads, ok := m["reads"]; ok {
		var arr []map[string]any
		if json.Unmarshal(reads, &arr) == nil {
			for _, r := range arr {
				if ok, _ := r["ok"].(bool); !ok {
					return silentError{}
				}
			}
		}
	}

	// Check queries array for ok:false
	if queries, ok := m["queries"]; ok {
		var arr []map[string]any
		if json.Unmarshal(queries, &arr) == nil {
			for _, q := range arr {
				if ok, _ := q["ok"].(bool); !ok {
					return silentError{}
				}
			}
		}
	}

	// Check edits for error
	if edits, ok := m["edits"]; ok {
		var em map[string]any
		if json.Unmarshal(edits, &em) == nil {
			if _, hasErr := em["error"]; hasErr {
				return silentError{}
			}
		}
	}

	// Check writes array for ok:false
	if writes, ok := m["writes"]; ok {
		var arr []map[string]any
		if json.Unmarshal(writes, &arr) == nil {
			for _, w := range arr {
				if ok, _ := w["ok"].(bool); !ok {
					return silentError{}
				}
			}
		}
	}

	// Check verify for ok:false (exit code 2 to distinguish from operation failures)
	if verify, ok := m["verify"]; ok {
		var vm map[string]any
		if json.Unmarshal(verify, &vm) == nil {
			if ok, _ := vm["ok"].(bool); !ok {
				return silentError{code: 2}
			}
		}
	}

	return nil
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
// Code 1 = operation failure (edit/read/write), Code 2 = verify failure.
type silentError struct{ code int }

func (e silentError) Error() string { return "" }
func (e silentError) ExitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
}
