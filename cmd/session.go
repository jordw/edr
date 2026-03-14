package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jordw/edr/internal/session"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(sessionCmd)
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionClearCmd)
	sessionCmd.AddCommand(sessionGCCmd)
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage persistent sessions",
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions",
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")
		tokens, err := session.ListSessions(edrDir)
		if err != nil {
			return err
		}
		if len(tokens) == 0 {
			fmt.Println("No active sessions.")
			return nil
		}
		for _, tok := range tokens {
			path, _ := session.SessionPath(edrDir, tok)
			info, err := os.Stat(path)
			if err != nil {
				fmt.Printf("  %s\n", tok)
				continue
			}
			fmt.Printf("  %s  (%d bytes, modified %s)\n", tok, info.Size(), info.ModTime().Format("2006-01-02 15:04:05"))
		}
		return nil
	},
}

var sessionClearCmd = &cobra.Command{
	Use:   "clear <token>",
	Short: "Delete a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")
		if err := session.ClearSession(edrDir, args[0]); err != nil {
			return err
		}
		fmt.Printf("Cleared session %s\n", args[0])
		return nil
	},
}

var sessionGCCmd = &cobra.Command{
	Use:   "gc",
	Short: "Delete sessions with dead PID tokens",
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")
		cleared, err := session.GCSessions(edrDir)
		if err != nil {
			return err
		}
		if len(cleared) == 0 {
			fmt.Println("No stale sessions to clean up.")
			return nil
		}
		for _, tok := range cleared {
			fmt.Printf("  Cleared %s\n", tok)
		}
		return nil
	},
}
