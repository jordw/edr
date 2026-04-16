package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
	"github.com/spf13/cobra"
)

var txnCmd = &cobra.Command{
	Use:   "txn",
	Short: "Multi-op transactions with preview and atomic rollback",
	Long: `A transaction anchors a rollback point at begin, then accumulates the
pre-mutation content of every file touched by subsequent ops. ` + "`" + `txn diff` + "`" +
		` shows the consolidated change; ` + "`" + `txn commit` + "`" + ` releases the anchor;
` + "`" + `txn rollback` + "`" + ` restores all files to their pre-begin state.`,
	Hidden: true,
}

var txnBeginCmd = &cobra.Command{
	Use:   "begin",
	Short: "Start a transaction; returns its checkpoint id",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")
		_ = os.MkdirAll(sessDir, 0o700)

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		if sess.ActiveTxn != "" {
			env := output.NewEnvelope("txn")
			env.AddFailedOpWithCode("t0", "txn", "txn_already_active",
				fmt.Sprintf("transaction %q is already active; commit or rollback first", sess.ActiveTxn))
			env.ComputeOK()
			output.PrintEnvelope(env)
			return silentError{code: 1}
		}

		cp, err := sess.CreateCheckpoint(sessDir, root, "txn", nil)
		if err != nil {
			return err
		}
		sess.ActiveTxn = cp.ID

		env := output.NewEnvelope("txn")
		env.AddOp("t0", "txn", map[string]any{
			"status": "began",
			"id":     cp.ID,
		})
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

var txnCommitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Release the current transaction's rollback anchor",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		if sess.ActiveTxn == "" {
			env := output.NewEnvelope("txn")
			env.AddFailedOpWithCode("t0", "txn", "no_active_txn", "no active transaction")
			env.ComputeOK()
			output.PrintEnvelope(env)
			return silentError{code: 1}
		}

		cpID := sess.ActiveTxn
		_ = session.DropCheckpoint(sessDir, cpID)
		sess.ActiveTxn = ""

		env := output.NewEnvelope("txn")
		env.AddOp("t0", "txn", map[string]any{
			"status": "committed",
			"id":     cpID,
		})
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

var txnRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Restore all files to their pre-begin state",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		if sess.ActiveTxn == "" {
			env := output.NewEnvelope("txn")
			env.AddFailedOpWithCode("t0", "txn", "no_active_txn", "no active transaction")
			env.ComputeOK()
			output.PrintEnvelope(env)
			return silentError{code: 1}
		}

		cpID := sess.ActiveTxn
		dirtyFiles := sess.GetDirtyFiles()
		restored, _, _, err := sess.RestoreCheckpoint(sessDir, root, cpID, false, dirtyFiles)
		if err != nil {
			return err
		}
		_ = session.DropCheckpoint(sessDir, cpID)
		sess.ActiveTxn = ""

		env := output.NewEnvelope("txn")
		env.AddOp("t0", "txn", map[string]any{
			"status":   "rolled_back",
			"id":       cpID,
			"restored": restored,
		})
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

var txnDiffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Consolidated unified diff from transaction begin to now",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := index.HomeEdrDir(root)
		sessDir := filepath.Join(edrDir, "sessions")

		sess, saveSess := session.LoadSession(edrDir, root)
		defer saveSess()

		if sess.ActiveTxn == "" {
			env := output.NewEnvelope("txn")
			env.AddFailedOpWithCode("t0", "txn", "no_active_txn", "no active transaction")
			env.ComputeOK()
			output.PrintEnvelope(env)
			return silentError{code: 1}
		}

		cp, err := session.LoadCheckpoint(sessDir, sess.ActiveTxn)
		if err != nil {
			return err
		}

		var diff string
		for _, snap := range cp.Files {
			abs := filepath.Join(root, snap.Path)
			current, readErr := os.ReadFile(abs)
			switch {
			case readErr != nil && snap.Content == nil:
				continue
			case readErr != nil:
				diff += edit.UnifiedDiff(snap.Path, snap.Content, nil)
			case snap.Content == nil:
				diff += edit.UnifiedDiff(snap.Path, nil, current)
			default:
				diff += edit.UnifiedDiff(snap.Path, snap.Content, current)
			}
		}

		env := output.NewEnvelope("txn")
		env.AddOp("t0", "txn", map[string]any{
			"id":    sess.ActiveTxn,
			"files": len(cp.Files),
			"diff":  diff,
		})
		env.ComputeOK()
		output.PrintEnvelope(env)
		return nil
	},
}

func init() {
	txnCmd.AddCommand(txnBeginCmd)
	txnCmd.AddCommand(txnCommitCmd)
	txnCmd.AddCommand(txnRollbackCmd)
	txnCmd.AddCommand(txnDiffCmd)
	rootCmd.AddCommand(txnCmd)
}
