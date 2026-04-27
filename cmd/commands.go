package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	scopestore "github.com/jordw/edr/internal/scope/store"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/status"
	"github.com/jordw/edr/internal/warnings"
	"github.com/spf13/cobra"
)

func init() {
	// Primary commands
	rootCmd.AddCommand(orientCmd)
	rootCmd.AddCommand(focusCmd)
	rootCmd.AddCommand(editCmd)
	rootCmd.AddCommand(renameCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(undoCmd)
	rootCmd.AddCommand(setupCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(filesCmd)
}

// =====================================================================
// Commands
// =====================================================================

var orientCmd = &cobra.Command{
	Use:   "orient [path]",
	Short: ToolDesc["orient"],
	Args:  cobra.MaximumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "orient", args) },
}

func init() { cmdspec.RegisterFlags(orientCmd.Flags(), "orient") }

var focusCmd = &cobra.Command{
	Use:   "focus <file>[:<symbol>] [<file>...] [flags]",
	Short: ToolDesc["focus"],
	Args:  cobra.MinimumNArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "focus", args) },
}

func init() {
	cmdspec.RegisterFlags(focusCmd.Flags(), "focus")
	if expand := focusCmd.Flags().Lookup("expand"); expand != nil {
		expand.NoOptDefVal = "deps"
	}
}

var editCmd = &cobra.Command{
	Use:   "edit [file[:symbol]]",
	Short: ToolDesc["edit"],
	Args: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("where") && len(args) == 0 {
			return nil
		}
		if len(args) >= 1 && len(args) <= 2 {
			return nil
		}
		// If --content is set, this is write mode — need exactly 1 file arg
		if cmd.Flags().Changed("content") || cmd.Flags().Changed("inside") || cmd.Flags().Changed("after") {
			if len(args) == 1 {
				return nil
			}
		}
		return fmt.Errorf("accepts between 1 and 2 arg(s), received %d", len(args))
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return dispatchCmdWithStdin(cmd, "edit", args, "new_text")
	},
}

func init() { cmdspec.RegisterFlags(editCmd.Flags(), "edit") }

var renameCmd = &cobra.Command{
	Use:   "rename <file:symbol> --to <new_name>",
	Short: ToolDesc["rename"],
	Args:  cobra.RangeArgs(1, 2),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "rename", args) },
}

func init() { cmdspec.RegisterFlags(renameCmd.Flags(), "rename") }

var statusCmd = &cobra.Command{
	Use:     "status",
	Aliases: []string{"context"},
	Short:   "Session status: build state, stale assumptions, external changes",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		flags := extractFlags(cmd)

		// Handle --reset: clear session and checkpoints
		if cmd.Flags().Changed("reset") {
			id := session.GenerateID()
			newSess := session.New()
			path := filepath.Join(edrDir, "sessions", id+".json")
			os.MkdirAll(filepath.Join(edrDir, "sessions"), 0700)
			newSess.SaveToFile(path)
			session.WriteSessionMapping(filepath.Join(edrDir, "sessions"), id)
			os.RemoveAll(filepath.Join(edrDir, "checkpoints"))
			cleanEdrDir(edrDir)
			result := map[string]any{"status": "reset", "session": id}
			env := output.NewEnvelope("status")
			env.AddOp("s0", "reset", result)
			env.ComputeOK()
			output.PrintEnvelope(env)
			return nil
		}

		// Handle --focus: set/clear focus string
		if cmd.Flags().Changed("focus") {
			focusVal, _ := flags["focus"].(string)
			sess.SetFocus(focusVal)
		}

		// Open DB for assumption checking (best-effort — status works without it)
		var db index.SymbolStore
		db, _ = openStore(root)
		if db != nil {
			defer db.Close()
		}

		result := buildNextResult(sess, db, root, edrDir)

		if flagBool, _ := flags["debug"].(bool); flagBool {
			sessDir := filepath.Join(edrDir, "sessions")
			sessionID := session.ResolveSessionID()
			result["debug"] = map[string]any{
				"root":       root,
				"edr_dir":    edrDir,
				"sess_dir":   sessDir,
				"session_id": sessionID,
				"sess_file":  filepath.Join(sessDir, sessionID+".json"),
				"checkpoints": func() []string {
					infos := session.ListCheckpoints(sessDir)
					ids := make([]string, len(infos))
					for i, c := range infos {
						ids[i] = c.ID
					}
					return ids
				}(),
			}
		}

		env := output.NewEnvelope("status")
		env.AddOp("s0", "status", result)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(statusCmd.Flags(), "status") }

// sessionCmd is a hidden backward-compatibility command.
// "edr session new" is now "edr reset --session".
var sessionCmd = &cobra.Command{
	Use:    "session",
	Short:  "Manage sessions (use reset --session instead)",
	Hidden: true,
}

var sessionNewCmd = &cobra.Command{
	Use:   "new",
	Short: "Create a new session (use reset --session instead)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")
		os.MkdirAll(sessDir, 0700)
		id := session.GenerateID()
		sess := session.New()
		path := filepath.Join(sessDir, id+".json")
		if err := sess.SaveToFile(path); err != nil {
			return err
		}
		session.WriteSessionMapping(filepath.Join(edrDir, "sessions"), id)
		fmt.Printf("{\"id\":%q}\n", id)
		cleanEdrDir(edrDir)
		return nil
	},
}

func init() {
	sessionCmd.AddCommand(sessionNewCmd)
	rootCmd.AddCommand(sessionCmd)
}

var undoCmd = &cobra.Command{
	Use:   "undo",
	Short: "Revert to the last auto-checkpoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		flags := extractFlags(cmd)
		noSave, _ := flags["no_save"].(bool)

		cpID := session.LatestAutoCheckpoint(sessDir)
		if cpID == "" {
			env := output.NewEnvelope("undo")
			env.AddFailedOpWithCode("u0", "undo", "no_checkpoint", "no auto-checkpoint found; nothing to undo")
			env.ComputeOK()
			output.PrintEnvelope(env)
			return silentError{code: 1}
		}

		dirtyFiles := sess.GetDirtyFiles()
		restored, notRemoved, preRestoreID, err := sess.RestoreCheckpoint(
			sessDir, root, cpID, !noSave, dirtyFiles,
		)
		if err != nil {
			return err
		}

		// Drop the auto-checkpoint we just restored (it is consumed)
		session.DropCheckpoint(sessDir, cpID)

		result := map[string]any{
			"status":   "undone",
			"target":   cpID,
			"restored": restored,
		}
		if preRestoreID != "" {
			result["safety_checkpoint"] = preRestoreID
		}
		// Report remaining checkpoints so the agent knows how many undos are left
		remaining := session.ListCheckpoints(sessDir)
		// Filter to only auto checkpoints (cp_auto_*)
		autoCount := 0
		for _, cp := range remaining {
			if strings.HasPrefix(cp.ID, "cp_auto_") {
				autoCount++
			}
		}
		result["remaining"] = autoCount
		// Files modified after the checkpoint that weren't snapshotted.
		// Do NOT delete them — they may be pre-existing files that a
		// multi-file rename modified. Only files with nil content in the
		// checkpoint (truly new files) are deleted by RestoreCheckpoint itself.
		if len(notRemoved) > 0 {
			result["unrestored"] = notRemoved
		}

		env := output.NewEnvelope("undo")
		env.AddOp("u0", "undo", result)
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() { cmdspec.RegisterFlags(undoCmd.Flags(), "undo") }

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: ToolDesc["index"],
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "index", args) },
}

func init() { cmdspec.RegisterFlags(indexCmd.Flags(), "index") }

var filesCmd = &cobra.Command{
	Use:   "files <pattern>",
	Short: ToolDesc["files"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "files", args) },
}

func init() { cmdspec.RegisterFlags(filesCmd.Flags(), "files") }

var refsToCmd = &cobra.Command{
	Use:   "refs-to <file:Symbol>",
	Short: ToolDesc["refs-to"],
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "refs-to", args) },
}

func init() {
	rootCmd.AddCommand(refsToCmd)
	cmdspec.RegisterFlags(refsToCmd.Flags(), "refs-to")
}

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: ToolDesc["bench"],
	Args:  cobra.NoArgs,
	RunE:  func(cmd *cobra.Command, args []string) error { return dispatchCmd(cmd, "bench", args) },
}

func init() { rootCmd.AddCommand(benchCmd) }

// buildNextResult constructs the result map for `edr next`.
func buildNextResult(sess *session.Session, db index.SymbolStore, root, edrDir string) map[string]any {
	result := map[string]any{}

	// Always show root so agents know which repo context they're in.
	result["root"] = output.Rel(root)

	// Storage health: index + scope + session share a uniform Reporter
	// contract in internal/status. Each block below is populated from an
	// Aggregate call; the legacy "index" shape (files, complete, symbols)
	// is preserved for agents that already depend on it, while scope and
	// session are additive.
	reports := status.Aggregate(
		idx.NewReporter(root, edrDir),
		scopestore.NewReporter(edrDir),
		session.NewReporter(edrDir),
	)
	for _, rep := range reports {
		switch rep.Name {
		case "index":
			idxInfo := map[string]any{
				"files":    rep.Files,
				"complete": idx.IsComplete(root, edrDir),
			}
			if syms, ok := rep.Extra["symbols"].(int); ok {
				idxInfo["symbols"] = syms
			}
			result["index"] = idxInfo
		case "scope":
			if rep.Exists {
				result["scope"] = map[string]any{
					"files": rep.Files,
					"bytes": rep.Bytes,
				}
			}
		case "session":
			if rep.Exists {
				sessInfo := map[string]any{
					"files": rep.Files,
					"bytes": rep.Bytes,
				}
				if cp, ok := rep.Extra["checkpoints"].(int); ok {
					sessInfo["checkpoints"] = cp
				}
				result["session"] = sessInfo
			}
		}
	}

	// Undo availability
	sessDir := filepath.Join(edrDir, "sessions")
	cpID := session.LatestAutoCheckpoint(sessDir)
	if cpID != "" {
		result["undo_available"] = true
	}
	// Focus
	if focus := sess.GetFocus(); focus != "" {
		result["focus"] = focus
	}

	// Build state
	buildStatus, editsSince := sess.BuildState()
	if buildStatus != "" {
		build := map[string]any{"status": buildStatus}
		if editsSince {
			build["edits_since"] = true
		}
		result["build"] = build
	}

	// Stale assumptions (fix items)
	if db != nil {
		fix := computeFixItems(sess, db)
		if len(fix) > 0 {
			result["fix"] = fix
		}
	}

	// External file modifications
	extMods := warnings.Check(sess, root)
	if len(extMods) > 0 {
		var items []any
		for _, w := range extMods {
			items = append(items, map[string]any{
				"file":    w.File,
				"kind":    w.Kind,
				"since":   w.OpID,
				"message": w.Message,
			})
		}
		result["external_changes"] = items
	}

	return result
}

// computeStaleAssumptions resolves current signatures for all tracked assumptions
// and returns any that have become stale. Shared by computeFixItems and emitWarnings.
func computeStaleAssumptions(sess *session.Session, db index.SymbolStore) []session.StaleAssumption {
	assumptions := sess.GetAssumptions()
	if len(assumptions) == 0 {
		return nil
	}

	currentSigs := make(map[string]string, len(assumptions))
	ctx := context.Background()
	for key := range assumptions {
		idx := strings.IndexByte(key, ':')
		if idx <= 0 {
			continue
		}
		file, symName := key[:idx], key[idx+1:]

		absFile, err := db.ResolvePath(file)
		if err != nil {
			continue
		}
		syms, err := db.GetSymbolsByFile(ctx, absFile)
		if err != nil {
			continue
		}
		src, err := os.ReadFile(absFile)
		if err != nil {
			continue
		}
		for _, sym := range syms {
			if sym.Name == symName {
				sig := index.ExtractSignatureFromSource(sym, src)
				currentSigs[key] = session.SigHash(sig)
				break
			}
		}
	}

	return sess.CheckAssumptions(currentSigs)
}

func computeFixItems(sess *session.Session, db index.SymbolStore) []any {
	stale := computeStaleAssumptions(sess, db)
	if len(stale) == 0 {
		return nil
	}

	var fix []any
	for i, s := range stale {
		item := map[string]any{
			"id":         fmt.Sprintf("stale_%d", i+1),
			"type":       "stale_assumption",
			"confidence": "exact",
			"file":       s.File,
			"symbol":     s.Symbol,
			"assumed_at": s.AssumedAt,
			"suggest":    fmt.Sprintf("read %s:%s", s.File, s.Symbol),
		}
		if s.Current == "" {
			item["reason"] = "symbol no longer exists"
		} else {
			item["reason"] = "signature changed since read"
		}
		fix = append(fix, item)
	}
	return fix
}
