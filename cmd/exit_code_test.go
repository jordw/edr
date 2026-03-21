package cmd_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStandaloneExitCodes verifies that every standalone command path
// returns non-zero exit when ok:false.
func TestStandaloneExitCodes(t *testing.T) {
	binary := buildBinary(t)

	// Create a temp repo with an index so commands don't fail on missing index.
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\nfunc main() {}\n"), 0644)

	// Index the temp repo first.
	run(t, binary, repoDir, "reindex")

	tests := []struct {
		name     string
		args     []string
		wantOK   bool
		wantExit int // 0 = success, 1 = failure
	}{
		// Successful paths
		{"read existing file", []string{"read", "hello.go"}, true, 0},
		{"map repo", []string{"map"}, true, 0},
		{"search existing symbol", []string{"search", "main"}, true, 0},

		// Failure paths — must all exit non-zero
		{"read missing file", []string{"read", "does_not_exist.go"}, false, 1},
		{"edit missing file", []string{"edit", "does_not_exist.go", "--old-text", "x", "--new-text", "y"}, false, 1},
		{"search no results", []string{"search", "zzz_nonexistent_symbol_zzz"}, true, 0}, // no results is not an error
		{"refs missing symbol", []string{"refs", "zzz_nonexistent_zzz"}, false, 1},
		{"map missing file", []string{"map", "does_not_exist.go"}, false, 1},

		// Standalone edit with --old-text/--new-text flags (tests hyphen→underscore normalization)
		{"edit dry-run with flags", []string{"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run"}, true, 0},

		// Batch with --verbose (tests boolean-aware findBatchFlag)
		{"verbose batch read", []string{"--verbose", "-r", "hello.go"}, true, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			out, err := cmd.CombinedOutput()

			var env struct {
				OK bool `json:"ok"`
			}
			if jsonErr := json.Unmarshal(out, &env); jsonErr != nil {
				t.Fatalf("failed to parse output as JSON: %v\noutput: %s", jsonErr, out)
			}

			if env.OK != tt.wantOK {
				t.Errorf("ok = %v, want %v\noutput: %s", env.OK, tt.wantOK, out)
			}

			exitCode := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				} else {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if exitCode != tt.wantExit {
				t.Errorf("exit code = %d, want %d\noutput: %s", exitCode, tt.wantExit, out)
			}
		})
	}
}

// TestNoIndexError verifies that read-only commands fail with a clear error
// when the repository has not been indexed.
// TestAutoIndexOnFirstUse verifies that read-only operations auto-index
// on an unindexed repo instead of failing with no_index.
func TestAutoIndexOnFirstUse(t *testing.T) {
	binary := buildBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{"read", []string{"read", "hello.go"}},
		{"search", []string{"search", "main"}},
		{"map", []string{"map"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\nfunc main() {}\n"), 0644)

			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			err := cmd.Run()

			if err != nil {
				t.Fatalf("expected success (auto-index), got error: %v\nstdout: %s", err, stdout.String())
			}

			var env struct {
				OK bool `json:"ok"`
			}
			if jsonErr := json.Unmarshal(stdout.Bytes(), &env); jsonErr != nil {
				t.Fatalf("failed to parse output as JSON: %v\nstdout: %s", jsonErr, stdout.String())
			}
			if !env.OK {
				t.Errorf("expected ok:true after auto-index, got ok:false\nstdout: %s", stdout.String())
			}
		})
	}
}

// TestBatchAutoIndex verifies that batch read-only operations auto-index
// on an unindexed repo instead of failing.
func TestBatchAutoIndex(t *testing.T) {
	binary := buildBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{"batch read", []string{"-r", "hello.go"}},
		{"batch search", []string{"-s", "main"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoDir := t.TempDir()
			os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\n"), 0644)

			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = repoDir
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			err := cmd.Run()
			if err != nil {
				t.Fatalf("expected success (auto-index), got error: %v\nstdout: %s", err, stdout.String())
			}

			var env struct {
				OK bool `json:"ok"`
			}
			if jsonErr := json.Unmarshal(stdout.Bytes(), &env); jsonErr != nil {
				t.Fatalf("failed to parse output as JSON: %v\nstdout: %s", jsonErr, stdout.String())
			}
			if !env.OK {
				t.Errorf("expected ok:true after auto-index\nstdout: %s", stdout.String())
			}
		})
	}
}

// TestVerifyWithoutIndex verifies that standalone verify works without an index.
func TestVerifyWithoutIndex(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module test\n"), 0644)

	cmd := exec.Command(binary, "verify")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("verify without index failed: %v\n%s", err, out)
	}

	var env struct {
		OK     bool `json:"ok"`
		Verify any  `json:"verify"`
	}
	if jsonErr := json.Unmarshal(out, &env); jsonErr != nil {
		t.Fatalf("failed to parse: %v\n%s", jsonErr, out)
	}
	if !env.OK {
		t.Errorf("expected ok:true\n%s", out)
	}
	if env.Verify == nil {
		t.Error("verify field should be set")
	}
}

// TestVerifyNoSideEffects verifies that verify does not create .edr directory.
func TestVerifyNoSideEffects(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module test\n"), 0644)

	// Run verify
	cmd := exec.Command(binary, "verify")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verify failed: %v\n%s", err, out)
	}

	// .edr must not exist
	if _, err := os.Stat(filepath.Join(repoDir, ".edr")); err == nil {
		t.Error(".edr directory was created by verify — verify should have no side effects")
	}

	// Subsequent read should auto-index and succeed.
	cmd = exec.Command(binary, "read", "main.go")
	cmd.Dir = repoDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("expected read to auto-index and succeed, got error: %v\nstdout: %s", err, stdout.String())
	}
}

// TestSetupEnvelope verifies that setup --json wraps output in standard envelope.
func TestSetupEnvelope(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\n"), 0644)

	cmd := exec.Command(binary, "setup", "--json", "--skip-index", "--generic")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("setup --json failed: %v\n%s", err, out)
	}

	var env struct {
		SchemaVersion int    `json:"schema_version"`
		OK            bool   `json:"ok"`
		Command       string `json:"command"`
		Ops           []any  `json:"ops"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("failed to parse envelope: %v\noutput: %s", err, out)
	}
	if env.Command != "setup" {
		t.Errorf("command = %q, want setup", env.Command)
	}
	if env.SchemaVersion != 2 {
		t.Errorf("schema_version = %d, want 2", env.SchemaVersion)
	}
	if !env.OK {
		t.Errorf("ok = false, want true\noutput: %s", out)
	}
	if len(env.Ops) == 0 {
		t.Error("expected at least one op")
	}
}

// TestVerifyOutputShape verifies that standalone verify uses the verify field, not ops.
func TestVerifyOutputShape(t *testing.T) {
	binary := buildBinary(t)
	repoDir := t.TempDir()
	os.WriteFile(filepath.Join(repoDir, "hello.go"), []byte("package main\nfunc main() {}\n"), 0644)
	run(t, binary, repoDir, "reindex")

	cmd := exec.Command(binary, "verify")
	cmd.Dir = repoDir
	out, _ := cmd.CombinedOutput() // may exit non-zero if build fails

	var env struct {
		Command string `json:"command"`
		Ops     []any  `json:"ops"`
		Verify  any    `json:"verify"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("failed to parse envelope: %v\noutput: %s", err, out)
	}
	if env.Command != "verify" {
		t.Errorf("command = %q, want verify", env.Command)
	}
	if len(env.Ops) != 0 {
		t.Errorf("ops should be empty for verify, got %d ops", len(env.Ops))
	}
	if env.Verify == nil {
		t.Error("verify field should be set")
	}
}

// buildBinary compiles the edr binary for testing.
func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "edr")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = findRepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build: %v\n%s", err, out)
	}
	return binary
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up to find go.mod
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root")
		}
		dir = parent
	}
}

func run(t *testing.T, binary, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
}
