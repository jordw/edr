package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffAgainstPrevious_FirstRun(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	out := diffAgainstPrevious(runDir, "echo hello", "hello world\n")
	if out != "hello world\n" {
		t.Errorf("first run should show full output, got: %q", out)
	}
}

func TestDiffAgainstPrevious_Identical(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	diffAgainstPrevious(runDir, "echo hello", "hello world\n")
	out := diffAgainstPrevious(runDir, "echo hello", "hello world\n")
	if !strings.Contains(out, "no changes") {
		t.Errorf("identical run should say no changes, got: %q", out)
	}
}

func TestDiffAgainstPrevious_ShowsInlineDiff(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	diffAgainstPrevious(runDir, "test", "aaa\nbbb\nccc\nddd\neee\n")
	out := diffAgainstPrevious(runDir, "test", "aaa\nBBB\nccc\nddd\neee\n")

	if !strings.Contains(out, "{bbb → BBB}") {
		t.Errorf("should show inline diff, got: %q", out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("should collapse unchanged, got: %q", out)
	}
}

func TestDiffAgainstPrevious_AddedLines(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	diffAgainstPrevious(runDir, "test", "aaa\nbbb\n")
	out := diffAgainstPrevious(runDir, "test", "aaa\nbbb\nccc\n")
	if !strings.Contains(out, "{+ ccc}") {
		t.Errorf("should show added line, got: %q", out)
	}
}

func TestDiffAgainstPrevious_RemovedLines(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	diffAgainstPrevious(runDir, "test", "aaa\nbbb\nccc\n")
	out := diffAgainstPrevious(runDir, "test", "aaa\nbbb\n")
	if !strings.Contains(out, "{- ccc}") {
		t.Errorf("should show removed line, got: %q", out)
	}
}

func TestDiffAgainstPrevious_DifferentCommands(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	diffAgainstPrevious(runDir, "cmd1", "output1\n")
	diffAgainstPrevious(runDir, "cmd2", "output2\n")
	out := diffAgainstPrevious(runDir, "cmd1", "output1\n")
	if !strings.Contains(out, "no changes") {
		t.Errorf("cmd1 should match its own history, got: %q", out)
	}
}

func TestDiffAgainstPrevious_OverwritesPrevious(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	diffAgainstPrevious(runDir, "test", "v1\n")
	diffAgainstPrevious(runDir, "test", "v2\n")
	out := diffAgainstPrevious(runDir, "test", "v2\n")
	if !strings.Contains(out, "no changes") {
		t.Errorf("should match v2, got: %q", out)
	}
}

func TestDiffAgainstPrevious_TruncatesLargeOutput(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	big := strings.Repeat("x", maxRunOutput+1000)
	diffAgainstPrevious(runDir, "test", big)
	files, _ := os.ReadDir(runDir)
	for _, f := range files {
		info, _ := f.Info()
		if info.Size() > int64(maxRunOutput)+100 {
			t.Errorf("stored file should be capped at %d, got %d", maxRunOutput, info.Size())
		}
	}
}

func TestInlineDiff_TimingChange(t *testing.T) {
	got := inlineDiff("ok  pkg  0.150s", "ok  pkg  0.131s")
	if !strings.Contains(got, "{") || !strings.Contains(got, "→") {
		t.Errorf("should have inline markers, got: %q", got)
	}
	if !strings.HasPrefix(got, "ok  pkg  0.1") {
		t.Errorf("should preserve common prefix, got: %q", got)
	}
}

func TestInlineDiff_StructuralChange(t *testing.T) {
	got := inlineDiff("--- PASS: TestBar", "--- FAIL: TestBar")
	if !strings.Contains(got, "{PASS → FAIL}") {
		t.Errorf("should show PASS→FAIL, got: %q", got)
	}
}

func TestInlineDiff_Identical(t *testing.T) {
	got := inlineDiff("same line", "same line")
	if got != "same line" {
		t.Errorf("identical lines should return as-is, got: %q", got)
	}
}

func TestSparseDiff_AllUnchanged(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	out := sparseDiff(lines, lines)
	if !strings.Contains(out, "[5 unchanged lines]") {
		t.Errorf("should collapse all, got: %q", out)
	}
}

func TestSparseDiff_InsertedLine(t *testing.T) {
	old := []string{"aaa", "bbb", "ccc", "ddd"}
	new := []string{"aaa", "INSERTED", "bbb", "ccc", "ddd"}
	out := sparseDiff(old, new)
	if !strings.Contains(out, "{+ INSERTED}") {
		t.Errorf("should show inserted line, got: %q", out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("should collapse unchanged, got: %q", out)
	}
	// Should NOT show garbled inline diffs
	if strings.Contains(out, "{aaa") || strings.Contains(out, "{bbb") {
		t.Errorf("should not garble unchanged lines, got: %q", out)
	}
}

func TestSparseDiff_RemovedLine(t *testing.T) {
	old := []string{"aaa", "REMOVE_ME", "bbb", "ccc"}
	new := []string{"aaa", "bbb", "ccc"}
	out := sparseDiff(old, new)
	if !strings.Contains(out, "{- REMOVE_ME}") {
		t.Errorf("should show removed line, got: %q", out)
	}
}

func TestSparseDiff_MixedChanges(t *testing.T) {
	old := []string{"a", "b", "c", "d", "e"}
	new := []string{"a", "B", "c", "d", "E"}
	out := sparseDiff(old, new)
	if !strings.Contains(out, "{b → B}") {
		t.Errorf("should show b→B, got: %q", out)
	}
	if !strings.Contains(out, "{e → E}") {
		t.Errorf("should show e→E, got: %q", out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("should have unchanged sections, got: %q", out)
	}
}

func TestSparseDiff_DigitOnlyCollapse(t *testing.T) {
	old := []string{
		"PASS Test1 (0.003s)",
		"PASS Test2 (0.005s)",
		"PASS Test3 (0.001s)",
		"ok",
	}
	new := []string{
		"PASS Test1 (0.004s)",
		"PASS Test2 (0.006s)",
		"PASS Test3 (0.002s)",
		"ok",
	}
	out := sparseDiff(old, new)
	if !strings.Contains(out, "numbers changed") {
		t.Errorf("digit-only changes should collapse, got: %q", out)
	}
	// Should NOT show individual inline diffs for timing
	if strings.Contains(out, "→") {
		t.Errorf("digit-only lines should not show inline diff, got: %q", out)
	}
}

func TestIsDigitOnlyChange(t *testing.T) {
	if !isDigitOnlyChange("test 0.003s", "test 0.005s") {
		t.Error("should be digit-only")
	}
	if isDigitOnlyChange("PASS test", "FAIL test") {
		t.Error("should not be digit-only")
	}
	if isDigitOnlyChange("short", "longer string") {
		t.Error("different lengths should not be digit-only")
	}
}
