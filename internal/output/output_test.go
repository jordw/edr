package output

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// capturePlain runs printPlain on an envelope and returns the captured stdout.
func capturePlain(t *testing.T, e *Envelope) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	printPlain(e)
	w.Close()
	os.Stdout = old
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	r.Close()
	return string(buf[:n])
}

func TestPlainVerifyIncludesCommand(t *testing.T) {
	tests := []struct {
		name    string
		verify  map[string]any
		wantCmd string
	}{
		{
			"passed",
			map[string]any{"status": "passed", "command": "go build ./..."},
			"go build ./...",
		},
		{
			"failed",
			map[string]any{"status": "failed", "command": "go build ./...", "error": "exit 1"},
			"go build ./...",
		},
		{
			"skipped",
			map[string]any{"status": "skipped", "command": "go build ./...", "reason": "dry run"},
			"go build ./...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := NewEnvelope("edit")
			env.AddOp("e0", "edit", map[string]any{"file": "f.go", "status": "applied", "hash": "abc"})
			env.SetVerify(tt.verify)

			out := capturePlain(t, env)

			// Find the verify line (last JSON line)
			lines := strings.Split(strings.TrimSpace(out), "\n")
			var verifyLine string
			for _, l := range lines {
				if strings.Contains(l, `"verify"`) {
					verifyLine = l
				}
			}
			if verifyLine == "" {
				t.Fatalf("no verify line found in output:\n%s", out)
			}

			var h map[string]any
			if err := json.Unmarshal([]byte(verifyLine), &h); err != nil {
				t.Fatalf("failed to parse verify header %q: %v", verifyLine, err)
			}
			if h["command"] != tt.wantCmd {
				t.Errorf("command = %v, want %q", h["command"], tt.wantCmd)
			}
		})
	}
}

func TestPlainVerifyFailureHeaderOnly(t *testing.T) {
	env := NewEnvelope("edit")
	env.AddOp("e0", "edit", map[string]any{"file": "f.go", "status": "applied", "hash": "abc"})
	env.SetVerify(map[string]any{
		"status":  "failed",
		"command": "go build ./...",
		"error":   "exit status 1",
		"output":  "main.go:5: undefined: foo\n",
	})

	out := capturePlain(t, env)

	// Find the verify line
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var verifyLine string
	for _, l := range lines {
		if strings.Contains(l, `"verify"`) {
			verifyLine = l
		}
	}
	if verifyLine == "" {
		t.Fatalf("no verify line in output:\n%s", out)
	}

	var h map[string]any
	if err := json.Unmarshal([]byte(verifyLine), &h); err != nil {
		t.Fatalf("parse verify header: %v", err)
	}

	// Output must be in the header, not as a separate body line
	if h["output"] == nil {
		t.Error("output should be included in the verify header")
	}

	// Verify there's no body after the verify header (header-only contract)
	verifyIdx := -1
	for i, l := range lines {
		if strings.Contains(l, `"verify"`) {
			verifyIdx = i
		}
	}
	if verifyIdx >= 0 && verifyIdx < len(lines)-1 {
		// Any lines after verify must not be raw body content
		remaining := strings.TrimSpace(strings.Join(lines[verifyIdx+1:], "\n"))
		if remaining != "" {
			t.Errorf("verify failure should be header-only, but has trailing body: %q", remaining)
		}
	}
}

func TestPlainSessionNewPreserved(t *testing.T) {
	tests := []struct {
		name   string
		opType string
		op     map[string]any
	}{
		{
			"read",
			"read",
			map[string]any{"file": "f.go", "hash": "abc", "content": "hello", "session": "new"},
		},
		{
			"search",
			"search",
			map[string]any{"total_matches": 1, "session": "new", "matches": []any{}},
		},
		{
			"map",
			"map",
			map[string]any{"files": 1, "symbols": 2, "session": "new", "content": []any{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := NewEnvelope(tt.opType)
			env.AddOp("op0", tt.opType, tt.op)

			out := capturePlain(t, env)

			// First line is the header
			lines := strings.SplitN(out, "\n", 2)
			var h map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &h); err != nil {
				t.Fatalf("failed to parse header %q: %v", lines[0], err)
			}
			if h["session"] != "new" {
				t.Errorf("session = %v, want \"new\"", h["session"])
			}
		})
	}
}

func TestTokenEstimate(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefgh", 2},
		{"abcdefghi", 3},
	}
	for _, tt := range tests {
		got := TokenEstimate(tt.input)
		if got != tt.want {
			t.Errorf("TokenEstimate(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
