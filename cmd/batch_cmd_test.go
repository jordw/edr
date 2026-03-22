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
