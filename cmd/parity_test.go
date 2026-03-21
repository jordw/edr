package cmd_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

var parityCounter atomic.Int64

// TestBatchStandaloneParity verifies that standalone and batch commands
// produce identical ops arrays (modulo expected differences like op_id,
// session, and mtime).
func TestBatchStandaloneParity(t *testing.T) {
	binary := buildBinary(t)

	// Create a temp repo with a Go file.
	repoDir := t.TempDir()
	goFile := filepath.Join(repoDir, "hello.go")
	os.WriteFile(goFile, []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n\nfunc helper() int {\n\treturn 42\n}\n"), 0644)

	// Index the temp repo.
	run(t, binary, repoDir, "reindex")

	tests := []struct {
		name           string
		standaloneArgs []string
		batchArgs      []string
	}{
		{
			name:           "read",
			standaloneArgs: []string{"read", "hello.go"},
			batchArgs:      []string{"-r", "hello.go"},
		},
		{
			name:           "read signatures",
			standaloneArgs: []string{"read", "hello.go", "--signatures"},
			batchArgs:      []string{"-r", "hello.go", "--sig"},
		},
		{
			name:           "read sig alias standalone",
			standaloneArgs: []string{"read", "hello.go", "--sig"},
			batchArgs:      []string{"-r", "hello.go", "--sig"},
		},
		{
			name:           "search symbol",
			standaloneArgs: []string{"search", "main"},
			batchArgs:      []string{"-s", "main"},
		},
		{
			name:           "search text",
			standaloneArgs: []string{"search", "main", "--text"},
			batchArgs:      []string{"-s", "main", "--text"},
		},
		{
			name:           "edit dry-run",
			standaloneArgs: []string{"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run"},
			batchArgs:      []string{"-e", "hello.go", "--old", "package main", "--new", "package test", "--dry-run"},
		},
	}

	// Fields to ignore when comparing ops.
	ignoreFields := map[string]bool{
		"op_id":   true,
		"session": true,
		"mtime":   true,
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run standalone command.
			standaloneOps := runAndExtractOps(t, binary, repoDir, tt.standaloneArgs)
			// Run batch command.
			batchOps := runAndExtractOps(t, binary, repoDir, tt.batchArgs)

			if len(standaloneOps) != len(batchOps) {
				t.Fatalf("ops count mismatch: standalone=%d, batch=%d\nstandalone: %s\nbatch: %s",
					len(standaloneOps), len(batchOps),
					mustJSON(standaloneOps), mustJSON(batchOps))
			}

			for i := range standaloneOps {
				sOp, ok1 := standaloneOps[i].(map[string]interface{})
				bOp, ok2 := batchOps[i].(map[string]interface{})
				if !ok1 || !ok2 {
					t.Fatalf("op[%d] is not a JSON object", i)
				}

				// Collect all keys from both.
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

					sVal, sHas := sOp[k]
					bVal, bHas := bOp[k]

					if sHas != bHas {
						t.Errorf("op[%d] field %q: standalone has=%v, batch has=%v", i, k, sHas, bHas)
						continue
					}

					if !reflect.DeepEqual(sVal, bVal) {
						t.Errorf("op[%d] field %q mismatch:\n  standalone: %v\n  batch:      %v", i, k, sVal, bVal)
					}
				}
			}
		})
	}
}

// runAndExtractOps runs the binary with args and returns the ops array from the JSON envelope.
func runAndExtractOps(t *testing.T, binary, dir string, args []string) []interface{} {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	// Use a unique session ID per invocation to avoid session caching
	// causing the second call to return "unchanged" instead of full content.
	cmd.Env = testEnv( fmt.Sprintf("EDR_SESSION=parity_%d", parityCounter.Add(1)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}

	var envelope struct {
		Ops []interface{} `json:"ops"`
	}
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("failed to parse JSON from %v: %v\n%s", args, err, out)
	}
	if envelope.Ops == nil {
		t.Fatalf("no ops array in output of %v:\n%s", args, out)
	}
	return envelope.Ops
}

func mustJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(b)
}
