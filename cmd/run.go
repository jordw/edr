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
// Otherwise shows a unified diff with context.
func diffAgainstPrevious(runDir, command, current string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(command)))[:12]
	lastFile := filepath.Join(runDir, key+".last")

	prev, err := os.ReadFile(lastFile)
	hasPrev := err == nil

	// Store current (truncate from front if over 1MB)
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

	return lineDiff(prevStr, current)
}

// lineDiff produces a unified diff between old and new.
// If the diff is >80% of the output, shows full output instead.
func lineDiff(old, new string) string {
	oldLines := strings.Split(strings.TrimRight(old, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(new, "\n"), "\n")

	hunks := computeHunks(oldLines, newLines)
	if len(hunks) == 0 {
		return fmt.Sprintf("[no changes, %d lines]\n", len(newLines))
	}

	var result strings.Builder
	diffLines := 0
	for _, h := range hunks {
		fmt.Fprintf(&result, "@@ -%d,%d +%d,%d @@\n",
			h.oldStart+1, h.oldCount, h.newStart+1, h.newCount)
		for _, line := range h.lines {
			result.WriteString(line)
			result.WriteByte('\n')
			diffLines++
		}
	}

	unchanged := len(newLines) - diffLines
	if unchanged > 0 {
		fmt.Fprintf(&result, "[%d unchanged lines omitted]\n", unchanged)
	}
	return result.String()
}

type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	lines              []string
}

const contextLines = 3

// computeHunks builds diff hunks from two line slices using LCS.
func computeHunks(old, new []string) []hunk {
	m, n := len(old), len(new)

	// Too large for DP — show as one big hunk
	if m*n > 10_000_000 {
		return []hunk{{
			oldStart: 0, oldCount: m,
			newStart: 0, newCount: n,
			lines:    prefixLines(new, "+"),
		}}
	}

	lcs := computeLCS(old, new)

	var hunks []hunk
	var cur *hunk
	oi, ni, li := 0, 0, 0

	flush := func() {
		if cur != nil && len(cur.lines) > 0 {
			hunks = append(hunks, *cur)
			cur = nil
		}
	}

	for oi < m || ni < n {
		if li < len(lcs) && oi < m && ni < n && old[oi] == lcs[li] && new[ni] == lcs[li] {
			// Matching line
			if cur != nil {
				cur.lines = append(cur.lines, " "+old[oi])
				cur.oldCount++
				cur.newCount++
				trailing := 0
				for j := len(cur.lines) - 1; j >= 0 && cur.lines[j][0] == ' '; j-- {
					trailing++
				}
				if trailing >= contextLines {
					flush()
				}
			}
			oi++
			ni++
			li++
			continue
		}

		// Start or extend a hunk
		if cur == nil {
			cur = &hunk{oldStart: oi, newStart: ni}
			start := oi - contextLines
			if start < 0 {
				start = 0
			}
			for i := start; i < oi; i++ {
				cur.lines = append(cur.lines, " "+old[i])
				cur.oldCount++
				cur.newCount++
				cur.oldStart = i
				cur.newStart = ni - (oi - i)
			}
		}

		for oi < m && (li >= len(lcs) || old[oi] != lcs[li]) {
			cur.lines = append(cur.lines, "-"+old[oi])
			cur.oldCount++
			oi++
		}

		for ni < n && (li >= len(lcs) || new[ni] != lcs[li]) {
			cur.lines = append(cur.lines, "+"+new[ni])
			cur.newCount++
			ni++
		}
	}

	flush()
	return hunks
}

// computeLCS returns the longest common subsequence of a and b.
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
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

func prefixLines(lines []string, prefix string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = prefix + l
	}
	return out
}
