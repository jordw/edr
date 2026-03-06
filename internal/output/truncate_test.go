package output

import "testing"

func TestTruncateAtLine_Empty(t *testing.T) {
	s, trunc := TruncateAtLine("", 100)
	if trunc {
		t.Error("empty string should not be truncated")
	}
	if s != "" {
		t.Errorf("expected empty, got %q", s)
	}
}

func TestTruncateAtLine_UnderBudget(t *testing.T) {
	input := "line1\nline2\nline3\n"
	s, trunc := TruncateAtLine(input, 1000)
	if trunc {
		t.Error("should not truncate when under budget")
	}
	if s != input {
		t.Errorf("expected input unchanged")
	}
}

func TestTruncateAtLine_ExactBudget(t *testing.T) {
	input := "line1\nline2\n"
	s, trunc := TruncateAtLine(input, len(input))
	if trunc {
		t.Error("exact budget should not truncate")
	}
	if s != input {
		t.Errorf("expected input unchanged")
	}
}

func TestTruncateAtLine_CutsAtNewline(t *testing.T) {
	input := "line1\nline2\nline3\n"
	// Budget cuts into "line2" — should keep only "line1\n"
	s, trunc := TruncateAtLine(input, 8)
	if !trunc {
		t.Error("should be truncated")
	}
	if s != "line1\n... (truncated)" {
		t.Errorf("expected truncation at line1, got %q", s)
	}
}

func TestTruncateAtLine_NewlineAtBoundary(t *testing.T) {
	input := "line1\nline2\nline3\n"
	// Budget = 6, exactly after "line1\n"
	s, trunc := TruncateAtLine(input, 6)
	if !trunc {
		t.Error("should be truncated")
	}
	if s != "line1\n... (truncated)" {
		t.Errorf("expected truncation at line1, got %q", s)
	}
}

func TestTruncateAtLine_SingleLine(t *testing.T) {
	input := "a very long single line without newlines"
	s, trunc := TruncateAtLine(input, 10)
	if !trunc {
		t.Error("should be truncated")
	}
	// No newline found, falls back to char budget
	if s != input[:10]+"... (truncated)" {
		t.Errorf("got %q", s)
	}
}

func TestTruncateAtLine_SingleLineWithTrailingNewline(t *testing.T) {
	input := "a very long single line\n"
	s, trunc := TruncateAtLine(input, 10)
	if !trunc {
		t.Error("should be truncated")
	}
	// No newline before budget=10, so take first line
	if s != "a very long single line\n... (truncated)" {
		t.Errorf("got %q", s)
	}
}

func TestTruncateBodyToTokenBudget_NoBudget(t *testing.T) {
	body, trunc := TruncateBodyToTokenBudget("hello world", 0, 0)
	if trunc {
		t.Error("no budget should not truncate")
	}
	if body != "hello world" {
		t.Errorf("expected unchanged body")
	}
}

func TestTruncateBodyToTokenBudget_Exhausted(t *testing.T) {
	body, trunc := TruncateBodyToTokenBudget("hello", 10, 10)
	if !trunc {
		t.Error("should be truncated when budget exhausted")
	}
	if body != "" {
		t.Errorf("expected empty body, got %q", body)
	}
}

func TestTruncateBodyToTokenBudget_Partial(t *testing.T) {
	// 50 tokens budget, 10 used = 40 remaining = 160 chars
	input := "line1\nline2\nline3\nline4\n" // 24 chars, fits
	body, trunc := TruncateBodyToTokenBudget(input, 50, 10)
	if trunc {
		t.Error("should fit in budget")
	}
	if body != input {
		t.Errorf("expected unchanged")
	}
}
