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
		{"file not found", []string{"read", "nonexistent.go"}, "no such file or directory", true},
		{"symbol not found", []string{"read", "hello.go:nonExistentSymbol"}, "not found", true},
		{"batch file not found", []string{"-r", "nonexistent.go"}, "no such file or directory", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=errcode_%d", parityCounter.Add(1)))
			out, _ := cmd.CombinedOutput() // expect non-zero exit

			outStr := string(out)
			// All per-op errors now go in ops[].error (parity with batch)
			if !contains(outStr, tt.wantCode) {
				t.Errorf("expected %q in output, got: %s", tt.wantCode, outStr)
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

// TestNoSessionMeansNoDedup verifies that without EDR_SESSION, repeated reads
// always return content (no session dedup).
func TestNoSessionMeansNoDedup(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	for i := 0; i < 3; i++ {
		cmd := exec.Command(binary, "read", "hello.go")
		cmd.Dir = repoDir
		// Explicitly unset EDR_SESSION
		cmd.Env = filterEnv(os.Environ(), "EDR_SESSION")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("read %d failed: %v\n%s", i, err, out)
		}

		var env struct {
			Ops []map[string]any `json:"ops"`
		}
		json.Unmarshal(out, &env)
		if len(env.Ops) == 0 {
			t.Fatalf("read %d: no ops", i)
		}
		op := env.Ops[0]
		if _, has := op["content"]; !has {
			t.Errorf("read %d: missing content (session dedup without EDR_SESSION?)\nop: %s", i, mustJSON(op))
		}
		if unchanged, _ := op["unchanged"].(bool); unchanged {
			t.Errorf("read %d: got unchanged=true without EDR_SESSION set", i)
		}
	}
}

// TestErrorParityBatchStandalone verifies that both batch and standalone put
// per-op errors in ops[].error (not in the envelope errors[] array).
func TestErrorParityBatchStandalone(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	tests := []struct {
		name string
		args []string
	}{
		{"standalone file not found", []string{"read", "nonexistent.go"}},
		{"batch file not found", []string{"-r", "nonexistent.go"}},
		{"standalone symbol not found", []string{"read", "hello.go:noSuchSymbol"}},
		{"batch symbol not found", []string{"-r", "hello.go:noSuchSymbol"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=errparity_%d", parityCounter.Add(1)))
			out, _ := cmd.CombinedOutput()

			var env struct {
				OK     bool               `json:"ok"`
				Ops    []map[string]any    `json:"ops"`
				Errors []map[string]string `json:"errors"`
			}
			json.Unmarshal(out, &env)

			if env.OK {
				t.Errorf("expected ok=false for error case")
			}
			if len(env.Errors) != 0 {
				t.Errorf("per-op errors should be in ops[].error, not errors[]\nerrors: %s", mustJSON(env.Errors))
			}
			if len(env.Ops) == 0 {
				t.Fatalf("expected a failed op in ops[], got empty\noutput: %s", out)
			}
			op := env.Ops[0]
			if _, has := op["error"]; !has {
				t.Errorf("op missing 'error' field\nop: %s", mustJSON(op))
			}
			if _, has := op["op_id"]; !has {
				t.Errorf("failed op missing 'op_id'\nop: %s", mustJSON(op))
			}
			if _, has := op["type"]; !has {
				t.Errorf("failed op missing 'type'\nop: %s", mustJSON(op))
			}
		})
	}
}

// TestContentHasNoLineNumbers verifies that read content is raw text,
// not prefixed with line numbers like "1\tpackage main\n2\t...".
func TestContentHasNoLineNumbers(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	tests := []struct {
		name string
		args []string
	}{
		{"standalone read", []string{"read", "hello.go"}},
		{"batch read", []string{"-r", "hello.go"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=nolnum_%d", parityCounter.Add(1)))
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
			content, _ := env.Ops[0]["content"].(string)
			if content == "" {
				t.Fatal("empty content")
			}
			// Content must start with "package", not "1\tpackage"
			if !strings.HasPrefix(content, "package main") {
				t.Errorf("content has unexpected prefix (line numbers?)\ngot: %q", content[:min(60, len(content))])
			}
			// No line should start with a digit followed by a tab
			for i, line := range strings.Split(content, "\n") {
				if len(line) > 0 && line[0] >= '0' && line[0] <= '9' && strings.Contains(line, "\t") {
					t.Errorf("line %d looks like it has a line number prefix: %q", i+1, line)
					break
				}
			}
		})
	}
}

// TestSearchDeterminism verifies that the same text search returns
// identical results across multiple runs.
func TestSearchDeterminism(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n\nfunc helper() int {\n\treturn 42\n}\n")

	var firstResult string
	for i := 0; i < 5; i++ {
		cmd := exec.Command(binary, "search", "main", "--text")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=determ_%d", parityCounter.Add(1)))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %d failed: %v\n%s", i, err, out)
		}

		// Normalize: strip session field (varies per run)
		var env map[string]any
		json.Unmarshal(out, &env)
		if ops, ok := env["ops"].([]any); ok {
			for _, op := range ops {
				if m, ok := op.(map[string]any); ok {
					delete(m, "session")
					delete(m, "op_id")
				}
			}
		}
		normalized, _ := json.Marshal(env)
		result := string(normalized)

		if i == 0 {
			firstResult = result
		} else if result != firstResult {
			t.Errorf("run %d differs from run 0:\nrun 0: %s\nrun %d: %s", i, firstResult, i, result)
			break
		}
	}
}

// TestNoopParityBatchStandalone verifies that noop edits (old_text == new_text)
// produce identical op shape from batch and standalone.
func TestNoopParityBatchStandalone(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	standaloneArgs := []string{"edit", "hello.go", "--old-text", "package main", "--new-text", "package main", "--dry-run"}
	batchArgs := []string{"-e", "hello.go", "--old", "package main", "--new", "package main", "--dry-run"}

	standaloneOps := runAndExtractOps(t, binary, repoDir, standaloneArgs)
	batchOps := runAndExtractOps(t, binary, repoDir, batchArgs)

	if len(standaloneOps) == 0 || len(batchOps) == 0 {
		t.Fatalf("expected ops: standalone=%d batch=%d", len(standaloneOps), len(batchOps))
	}

	sOp := standaloneOps[0].(map[string]any)
	bOp := batchOps[0].(map[string]any)

	// Both must have status: "noop"
	if s, _ := sOp["status"].(string); s != "noop" {
		t.Errorf("standalone status = %q, want \"noop\"\nop: %s", s, mustJSON(sOp))
	}
	if s, _ := bOp["status"].(string); s != "noop" {
		t.Errorf("batch status = %q, want \"noop\"\nop: %s", s, mustJSON(bOp))
	}

	// Neither should have legacy "noop" or "ok" boolean fields
	for name, op := range map[string]map[string]any{"standalone": sOp, "batch": bOp} {
		if _, has := op["noop"]; has {
			t.Errorf("%s op has legacy 'noop' field", name)
		}
		if _, has := op["ok"]; has {
			t.Errorf("%s op has legacy 'ok' field", name)
		}
	}
}

func filterEnv(env []string, key string) []string {
	out := make([]string, 0, len(env))
	prefix := key + "="
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// TestStandaloneEditAutoVerify verifies that standalone edit and write
// commands auto-verify after successful mutations (parity with batch).
func TestStandaloneEditAutoVerify(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")
	os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module test\ngo 1.21\n"), 0644)

	t.Run("edit verify passes", func(t *testing.T) {
		cmd := exec.Command(binary, "edit", "hello.go",
			"--old-text", "func main() {}",
			"--new-text", "func main() { println(\"ok\") }")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=autoverify_%d", parityCounter.Add(1)))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("edit failed: %v\n%s", err, out)
		}
		var env struct {
			OK     bool           `json:"ok"`
			Verify map[string]any `json:"verify"`
		}
		json.Unmarshal(out, &env)
		if env.Verify == nil {
			t.Fatal("standalone edit should auto-verify, but verify is null")
		}
		if status, _ := env.Verify["status"].(string); status != "passed" {
			t.Errorf("verify status should be passed, got: %s", mustJSON(env.Verify))
		}
	})

	t.Run("edit verify catches bad code", func(t *testing.T) {
		os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
		run(t, binary, repoDir, "reindex")

		cmd := exec.Command(binary, "edit", "hello.go",
			"--old-text", "func main() {}",
			"--new-text", "func main() { return badVar }")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=autoverify_%d", parityCounter.Add(1)))
		out, _ := cmd.CombinedOutput()

		var env struct {
			OK     bool           `json:"ok"`
			Verify map[string]any `json:"verify"`
		}
		json.Unmarshal(out, &env)
		if env.OK {
			t.Error("ok should be false when verify fails")
		}
		if env.Verify == nil {
			t.Fatal("verify field missing")
		}
		if status, _ := env.Verify["status"].(string); status == "passed" {
			t.Error("verify.status should be failed for bad code")
		}
	})

	t.Run("dry-run skips verify", func(t *testing.T) {
		os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
		run(t, binary, repoDir, "reindex")

		cmd := exec.Command(binary, "edit", "hello.go",
			"--old-text", "package main", "--new-text", "package test", "--dry-run")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=autoverify_%d", parityCounter.Add(1)))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dry-run failed: %v\n%s", err, out)
		}
		var env struct {
			Verify map[string]any `json:"verify"`
		}
		json.Unmarshal(out, &env)
		if env.Verify == nil {
			t.Fatal("dry-run should set verify to skipped, got null")
		}
		if status, _ := env.Verify["status"].(string); status != "skipped" {
			t.Errorf("dry-run verify should have status=skipped, got: %s", mustJSON(env.Verify))
		}
	})

	t.Run("write auto-verifies", func(t *testing.T) {
		cmd := exec.Command(binary, "write", "extra.go",
			"--content", "package main\n\nfunc extra() int { return 1 }\n")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=autoverify_%d", parityCounter.Add(1)))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("write failed: %v\n%s", err, out)
		}
		var env struct {
			Verify map[string]any `json:"verify"`
		}
		json.Unmarshal(out, &env)
		if env.Verify == nil {
			t.Fatal("standalone write should auto-verify, but verify is null")
		}
	})

	t.Run("verify failure exit code is 2", func(t *testing.T) {
		os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
		run(t, binary, repoDir, "reindex")

		cmd := exec.Command(binary, "edit", "hello.go",
			"--old-text", "func main() {}",
			"--new-text", "func main() { return badVar }")
		cmd.Dir = repoDir
		cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=autoverify_%d", parityCounter.Add(1)))
		_, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("expected non-zero exit for verify failure")
		}
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 2 {
				t.Errorf("exit code = %d, want 2 (verify-only failure)", ee.ExitCode())
			}
		}
	})
}

// TestErrorCodeField verifies that failed ops include an error_code field
// with a specific code, not just a generic string.
func TestErrorCodeField(t *testing.T) {
	binary, repoDir := setupContractRepo(t, "package main\n\nfunc main() {}\n")

	tests := []struct {
		name     string
		args     []string
		wantCode string
	}{
		{"standalone file not found", []string{"read", "nonexistent.go"}, "file_not_found"},
		{"standalone symbol not found", []string{"read", "hello.go:noSuchSymbol"}, "not_found"},
		{"batch file not found", []string{"-r", "nonexistent.go"}, "file_not_found"},
		{"batch symbol not found", []string{"-r", "hello.go:noSuchSymbol"}, "not_found"},
		{"standalone edit not found", []string{"edit", "hello.go", "--old-text", "XYZZY", "--new-text", "x"}, "not_found"},
		{"batch edit not found", []string{"-e", "hello.go", "--old", "XYZZY", "--new", "x"}, "not_found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=errcode_%d", parityCounter.Add(1)))
			out, _ := cmd.CombinedOutput()

			var env struct {
				Ops []map[string]any `json:"ops"`
			}
			json.Unmarshal(out, &env)
			if len(env.Ops) == 0 {
				t.Fatalf("no ops: %s", out)
			}
			code, _ := env.Ops[0]["error_code"].(string)
			if code != tt.wantCode {
				t.Errorf("error_code = %q, want %q\nop: %s", code, tt.wantCode, mustJSON(env.Ops[0]))
			}
		})
	}
}

// TestMapCodeFirst verifies that map prioritizes code files over
// documentation files when budget-constrained.
func TestMapCodeFirst(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	repoDir, _ = filepath.EvalSymlinks(repoDir)

	os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Title\n\n## Section\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n"), 0644)
	run(t, binary, repoDir, "reindex")

	cmd := exec.Command(binary, "map", "--budget", "100")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=mapcode_%d", parityCounter.Add(1)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("map failed: %v\n%s", err, out)
	}

	var env struct {
		Ops []struct {
			Content []struct {
				File string `json:"file"`
			} `json:"content"`
		} `json:"ops"`
	}
	json.Unmarshal(out, &env)
	if len(env.Ops) == 0 || len(env.Ops[0].Content) == 0 {
		t.Fatalf("no content in map output: %s", out)
	}

	firstFile := env.Ops[0].Content[0].File
	if !strings.HasSuffix(firstFile, ".go") {
		t.Errorf("first file in map should be .go, got %q", firstFile)
	}
}

// TestMultiFileReadProducesMultipleOps verifies that standalone multi-file
// read produces one op per file, matching batch behavior.
func TestMultiFileReadProducesMultipleOps(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	repoDir, _ = filepath.EvalSymlinks(repoDir)
	os.WriteFile(filepath.Join(repoDir, "a.go"), []byte("package main\nfunc A() {}\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "b.go"), []byte("package main\nfunc B() {}\n"), 0644)
	run(t, binary, repoDir, "reindex")

	cmd := exec.Command(binary, "read", "a.go", "b.go")
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=multifile_%d", parityCounter.Add(1)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("multi-file read failed: %v\n%s", err, out)
	}

	var env struct {
		OK  bool `json:"ok"`
		Ops []map[string]interface{} `json:"ops"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !env.OK {
		t.Fatalf("expected ok=true, got false\n%s", out)
	}
	if len(env.Ops) != 2 {
		t.Fatalf("expected 2 ops (one per file), got %d\n%s", len(env.Ops), out)
	}
	// Each op should have file, content, hash
	for i, op := range env.Ops {
		if op["file"] == nil {
			t.Errorf("op[%d] missing file", i)
		}
		if op["content"] == nil {
			t.Errorf("op[%d] missing content", i)
		}
		if op["hash"] == nil {
			t.Errorf("op[%d] missing hash", i)
		}
	}
}

// TestMutationOpsNeverHaveOKField verifies that edit and write results do not
// include an "ok" field at the op level — ok belongs on the envelope only.
func TestMutationOpsNeverHaveOKField(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	repoDir, _ = filepath.EvalSymlinks(repoDir)
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	run(t, binary, repoDir, "reindex")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"edit dry-run", []string{"edit", "main.go", "--old-text", "package main", "--new-text", "package test", "--dry-run"}},
		{"edit applied", []string{"edit", "main.go", "--old-text", "package main", "--new-text", "package test"}},
		{"write new file", []string{"write", "new.go", "--content", "package main"}},
		{"write overwrite", []string{"write", "new.go", "--content", "package test"}},
		{"write dry-run", []string{"write", "new.go", "--content", "package dry", "--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, tc.args...)
			cmd.Dir = repoDir
			cmd.Env = append(os.Environ(), fmt.Sprintf("EDR_SESSION=mutok_%d", parityCounter.Add(1)))
			out, _ := cmd.CombinedOutput()

			var env struct {
				Ops []map[string]interface{} `json:"ops"`
			}
			if err := json.Unmarshal(out, &env); err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}
			if len(env.Ops) == 0 {
				t.Fatalf("no ops\n%s", out)
			}
			for i, op := range env.Ops {
				if _, has := op["ok"]; has {
					t.Errorf("op[%d] has 'ok' field — should only be on envelope\n%s", i, out)
				}
				if op["status"] == nil {
					t.Errorf("op[%d] missing 'status' field\n%s", i, out)
				}
			}

			// Restore file for next subtest
			os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
		})
	}
}
