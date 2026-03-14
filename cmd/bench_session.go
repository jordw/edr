package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/trace"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(benchSessionCmd)
}

var benchSessionCmd = &cobra.Command{
	Use:   "bench-session [session-id]",
	Short: "Score a completed trace session",
	Long: `Scores a completed session from traces.db.
Without a session-id argument, scores the most recent session.

Outputs structured JSON with call counts, token estimates,
edit/verify pass rates, and optimization savings.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)

		edrDir := filepath.Join(root, ".edr")
		db, err := trace.OpenTraceDB(edrDir)
		if err != nil {
			return fmt.Errorf("open traces.db: %w", err)
		}
		defer db.Close()

		sessionID := ""
		if len(args) > 0 {
			sessionID = args[0]
		}

		result, err := trace.BenchSession(db, sessionID)
		if err != nil {
			return err
		}

		output.Print(result)
		return nil
	},
}
