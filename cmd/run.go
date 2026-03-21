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
	// Phase 1: Classify each line using LCS alignment.
	type entry struct {
		kind    string // "same", "modified", "added", "removed"
		newText string
		oldText string
		digitOnly bool // true if modification is digits-only
	}
	var entries []entry

	lcs := computeLCS(oldLines, newLines)
	oi, ni, li := 0, 0, 0

	for oi < len(oldLines) || ni < len(newLines) {
		if li < len(lcs) && oi < len(oldLines) && ni < len(newLines) &&
			oldLines[oi] == lcs[li] && newLines[ni] == lcs[li] {
			entries = append(entries, entry{kind: "same", newText: newLines[ni]})
			oi++
			ni++
			li++
			continue
		}

		// Collect non-LCS runs from both sides
		oldEnd := oi
		for oldEnd < len(oldLines) && (li >= len(lcs) || oldLines[oldEnd] != lcs[li]) {
			oldEnd++
		}
		newEnd := ni
		for newEnd < len(newLines) && (li >= len(lcs) || newLines[newEnd] != lcs[li]) {
			newEnd++
		}

		// Pair modifications
		for oi < oldEnd && ni < newEnd {
			entries = append(entries, entry{
				kind:      "modified",
				newText:   newLines[ni],
				oldText:   oldLines[oi],
				digitOnly: isDigitOnlyChange(oldLines[oi], newLines[ni]),
			})
			oi++
			ni++
		}
		for oi < oldEnd {
			entries = append(entries, entry{kind: "removed", oldText: oldLines[oi]})
			oi++
		}
		for ni < newEnd {
			entries = append(entries, entry{kind: "added", newText: newLines[ni]})
			ni++
		}
	}

	// Phase 2: Render with collapsing.
	// Merge adjacent "same" and "digit-only modified" into one summary.
	var result strings.Builder
	i := 0
	for i < len(entries) {
		e := entries[i]

		// Collapsible: unchanged or digit-only modified
		if e.kind == "same" || (e.kind == "modified" && e.digitOnly) {
			total := 0
			numChanged := 0
			for i < len(entries) {
				ei := entries[i]
				if ei.kind == "same" {
					total++
					i++
				} else if ei.kind == "modified" && ei.digitOnly {
					total++
					numChanged++
					i++
				} else {
					break
				}
			}
			if numChanged == 0 {
				fmt.Fprintf(&result, "[%d unchanged lines]\n", total)
			} else {
				fmt.Fprintf(&result, "[%d lines, %d with numbers changed]\n", total, numChanged)
			}
			continue
		}

		switch e.kind {
		case "modified":
			result.WriteString(inlineDiff(e.oldText, e.newText))
			result.WriteByte('\n')
			i++

		case "added":
			fmt.Fprintf(&result, "{+ %s}\n", e.newText)
			i++

		case "removed":
			fmt.Fprintf(&result, "{- %s}\n", e.oldText)
			i++
		}
	}

	return result.String()
}

// isDigitOnlyChange returns true if two strings of equal length differ
// only at byte positions where at least one side has a digit.
// isDigitOnlyChange returns true if two lines have the same non-digit
// skeleton. "test 18 in 0.003s" vs "test 9 in 0.005s" → both strip to
// "test  in .s" → digit-only change, even though lengths differ.
func isDigitOnlyChange(a, b string) bool {
	return a != b && stripDigits(a) == stripDigits(b)
}

func stripDigits(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			b = append(b, s[i])
		}
	}
	return string(b)
}

// computeLCS returns the longest common subsequence of two string slices.
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	// For very large outputs, skip LCS (O(m*n) memory)
	if m*n > 10_000_000 {
		return nil
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	result := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			result = append(result, a[i-1])
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	for l, r := 0, len(result)-1; l < r; l, r = l+1, r-1 {
		result[l], result[r] = result[r], result[l]
	}
	return result
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
