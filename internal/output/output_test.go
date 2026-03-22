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
