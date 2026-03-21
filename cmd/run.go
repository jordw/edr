package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jordw/edr/internal/session"
	"github.com/spf13/cobra"
)

var digitRe = regexp.MustCompile(`[0-9]+`)

var runCmd = &cobra.Command{
	Use:   "run <command...>",
	Short: "Run a command with output dedup across calls",
	Long: `Execute a shell command and deduplicate output blocks against the session.
Blocks separated by blank lines are hashed. Previously seen blocks are
replaced with [unchanged: N lines]. Use --full to bypass dedup.`,
	Args:                  cobra.MinimumNArgs(1),
	DisableFlagParsing:    false,
	RunE: func(cmd *cobra.Command, args []string) error {
		full, _ := cmd.Flags().GetBool("full")
		fuzzy, _ := cmd.Flags().GetBool("fuzzy")

		// Build the shell command
		shellCmd := strings.Join(args, " ")

		// Execute
		c := exec.Command("sh", "-c", shellCmd)
		c.Dir = getRoot(cmd)
		out, execErr := c.CombinedOutput()

		if full {
			os.Stdout.Write(out)
			if execErr != nil {
				if exitErr, ok := execErr.(*exec.ExitError); ok {
					return silentError{code: exitErr.ExitCode()}
				}
				return execErr
			}
			return nil
		}

		// Load session for dedup
		root := getRoot(cmd)
		edrDir := filepath.Join(root, ".edr")
		sess, saveSess := session.LoadSession(edrDir)
		defer saveSess()

		// Dedup by blocks
		key := "run:" + shellCmd
		output := dedupBlocks(sess, key, string(out), fuzzy)
		fmt.Print(output)

		if execErr != nil {
			if exitErr, ok := execErr.(*exec.ExitError); ok {
				return silentError{code: exitErr.ExitCode()}
			}
			return execErr
		}
		return nil
	},
}

func init() {
	runCmd.Flags().Bool("full", false, "Bypass dedup, show full output")
	runCmd.Flags().Bool("fuzzy", false, "Normalize numbers before hashing (tolerates timing changes)")
	rootCmd.AddCommand(runCmd)
}

// dedupBlocks splits output into blank-line-separated blocks, deduplicates
// against the session, and returns the filtered output.
func dedupBlocks(sess *session.Session, key, text string, fuzzy bool) string {
	if text == "" {
		return ""
	}

	blocks := splitBlocks(text)
	var result strings.Builder
	unchangedLines := 0

	for _, block := range blocks {
		hashInput := block
		if fuzzy {
			hashInput = digitRe.ReplaceAllString(block, "N")
		}
		h := session.ContentHash(hashInput)
		sessKey := key + ":" + h

		if sess.IsBlockSeen(sessKey) {
			unchangedLines += strings.Count(block, "\n") + 1
			continue
		}

		// Flush any pending unchanged count
		if unchangedLines > 0 {
			fmt.Fprintf(&result, "[unchanged: %d lines]\n\n", unchangedLines)
			unchangedLines = 0
		}

		sess.MarkBlockSeen(sessKey)
		result.WriteString(block)
		result.WriteString("\n\n")
	}

	if unchangedLines > 0 {
		fmt.Fprintf(&result, "[unchanged: %d lines]\n", unchangedLines)
	}

	return strings.TrimRight(result.String(), "\n") + "\n"
}

// splitBlocks splits text into blocks separated by blank lines.
func splitBlocks(text string) []string {
	text = strings.TrimRight(text, "\n")
	var blocks []string
	var current strings.Builder

	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			if current.Len() > 0 {
				blocks = append(blocks, current.String())
				current.Reset()
			}
			continue
		}
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		blocks = append(blocks, current.String())
	}
	return blocks
}
