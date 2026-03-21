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

func TestDiffAgainstPrevious_ShowsDiff(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")

	first := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	diffAgainstPrevious(runDir, "test", first)

	second := "line1\nline2\nline3\nline4\nCHANGED\nline6\nline7\nline8\nline9\nline10\n"
	out := diffAgainstPrevious(runDir, "test", second)

	if !strings.Contains(out, "+CHANGED") {
		t.Errorf("diff should show +CHANGED, got: %q", out)
	}
	if !strings.Contains(out, "-line5") {
		t.Errorf("diff should show -line5, got: %q", out)
	}
	if !strings.Contains(out, "unchanged lines omitted") {
		t.Errorf("should mention unchanged lines, got: %q", out)
	}
}

func TestDiffAgainstPrevious_CompletelyDifferent(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")

	diffAgainstPrevious(runDir, "test", "aaa\nbbb\nccc\n")
	out := diffAgainstPrevious(runDir, "test", "xxx\nyyy\nzzz\n")

	// Completely different — shows as diff with all lines changed
	if !strings.Contains(out, "+xxx") {
		t.Errorf("should show new lines, got: %q", out)
	}
	if !strings.Contains(out, "-aaa") {
		t.Errorf("should show removed lines, got: %q", out)
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

func TestLineDiff_OneLine(t *testing.T) {
	old := "PASS TestA\nPASS TestB\nPASS TestC\nPASS TestD\nPASS TestE\nPASS TestF\nok 0.003s\n"
	new := "PASS TestA\nPASS TestB\nPASS TestC\nPASS TestD\nPASS TestE\nPASS TestF\nok 0.005s\n"

	out := lineDiff(old, new)
	if !strings.Contains(out, "-ok 0.003s") {
		t.Errorf("should show old timing, got: %q", out)
	}
	if !strings.Contains(out, "+ok 0.005s") {
		t.Errorf("should show new timing, got: %q", out)
	}
	if !strings.Contains(out, "unchanged") {
		t.Errorf("should mention unchanged lines, got: %q", out)
	}
}

func TestLineDiff_TestFlip(t *testing.T) {
	old := "PASS Test1\nPASS Test2\nPASS Test3\nPASS Test4\nPASS Test5\nPASS Test6\nPASS Test7\nPASS Test8\nPASS Test9\nPASS Test10\nPASS TestFoo\nPASS TestBar\nPASS\n"
	new := "PASS Test1\nPASS Test2\nPASS Test3\nPASS Test4\nPASS Test5\nPASS Test6\nPASS Test7\nPASS Test8\nPASS Test9\nPASS Test10\nPASS TestFoo\nFAIL TestBar\n    expected 42\nFAIL\n"

	out := lineDiff(old, new)
	if !strings.Contains(out, "+FAIL TestBar") {
		t.Errorf("should show FAIL, got: %q", out)
	}
	if !strings.Contains(out, "+    expected 42") {
		t.Errorf("should show error, got: %q", out)
	}
}

func TestComputeLCS(t *testing.T) {
	a := []string{"a", "b", "c", "d", "e"}
	b := []string{"a", "c", "d", "f", "e"}
	lcs := computeLCS(a, b)
	got := strings.Join(lcs, ",")
	if got != "a,c,d,e" {
		t.Errorf("LCS = %q, want a,c,d,e", got)
	}
}

func TestComputeLCS_Empty(t *testing.T) {
	lcs := computeLCS([]string{}, []string{"a", "b"})
	if len(lcs) != 0 {
		t.Errorf("LCS of empty should be empty, got %v", lcs)
	}
}

func TestComputeLCS_Identical(t *testing.T) {
	a := []string{"a", "b", "c"}
	lcs := computeLCS(a, a)
	if len(lcs) != 3 {
		t.Errorf("LCS of identical should be full length, got %d", len(lcs))
	}
}
