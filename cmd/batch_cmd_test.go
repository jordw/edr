package cmd

import "testing"

func TestBatchParseNoGroup(t *testing.T) {
	args := []string{"-s", "pattern", "--no-group"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	if len(p.Queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(p.Queries))
	}
	if p.Queries[0].Group == nil || *p.Queries[0].Group != false {
		t.Error("--no-group should set Group to false")
	}
}

func TestBatchParseFuzzy(t *testing.T) {
	args := []string{"-e", "file.go", "--old", "x", "--new", "y", "--fuzzy"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	if len(p.Edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(p.Edits))
	}
	if p.Edits[0].Fuzzy == nil || !*p.Edits[0].Fuzzy {
		t.Error("--fuzzy should set Fuzzy to true")
	}
}

func TestBatchParseDelete(t *testing.T) {
	args := []string{"-e", "file.go", "--old", "x", "--delete"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	if len(p.Edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(p.Edits))
	}
	if p.Edits[0].Delete == nil || !*p.Edits[0].Delete {
		t.Error("--delete should set Delete to true")
	}
}

func TestBatchParseInsertAt(t *testing.T) {
	args := []string{"-e", "file.go", "--insert-at", "5", "--new", "hello"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	if len(p.Edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(p.Edits))
	}
	if p.Edits[0].InsertAt == nil || *p.Edits[0].InsertAt != 5 {
		t.Error("--insert-at should set InsertAt to 5")
	}
}

func TestBatchParseAtomic(t *testing.T) {
	args := []string{"-e", "f.go", "--old", "a", "--new", "b", "-e", "g.go", "--old", "c", "--new", "d", "--atomic"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	if p.Atomic == nil || !*p.Atomic {
		t.Error("--atomic should set Atomic to true")
	}
	if len(p.Edits) != 2 {
		t.Errorf("expected 2 edits, got %d", len(p.Edits))
	}
}

func TestBatchDryRunPostEditReadWarning(t *testing.T) {
	// Verify that --dry-run plus post-edit reads produces a warning
	args := []string{"-e", "f.go", "--old", "x", "--new", "y", "--dry-run", "-r", "f.go"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	if len(p.PostEditReads) != 1 {
		t.Fatalf("expected 1 post-edit read, got %d", len(p.PostEditReads))
	}
	if len(p.Edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(p.Edits))
	}
	// The warning is emitted at execution time, not parse time.
	// This test validates the parse structure is correct.
}

func TestBatchParseLevelTimeout(t *testing.T) {
	args := []string{"-e", "f.go", "--old", "a", "--new", "b", "-V", "--level", "test", "--timeout", "30"}
	state, err := parseBatchArgs(args)
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	vm, ok := p.Verify.(map[string]any)
	if !ok {
		t.Fatalf("expected verify map, got %T: %v", p.Verify, p.Verify)
	}
	if vm["level"] != "test" {
		t.Errorf("level = %v, want test", vm["level"])
	}
	if vm["timeout"] != 30 {
		t.Errorf("timeout = %v, want 30", vm["timeout"])
	}
}

func TestBatchQueryLang(t *testing.T) {
	// Test that JSON batch with lang field is correctly threaded
	q := doQuery{
		Cmd:  "map",
		Lang: sp("go"),
	}
	mc := queryToMultiCmd(q)
	if mc.Flags["lang"] != "go" {
		t.Errorf("expected lang=go, got %v", mc.Flags["lang"])
	}
}

func TestSilentErrorExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  silentError
		want int
	}{
		{"default is 1", silentError{}, 1},
		{"explicit 1", silentError{code: 1}, 1},
		{"verify failure is 2", silentError{code: 2}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSilentErrorMessage(t *testing.T) {
	// silentError should always return empty string — the structured JSON
	// output was already printed.
	e := silentError{code: 2}
	if msg := e.Error(); msg != "" {
		t.Errorf("Error() = %q, want empty", msg)
	}
}

func TestInterpretEscapesSkipsMultiline(t *testing.T) {
	// When the value already contains real newlines (shell handled quoting),
	// interpretEscapes should not process \n or \t sequences, since they
	// may be literal backslash sequences in source code (e.g. Go format strings).
	input := "t.Errorf(\"expected:\\n%v\\ngot:\\n%v\", a, b)\nreturn nil"
	got := interpretEscapes(input)
	if got != input {
		t.Errorf("interpretEscapes should not modify multi-line input\ngot:  %q\nwant: %q", got, input)
	}
}

func TestInterpretEscapesSingleLine(t *testing.T) {
	// Single-line values without real newlines/tabs should still get escape processing.
	input := `first\nsecond\tindented`
	want := "first\nsecond\tindented"
	got := interpretEscapes(input)
	if got != want {
		t.Errorf("interpretEscapes(%q) = %q, want %q", input, got, want)
	}
}

func TestBatchParseDeleteNewConflict(t *testing.T) {
	// --delete before --new
	_, err := parseBatchArgs([]string{"-e", "f.go", "--old", "x", "--delete", "--new", "y"})
	if err == nil {
		t.Error("expected error when --new combined with --delete")
	}
	// --new before --delete
	_, err = parseBatchArgs([]string{"-e", "f.go", "--old", "x", "--new", "y", "--delete"})
	if err == nil {
		t.Error("expected error when --delete combined with --new")
	}
	// --delete alone should work
	_, err = parseBatchArgs([]string{"-e", "f.go", "--old", "x", "--delete"})
	if err != nil {
		t.Errorf("--delete alone should work: %v", err)
	}
}

