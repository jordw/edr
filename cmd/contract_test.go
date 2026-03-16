package cmd_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func contains(s, substr string) bool { return strings.Contains(s, substr) }

// setupContractRepo creates a temp repo with a Go file and indexes it.
// Returns the binary path and the repo dir (symlink-resolved for macOS compatibility).
func setupContractRepo(t *testing.T, content string) (string, string) {
	t.Helper()
	binary := buildBinary(t)
	repoDir := t.TempDir()
	// Resolve symlinks to avoid macOS /var → /private/var mismatch
	// which causes symbol lookups to fail (DB stores one path, query uses another).
	repoDir, _ = filepath.EvalSymlinks(repoDir)
	os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte(content), 0644)
	run(t, binary, repoDir, "reindex")
	return binary, repoDir
}

// TestContentFieldNotBody verifies that symbol reads return "content", never "body".
// Regression test for the body→content rename.
func TestContentFieldNotBody(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n")

	tests := []struct {
		name string
		args []string
	}{
		{"standalone symbol read", []string{"read", "hello.go:helper"}},
		{"batch symbol read", []string{"-r", "hello.go:helper"}},
		{"standalone file read", []string{"read", "hello.go"}},
		{"batch file read", []string{"-r", "hello.go"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=content_%d", parityCounter.Add(1)))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("command failed: %v\n%s", err, out)
			}
			var env struct {
				Ops []map[string]any `json:"ops"`
			}
			json.Unmarshal(out, &env)
			if len(env.Ops) == 0 {
				t.Fatal("no ops")
			}
			op := env.Ops[0]
			if _, has := op["body"]; has {
				t.Errorf("op has 'body' field — should be 'content'")
			}
			if _, has := op["content"]; !has {
				t.Errorf("op missing 'content' field")
			}
		})
	}
}

// TestSymbolReadHasFileAndHash verifies that symbol reads include file and hash
// at the top level, not just nested inside the symbol sub-object.
func TestSymbolReadHasFileAndHash(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n")

	tests := []struct {
		name string
		args []string
	}{
		{"standalone", []string{"read", "hello.go:helper"}},
		{"batch", []string{"-r", "hello.go:helper"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=filehash_%d", parityCounter.Add(1)))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("command failed: %v\n%s", err, out)
			}
			var env struct {
				Ops []map[string]any `json:"ops"`
			}
			json.Unmarshal(out, &env)
			if len(env.Ops) == 0 {
				t.Fatal("no ops")
			}
			op := env.Ops[0]
			if _, has := op["file"]; !has {
				t.Errorf("symbol read op missing top-level 'file'\nop: %s", mustJSON(op))
			}
			if _, has := op["hash"]; !has {
				t.Errorf("symbol read op missing top-level 'hash'\nop: %s", mustJSON(op))
			}
		})
	}
}

// TestBatchCommandFieldParity verifies that single-type batch commands
// report the actual command name, not "batch".
func TestBatchCommandFieldParity(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	tests := []struct {
		name    string
		args    []string
		wantCmd string
	}{
		{"read-only batch", []string{"-r", "hello.go"}, "read"},
		{"search-only batch", []string{"-s", "main"}, "search"},
		{"edit-only batch", []string{"-e", "hello.go", "--old", "package main", "--new", "package main", "--dry-run"}, "edit"},
		{"mixed batch", []string{"-r", "hello.go", "-s", "main"}, "batch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=cmdfield_%d", parityCounter.Add(1)))
			out, err := cmd.CombinedOutput()
			if err != nil && tt.wantCmd != "edit" {
				t.Fatalf("command failed: %v\n%s", err, out)
			}
			var env struct {
				Command string `json:"command"`
			}
			json.Unmarshal(out, &env)
			if env.Command != tt.wantCmd {
				t.Errorf("command = %q, want %q", env.Command, tt.wantCmd)
			}
		})
	}
}

// TestErrorCodeSpecificity verifies that errors use specific codes,
// not the generic "command_error".
func TestErrorCodeSpecificity(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	tests := []struct {
		name      string
		args      []string
		wantCode  string
		wantInOps bool // batch per-op errors go in ops[].error, not errors[]
	}{
		{"file not found", []string{"read", "nonexistent.go"}, "file_not_found", false},
		{"symbol not found", []string{"read", "hello.go:nonExistentSymbol"}, "symbol_not_found", false},
		{"batch file not found", []string{"-r", "nonexistent.go"}, "no such file or directory", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=errcode_%d", parityCounter.Add(1)))
			out, _ := cmd.CombinedOutput() // expect non-zero exit

			outStr := string(out)
			if tt.wantInOps {
				// Batch per-op errors: check the error message in ops
				if !contains(outStr, tt.wantCode) {
					t.Errorf("expected %q in output, got: %s", tt.wantCode, outStr)
				}
			} else {
				// Standalone errors: check envelope error code
				var env struct {
					Errors []struct {
						Code string `json:"code"`
					} `json:"errors"`
				}
				json.Unmarshal(out, &env)
				found := false
				for _, e := range env.Errors {
					if e.Code == tt.wantCode {
						found = true
					}
				}
				if !found {
					t.Errorf("expected error code %q, got: %s", tt.wantCode, outStr)
				}
			}
		})
	}
}

// TestDryRunEnrichmentFields verifies that dry-run edits include
// structured preview fields (destructive, lines_added, lines_removed).
func TestDryRunEnrichmentFields(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n")

	tests := []struct {
		name string
		args []string
	}{
		{"standalone dry-run", []string{"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run"}},
		{"batch dry-run", []string{"-e", "hello.go", "--old", "package main", "--new", "package test", "--dry-run"}},
	}

	requiredFields := []string{"diff", "status", "file", "destructive", "lines_added", "lines_removed"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=dryrun_%d", parityCounter.Add(1)))
			out, _ := cmd.CombinedOutput()

			var env struct {
				Ops []map[string]any `json:"ops"`
			}
			json.Unmarshal(out, &env)
			if len(env.Ops) == 0 {
				t.Fatalf("no ops in output: %s", out)
			}
			op := env.Ops[0]

			for _, field := range requiredFields {
				if _, has := op[field]; !has {
					t.Errorf("dry-run op missing field %q\nop: %s", field, mustJSON(op))
				}
			}

			if status, _ := op["status"].(string); status != "dry_run" {
				t.Errorf("status = %q, want \"dry_run\"", status)
			}
		})
	}
}

// TestReadParitySymbolRead extends the existing parity test to cover
// symbol reads specifically — the path most prone to divergence.
func TestReadParitySymbolRead(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n")

	standaloneOps := runAndExtractOps(t, binary, repoDir, []string{"read", "hello.go:helper"})
	batchOps := runAndExtractOps(t, binary, repoDir, []string{"-r", "hello.go:helper"})

	if len(standaloneOps) != len(batchOps) {
		t.Fatalf("ops count mismatch: standalone=%d batch=%d", len(standaloneOps), len(batchOps))
	}

	ignoreFields := map[string]bool{"op_id": true, "session": true, "mtime": true}

	sOp := standaloneOps[0].(map[string]any)
	bOp := batchOps[0].(map[string]any)

	// Check that the same keys are present
	allKeys := map[string]bool{}
	for k := range sOp {
		allKeys[k] = true
	}
	for k := range bOp {
		allKeys[k] = true
	}
	for k := range allKeys {
		if ignoreFields[k] {
			continue
		}
		_, sHas := sOp[k]
		_, bHas := bOp[k]
		if sHas != bHas {
			t.Errorf("field %q: standalone has=%v, batch has=%v", k, sHas, bHas)
		}
	}
}
