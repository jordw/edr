package cmd_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestMain(m *testing.M) {
	os.Setenv("EDR_NO_HINTS", "1")
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Shared test helpers
// ---------------------------------------------------------------------------

var (
	cachedBinary string
	buildOnce    sync.Once
	buildErr     error
)

// buildBinary compiles the edr binary once and caches the result.
func buildBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "edr-test-bin-*")
		if err != nil {
			buildErr = err
			return
		}
		binary := filepath.Join(dir, "edr")
		cmd := exec.Command("go", "build", "-o", binary, ".")
		cmd.Dir = findRepoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("failed to build: %v\n%s", err, out)
			return
		}
		cachedBinary = binary
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return cachedBinary
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
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
	cmd.Env = append(os.Environ(), "EDR_NO_HINTS=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
}

// testEnv returns os.Environ() with EDR_NO_HINTS=1.
func testEnv(extra ...string) []string {
	return append(append(os.Environ(), "EDR_NO_HINTS=1"), extra...)
}
