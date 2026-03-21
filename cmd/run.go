package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const maxRunOutput = 1 << 20 // 1MB cap for stored output

var runCmd = &cobra.Command{
	Use:   "run <command...>",
	Short: "Run a command, show only what changed since last run",
	Long: `Execute a shell command and diff output against the previous run.
First run shows full output. Subsequent runs show a sparse view:
unchanged regions collapse to [N unchanged lines], changed lines
show inline {old → new} markers. Use --full for raw output.`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		full, _ := cmd.Flags().GetBool("full")

		shellCmd := strings.Join(args, " ")

		c := exec.Command("sh", "-c", shellCmd)
		c.Dir = getRoot(cmd)
		out, execErr := c.CombinedOutput()

		if full {
			os.Stdout.Write(out)
			return exitError(execErr)
		}

		root := getRoot(cmd)
		runDir := filepath.Join(root, ".edr", "run")
		output := diffAgainstPrevious(runDir, shellCmd, string(out))
		fmt.Print(output)

		return exitError(execErr)
	},
}

func init() {
	runCmd.Flags().Bool("full", false, "Bypass diff, show full output")
	rootCmd.AddCommand(runCmd)
}

func exitError(err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return silentError{code: exitErr.ExitCode()}
	}
	return err
}

// diffAgainstPrevious diffs current output against the stored previous run.
// First run shows full output. Identical output prints "[no changes]".
// Otherwise shows sparse output with inline diffs.
func diffAgainstPrevious(runDir, command, current string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(command)))[:12]
	lastFile := filepath.Join(runDir, key+".last")

	prev, err := os.ReadFile(lastFile)
	hasPrev := err == nil

	// Store current (truncate from front if over cap)
	store := current
	if len(store) > maxRunOutput {
		store = store[len(store)-maxRunOutput:]
	}
	os.MkdirAll(runDir, 0755)
	os.WriteFile(lastFile, []byte(store), 0644)

	if !hasPrev {
		return current
	}

	prevStr := string(prev)
	if prevStr == current {
		lines := strings.Count(current, "\n")
		return fmt.Sprintf("[no changes, %d lines]\n", lines)
	}

	oldLines := strings.Split(strings.TrimRight(prevStr, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(current, "\n"), "\n")
	return sparseDiff(oldLines, newLines)
}

// sparseDiff produces a sparse version of the new output where unchanged
// regions are collapsed and changed lines show inline {old → new} markers.
// Uses positional alignment (line N vs line N).
func sparseDiff(oldLines, newLines []string) string {
	var result strings.Builder
	i := 0

	for i < len(newLines) {
		if i < len(oldLines) && newLines[i] == oldLines[i] {
			// Count consecutive unchanged
			count := 0
			for i < len(newLines) && i < len(oldLines) && newLines[i] == oldLines[i] {
				count++
				i++
			}
			fmt.Fprintf(&result, "[%d unchanged lines]\n", count)
		} else if i < len(oldLines) {
			// Modified — inline diff
			result.WriteString(inlineDiff(oldLines[i], newLines[i]))
			result.WriteByte('\n')
			i++
		} else {
			// Added
			fmt.Fprintf(&result, "{+ %s}\n", newLines[i])
			i++
		}
	}

	// Removed lines at end
	for i < len(oldLines) {
		fmt.Fprintf(&result, "{- %s}\n", oldLines[i])
		i++
	}

	return result.String()
}

// inlineDiff produces a line with {old → new} markers for changed segments.
// Unchanged prefix and suffix are kept as-is.
func inlineDiff(old, new string) string {
	// Find common prefix
	pfx := 0
	for pfx < len(old) && pfx < len(new) && old[pfx] == new[pfx] {
		pfx++
	}
	// Find common suffix
	sfx := 0
	for sfx < len(old)-pfx && sfx < len(new)-pfx && old[len(old)-1-sfx] == new[len(new)-1-sfx] {
		sfx++
	}

	oldMid := old[pfx : len(old)-sfx]
	newMid := new[pfx : len(new)-sfx]

	if oldMid == "" && newMid == "" {
		return new
	}

	var b strings.Builder
	b.WriteString(new[:pfx])
	b.WriteString("{")
	b.WriteString(oldMid)
	b.WriteString(" → ")
	b.WriteString(newMid)
	b.WriteString("}")
	b.WriteString(new[len(new)-sfx:])
	return b.String()
}
