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
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/spf13/cobra"
)

// IsBatchFlag returns true if arg is a batch operation flag.
func IsBatchFlag(arg string) bool {
	switch arg {
	case "-f", "--focus", "-o", "--orient",
		"-r", "--read", "-s", "--search", "-e", "--edit", "-w", "--write",
		"-m", "--map", "-q", "--query", "-V", "--verify", "--no-verify":
		return true
	}
	return false
}

var batchCmd = &cobra.Command{
	Use:   "batch",
	Short: "Execute batched operations",
	Long:  batchHelpText(),
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
	opMap
	opQuery
	opEdit
	opWrite
)

type batchState struct {
	reads           []doRead
	queries         []doQuery
	edits           []doEdit
	writes          []doWrite
	postEditReads   []doRead  // reads that follow edits/writes in CLI order
	postEditQueries []doQuery // queries that follow edits/writes in CLI order

	// readQueryOrder tracks interleaved CLI order: "r0","q0","r1","q1",...
	readQueryOrder []string
	postEditOrder  []string

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

	root        string
	stdinUsed   bool
	lastFile    string // carry-forward: last file used by any op
	assertions  []doAssert
}

func (s *batchState) flush() {
	switch s.currentOp {
	case opRead:
		if s.currentRead.File != "" {
			if s.seenMutation {
				tag := fmt.Sprintf("r%d", len(s.postEditReads))
				s.postEditReads = append(s.postEditReads, s.currentRead)
				s.postEditOrder = append(s.postEditOrder, tag)
			} else {
				tag := fmt.Sprintf("r%d", len(s.reads))
				s.reads = append(s.reads, s.currentRead)
				s.readQueryOrder = append(s.readQueryOrder, tag)
			}
		}
		s.currentRead = doRead{}
	case opSearch, opMap, opQuery:
		if s.seenMutation {
			tag := fmt.Sprintf("q%d", len(s.postEditQueries))
			s.postEditQueries = append(s.postEditQueries, s.currentQuery)
			s.postEditOrder = append(s.postEditOrder, tag)
		} else {
			tag := fmt.Sprintf("q%d", len(s.queries))
			s.queries = append(s.queries, s.currentQuery)
			s.readQueryOrder = append(s.readQueryOrder, tag)
		}
		s.currentQuery = doQuery{}
	case opEdit:
		if s.currentEdit.File != "" || s.currentEdit.Where != "" {
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
		Reads:           s.reads,
		Queries:         s.queries,
		Edits:           s.edits,
		Writes:          s.writes,
		PostEditReads:   s.postEditReads,
		PostEditQueries: s.postEditQueries,
		ReadQueryOrder:  s.readQueryOrder,
		PostEditOrder:   s.postEditOrder,
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
	}
	// No auto-verify in batch mode — use -V to opt in.

	if len(s.assertions) > 0 {
		p.Assertions = s.assertions
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
			root := s.root
			if root == "" {
				if envRoot := os.Getenv("EDR_ROOT"); envRoot != "" {
					root = envRoot
				} else {
					wd, _ := os.Getwd()
					root = discoverRoot(wd)
				}
			}
			if root != "" {
				resolved, err := index.ResolvePathReadOnly(root, path)
				if err != nil {
					return "", fmt.Errorf("%s: %w", flag, err)
				}
				path = resolved
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("%s: reading %s: %w", flag, path, err)
			}
			// Strip single trailing newline — editors and shell redirection
			// add one, but source text rarely ends with a bare newline.
			s := string(data)
			s = strings.TrimSuffix(s, "\n")
			return s, nil
		}
		return interpretEscapes(val), nil
	}

	for i < len(args) {
		arg := args[i]

		switch arg {
		// ── operations ──

		case "-f", "--focus", "-r", "--read":
			s.flush()
			s.currentOp = opRead
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			file, sym := splitFileArg(val)
			s.lastFile = file
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

		case "-o", "--orient", "-m", "--map":
			s.flush()
			s.currentOp = opMap
			// Map takes an optional file argument. Peek to see if next arg is a flag.
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				val, _ := nextArg(arg)
				s.currentQuery = doQuery{Cmd: "map", File: sp(val)}
			} else {
				s.currentQuery = doQuery{Cmd: "map"}
			}

		case "-q", "--query":
			s.flush()
			s.currentOp = opQuery
			// Next arg is the command name (search, orient, focus, etc.)
			cmdName, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			spec := cmdspec.ByName(cmdName)
			if spec == nil || spec.Category != cmdspec.CatRead {
				return nil, fmt.Errorf("-q: %q is not a valid query command (use search, orient, focus)", cmdName)
			}
			s.currentQuery = doQuery{Cmd: cmdName}
			// Consume the target argument (file:symbol) if present
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				val, _ := nextArg(arg)
				file, sym := splitFileArg(val)
				s.currentQuery.File = sp(file)
				if sym != "" {
					s.currentQuery.Symbol = sp(sym)
				}
			}

		case "-e", "--edit":
			s.flush()
			s.currentOp = opEdit
			// Peek: if next arg is a flag (starts with --), this is a file-less edit.
			// Use lastFile from the previous op (carry-forward).
			if i+1 < len(args) && strings.HasPrefix(args[i+1], "--") {
				s.currentEdit = doEdit{File: s.lastFile}
			} else {
				val, err := nextArg(arg)
				if err != nil {
					return nil, err
				}
				file, sym := splitFileArg(val)
				s.lastFile = file
				s.currentEdit = doEdit{File: file, Symbol: sym}
			}

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
			switch s.currentOp {
			case opRead:
				s.currentRead.Signatures = bp(true)
			case opQuery:
				s.currentQuery.Signatures = bp(true)
			default:
				return nil, fmt.Errorf("%s is only valid after -r or -q", arg)
			}

		case "--skeleton":
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--skeleton is only valid after -r")
			}
			s.currentRead.Skeleton = bp(true)

		case "--expand":
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--expand is only valid after -r")
			}
			// Peek: if next arg looks like a value (not a flag), consume it
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				val, _ := nextArg(arg)
				s.currentRead.Expand = val
			} else {
				s.currentRead.Expand = "deps"
			}

		case "--depth":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opRead:
				s.currentRead.Depth = ip(n)
			case opQuery:
				s.currentQuery.Depth = ip(n)
			default:
				return nil, fmt.Errorf("--depth is only valid after -r or -q")
			}

		case "--budget":
			n, err := nextInt(arg)
			if err != nil {
				return nil, err
			}
			switch s.currentOp {
			case opRead:
				s.currentRead.Budget = ip(n)
			case opSearch, opMap, opQuery:
				s.currentQuery.Budget = ip(n)
			default:
				return nil, fmt.Errorf("--budget is only valid after -r, -s, -m, or -q")
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
			case opSearch, opMap, opQuery:
				s.currentQuery.Full = bp(true)
			default:
				return nil, fmt.Errorf("--full is only valid after -r, -s, -m, or -q")
			}

		case "--symbols":
			if s.currentOp != opRead {
				return nil, fmt.Errorf("--symbols is only valid after -r")
			}
			s.currentRead.Symbols = bp(true)

		// ── search modifiers ──

		case "--body":
			switch s.currentOp {
			case opMap:
				val, err := nextArg(arg)
				if err != nil {
					return nil, err
				}
				s.currentQuery.BodySearch = sp(val)
			case opSearch, opQuery:
				s.currentQuery.Body = bp(true)
			default:
				return nil, fmt.Errorf("--body is only valid after -o/-m, -s, or -q")
			}

		case "--no-body":
			switch s.currentOp {
			case opSearch, opQuery:
				s.currentQuery.Body = bp(false)
			default:
				return nil, fmt.Errorf("--no-body is only valid after -s or -q")
			}

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

		case "--where":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--where is only valid after -e")
			}
			s.currentEdit.Where = val

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

		// ── query modifiers ──

		case "--impact":
			if s.currentOp != opQuery {
				return nil, fmt.Errorf("--impact is only valid after -q")
			}
			s.currentQuery.Impact = bp(true)

		case "--callers":
			if s.currentOp != opQuery {
				return nil, fmt.Errorf("--callers is only valid after -q")
			}
			s.currentQuery.Callers = bp(true)

		case "--deps":
			if s.currentOp != opQuery {
				return nil, fmt.Errorf("--deps is only valid after -q")
			}
			s.currentQuery.Deps = bp(true)

		case "--chain":
			if s.currentOp != opQuery {
				return nil, fmt.Errorf("--chain is only valid after -q")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Chain = sp(val)

		// ── map modifiers ──

		case "--dir":
			if s.currentOp != opMap {
				return nil, fmt.Errorf("--dir is only valid after -o/-m")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Dir = sp(val)

		case "--lang":
			if s.currentOp != opMap {
				return nil, fmt.Errorf("--lang is only valid after -o/-m")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Lang = sp(val)

		case "--grep":
			if s.currentOp != opMap {
				return nil, fmt.Errorf("--grep is only valid after -o/-m")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Grep = sp(val)

		case "--glob":
			if s.currentOp != opMap {
				return nil, fmt.Errorf("--glob is only valid after -o/-m")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Glob = sp(val)

		case "--type":
			if s.currentOp != opMap {
				return nil, fmt.Errorf("--type is only valid after -o/-m")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentQuery.Type = sp(val)

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
				if s.currentEdit.Delete != nil && *s.currentEdit.Delete {
					return nil, fmt.Errorf("--new cannot be combined with --delete (--delete means replace with empty string)")
				}
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
			if s.currentEdit.NewText != "" {
				return nil, fmt.Errorf("--delete cannot be combined with --new (--delete means replace with empty string)")
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

		case "--read-back", "--read_back":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--read-back is only valid after -e")
			}
			s.currentEdit.ReadBack = bp(true)

		case "--hash", "--expect-hash", "--expect_hash":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--hash is only valid after -e")
			}
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.currentEdit.ExpectHash = val

		case "--refresh-hash", "--refresh_hash":
			if s.currentOp != opEdit {
				return nil, fmt.Errorf("--refresh-hash is only valid after -e")
			}
			t := true
			s.currentEdit.RefreshHash = &t

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

		// ── assertions ──

		case "--assert-symbol-exists":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.assertions = append(s.assertions, doAssert{SymbolExists: val})

		case "--assert-symbol-absent":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.assertions = append(s.assertions, doAssert{SymbolAbsent: val})

		case "--assert-text-present":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.assertions = append(s.assertions, doAssert{TextPresent: val, File: s.lastFile})

		case "--assert-text-absent":
			val, err := nextArg(arg)
			if err != nil {
				return nil, err
			}
			s.assertions = append(s.assertions, doAssert{TextAbsent: val, File: s.lastFile})

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

	// Resolve root — discover repo root by walking up for .git
	root := state.root
	if root == "" {
		if envRoot := os.Getenv("EDR_ROOT"); envRoot != "" {
			root = envRoot
		} else {
			wd, _ := os.Getwd()
			root = discoverRoot(wd)
		}
	}
	if normalized, err := index.NormalizeRoot(root); err == nil {
		root = normalized
	}

	// Read-only batch
	// operations use the strict opener — same contract as standalone.
	var db index.SymbolStore
	db, err = openStore(root)
	if err != nil {
		return err
	}
	defer db.Close()

	edrDir := db.EdrDir()
	sess, saveSess := session.LoadSession(edrDir, db.Root())
	defer saveSess()

	// Opportunistic cleanup (rate-limited to once per hour)
	maybeCleanEdrDir(edrDir)

	cmdName := inferBatchCommand(&params)
	env := output.NewEnvelope(cmdName)
	if err := handleDo(context.Background(), db, sess, env, json.RawMessage(paramsJSON)); err != nil {
		return err
	}

	output.PrintEnvelope(env)
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
		if len(p.Queries) == 1 {
			name = p.Queries[0].Cmd
			if name == "" {
				name = "query"
			}
		} else {
			name = "query"
		}
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

// silentError signals non-zero exit without printing an additional error message.
// Only used by `run` (subprocess exit code passthrough) and `setup`.
// Agent-facing commands always exit 0; errors are in the JSON output.
type silentError struct{ code int }

func (e silentError) Error() string { return "" }
func (e silentError) ExitCode() int {
	if e.code == 0 {
		return 1
	}
	return e.code
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

// batchHelpText generates a structured help string from batchFlagOps and cmdspec.
func batchHelpText() string {
	var b strings.Builder

	b.WriteString(`Execute multiple operations in a single batch.

Operations:
  -f, --focus  FILE[:SYM]    Read file or symbol
  -o, --orient [DIR]         Structural overview
  -s, --search PATTERN       Search for text/symbols
  -e, --edit   FILE[:SYM]    Edit file (file carries forward from previous op)
  -w, --write  FILE          Write/create file
  -q, --query  CMD [TARGET]  Run query command (search, orient, focus)
  -V, --verify               Build check (opt-in, use -V to enable)

Modifier flags apply to the preceding operation.
`)

	// Build a combined description map: canonical name → desc, alias → desc.
	descs := make(map[string]string)
	for _, spec := range cmdspec.Registry {
		for _, f := range spec.Flags {
			cliName := strings.ReplaceAll(f.Name, "_", "-")
			if _, exists := descs[cliName]; !exists {
				descs[cliName] = f.Desc
			}
			if f.Alias != "" {
				alias := strings.ReplaceAll(f.Alias, "_", "-")
				if _, exists := descs[alias]; !exists {
					descs[alias] = f.Desc
				}
			}
		}
	}
	// Batch-only flags not in cmdspec registry.
	batchDescs := map[string]string{
		"context":        "Lines of context around matches",
		"include":        "Include files matching glob",
		"exclude":        "Exclude files matching glob",
		"limit":          "Max results",
		"no-body":        "Omit body from results",
		"no-group":       "Don't group results by file",
		"regex":          "Treat pattern as regex",
		"text":           "Plain text search (not symbol)",
		"callers":        "Show callers of symbol",
		"chain":          "Follow call chain expression",
		"deps":           "Show dependencies",
		"depth":          "Expansion depth",
		"impact":         "Show impact analysis",
		"read-after-edit": "Auto-focus files after edits",
		"root":           "Repository root directory",
		"verbose":        "Emit diagnostics to stderr",
		"delete":         "Delete matched text or symbol",
		"fuzzy":          "Allow whitespace mismatches",
		"hash":           "Reject edit if file hash mismatch",
		"refresh-hash":   "On hash mismatch, retry once",
		"insert-at":      "Insert new text before line N",
		"no-verify":      "Skip auto-verify on edits",
		"atomic":         "All-or-nothing edits",
	}
	for k, v := range batchDescs {
		descs[k] = v
	}

	// Build op → sorted flags from batchFlagOps.
	type opInfo struct {
		key   string
		title string
	}
	ops := []opInfo{
		{"-r", "Focus (-f)"},
		{"-m", "Orient (-o)"},
		{"-s", "Search (-s)"},
		{"-e", "Edit (-e)"},
		{"-w", "Write (-w)"},
		{"-q", "Query (-q)"},
		{"--verify", "Verify"},
		{"global", "Global"},
	}

	for _, op := range ops {
		var flags []string
		for flag, opList := range batchFlagOps {
			for _, o := range opList {
				if o == op.key {
					flags = append(flags, flag)
					break
				}
			}
		}
		if len(flags) == 0 {
			continue
		}

		// Deduplicate: resolve each flag to its canonical name,
		// keep only the shortest CLI form per canonical name.
		canonical := make(map[string]string) // canonical → shortest flag
		for _, f := range flags {
			bare := strings.TrimLeft(f, "-")
			canon := cmdspec.CanonicalFlagName(bare)
			if canon == "" {
				canon = bare
			}
			if prev, exists := canonical[canon]; !exists || len(f) < len(prev) {
				canonical[canon] = f
			}
		}
		// Collect and sort unique flags.
		unique := make([]string, 0, len(canonical))
		for _, f := range canonical {
			unique = append(unique, f)
		}
		for i := 0; i < len(unique); i++ {
			for j := i + 1; j < len(unique); j++ {
				if unique[i] > unique[j] {
					unique[i], unique[j] = unique[j], unique[i]
				}
			}
		}

		fmt.Fprintf(&b, "  %s:\n", op.title)
		for _, f := range unique {
			bare := strings.TrimLeft(f, "-")
			desc := descs[bare]
			if desc == "" {
				// Try canonical name.
				canon := cmdspec.CanonicalFlagName(bare)
				if canon != "" {
					desc = descs[canon]
				}
			}
			if desc != "" {
				fmt.Fprintf(&b, "    %-18s %s\n", f, desc)
			} else {
				fmt.Fprintf(&b, "    %s\n", f)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString(`Examples:
  edr -f cmd/root.go --sig -s "handleRequest"
  edr -e cmd/root.go --old "oldFunc" --new "newFunc" -V
  edr -f file.go:Sym -e --old "x" --new "y" -f file.go:Sym
  edr -s "TODO" --include "*.go" --limit 10
  edr --focus file.go:Func --expand callers
  edr -w new.go --content "package main" --mkdir`)

	return b.String()
}

// allBatchFlags returns every flag recognized by parseBatchArgs, grouped by
// which operation(s) they belong to. Used for "did you mean" suggestions.
var batchFlagOps = map[string][]string{
	"--sig":             {"-r", "-q"},
	"--signatures":      {"-r", "-q"},
	"--skeleton":        {"-r"},
	"--depth":           {"-r", "-q"},
	"--budget":          {"-r", "-s", "-m", "-q"},
	"--lines":           {"-r", "-e"},
	"--full":            {"-r", "-s", "-m", "-q"},
	"--symbols":         {"-r"},
	"--body":            {"-m", "-s", "-q"},
	"--no-body":         {"-s", "-q"},
	"--include":         {"-s"},
	"--exclude":         {"-s"},
	"--text":            {"-s"},
	"--context":         {"-s"},
	"--limit":           {"-s"},
	"--in":              {"-s", "-e"},
	"--where":           {"-e"},
	"--regex":           {"-s"},
	"--old":             {"-e"},
	"--old-text":        {"-e"},
	"--new":             {"-e", "-w"},
	"--new-text":        {"-e", "-w"},
	"--all":             {"-e"},
	"--after":           {"-w"},
	"--content":         {"-w"},
	"--inside":          {"-w"},
	"--mkdir":           {"-w"},
	"--append":          {"-w"},
	"--dry-run":         {"-e"},
	"--read-back":       {"-e"},
	"--delete":          {"-e"},
	"--fuzzy":           {"-e"},
	"--hash":            {"-e"},
	"--refresh-hash":    {"-e"},
	"--insert-at":       {"-e"},
	"--start-line":      {"-e"},
	"--end-line":        {"-e"},
	"--no-group":        {"-s"},
	"--expand":          {"-r"},
	"--no-verify":       {"global"},
	"--atomic":          {"global"},
	"--impact":          {"-q"},
	"--callers":         {"-q"},
	"--deps":            {"-q"},
	"--chain":           {"-q"},
	"--command":         {"--verify"},
	"--dir":             {"-m"},
	"--lang":            {"-m"},
	"--grep":            {"-m"},
	"--glob":            {"-m"},
	"--type":            {"-m"},
	"--read-after-edit": {"global"},
	"--root":            {"global"},
	"--verbose":         {"global"},
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
	// If the value already contains a real newline, the shell handled
	// quoting (multi-line string or $'...\n...'), so skip escape
	// processing to avoid corrupting literal backslash sequences in the
	// content (e.g. Go format strings like "foo:\n%v").
	if strings.ContainsAny(s, "\n\t") {
		return s
	}
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
