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
First run shows full output. Subsequent runs show only the diff.
If output is identical, prints "[no changes]".
Use --full to bypass diffing and show raw output.`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		full, _ := cmd.Flags().GetBool("full")

		shellCmd := strings.Join(args, " ")

		// Execute the command
		c := exec.Command("sh", "-c", shellCmd)
		c.Dir = getRoot(cmd)
		out, execErr := c.CombinedOutput()

		if full {
			os.Stdout.Write(out)
			return exitError(execErr)
		}

		// Diff against previous run
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

// diffAgainstPrevious loads the previous output for this command,
// computes a line-level diff, and returns the appropriate display.
// Stores current output for the next run.
func diffAgainstPrevious(runDir, command, current string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(command)))[:12]
	lastFile := filepath.Join(runDir, key+".last")

	// Load previous output
	prev, err := os.ReadFile(lastFile)
	hasPrev := err == nil

	// Store current output (truncate from front if over cap)
	storeCurrent := current
	if len(storeCurrent) > maxRunOutput {
		storeCurrent = storeCurrent[len(storeCurrent)-maxRunOutput:]
	}
	os.MkdirAll(runDir, 0755)
	os.WriteFile(lastFile, []byte(storeCurrent), 0644)

	// First run — show full output
	if !hasPrev {
		return current
	}

	prevStr := string(prev)

	// Identical — suppress entirely
	if prevStr == current {
		lines := strings.Count(current, "\n")
		return fmt.Sprintf("[no changes, %d lines]\n", lines)
	}

	// Compute line diff
	return lineDiff(prevStr, current)
}

// lineDiff computes a compact diff between old and new text.
// If the diff is larger than 80% of the new text, returns full output instead.
func lineDiff(old, new string) string {
	oldLines := strings.Split(strings.TrimRight(old, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(new, "\n"), "\n")

	// Simple LCS-based diff using the Hunt-McIlroy algorithm approach.
	// Build a map of old line → positions for matching.
	type hunk struct {
		oldStart, oldCount int
		newStart, newCount int
		lines              []string // prefixed with +, -, or space
	}

	// Use patience-style: match identical lines, collect hunks of differences.
	hunks := computeHunks(oldLines, newLines)

	if len(hunks) == 0 {
		// Only whitespace/trailing differences
		lines := len(newLines)
		return fmt.Sprintf("[no changes, %d lines]\n", lines)
	}

	// Build unified-style diff output
	var result strings.Builder
	totalDiffLines := 0

	for _, h := range hunks {
		// Hunk header
		fmt.Fprintf(&result, "@@ -%d,%d +%d,%d @@\n", h.oldStart+1, h.oldCount, h.newStart+1, h.newCount)
		for _, line := range h.lines {
			result.WriteString(line)
			result.WriteByte('\n')
			totalDiffLines++
		}
	}

	// If diff is bigger than 80% of new output, just show full output
	if totalDiffLines > len(newLines)*80/100 {
		return strings.Join(newLines, "\n") + "\n"
	}

	unchangedLines := len(newLines) - totalDiffLines
	if unchangedLines > 0 {
		fmt.Fprintf(&result, "[%d unchanged lines omitted]\n", unchangedLines)
	}

	return result.String()
}

// computeHunks computes diff hunks between old and new line slices.
// Returns hunks with context showing what changed.
func computeHunks(old, new []string) []hunk {
	// Compute LCS using standard DP (fine for outputs up to ~10K lines)
	m, n := len(old), len(new)

	// For very large outputs, fall back to showing full
	if m*n > 10_000_000 {
		return []hunk{{
			oldStart: 0, oldCount: m,
			newStart: 0, newCount: n,
			lines: prefixLines(new, "+"),
		}}
	}

	// Build edit script via LCS
	lcs := computeLCS(old, new)

	// Walk through both sequences, producing hunks where they differ
	var hunks []hunk
	var current *hunk
	oi, ni, li := 0, 0, 0
	contextLines := 3

	flushHunk := func() {
		if current != nil && len(current.lines) > 0 {
			hunks = append(hunks, *current)
			current = nil
		}
	}

	for oi < m || ni < n {
		// Check if current lines match the next LCS element
		if li < len(lcs) && oi < m && ni < n && old[oi] == lcs[li] && new[ni] == lcs[li] {
			// Matching line — context
			if current != nil {
				current.lines = append(current.lines, " "+old[oi])
				current.oldCount++
				current.newCount++
				// If we've had enough trailing context, close the hunk
				trailingContext := 0
				for j := len(current.lines) - 1; j >= 0 && current.lines[j][0] == ' '; j-- {
					trailingContext++
				}
				if trailingContext >= contextLines {
					flushHunk()
				}
			}
			oi++
			ni++
			li++
			continue
		}

		// Difference — start or extend a hunk
		if current == nil {
			// Add leading context from already-matched lines
			current = &hunk{oldStart: oi, newStart: ni}
			start := oi - contextLines
			if start < 0 {
				start = 0
			}
			for i := start; i < oi; i++ {
				current.lines = append(current.lines, " "+old[i])
				current.oldCount++
				current.newCount++
				current.oldStart = i
				current.newStart = ni - (oi - i)
			}
		}

		// Consume removed lines (in old but not matching)
		for oi < m && (li >= len(lcs) || old[oi] != lcs[li]) {
			current.lines = append(current.lines, "-"+old[oi])
			current.oldCount++
			oi++
		}

		// Consume added lines (in new but not matching)
		for ni < n && (li >= len(lcs) || new[ni] != lcs[li]) {
			current.lines = append(current.lines, "+"+new[ni])
			current.newCount++
			ni++
		}
	}

	flushHunk()
	return hunks
}

type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []string
}

// computeLCS returns the longest common subsequence of a and b.
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	// DP table — use rolling two rows to save memory
	prev := make([]int, n+1)
	curr := make([]int, n+1)

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
			} else if prev[j] > curr[j-1] {
				curr[j] = prev[j]
			} else {
				curr[j] = curr[j-1]
			}
		}
		prev, curr = curr, make([]int, n+1)
	}

	// Backtrack to recover LCS (need full table for this)
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
	// Reverse
	for l, r := 0, len(result)-1; l < r; l, r = l+1, r-1 {
		result[l], result[r] = result[r], result[l]
	}
	return result
}

func prefixLines(lines []string, prefix string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = prefix + l
	}
	return out
}
