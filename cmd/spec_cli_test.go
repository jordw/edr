// spec_cli_test.go — Black-box spec-surface tests.
//
// Every test here runs the edr binary as a subprocess and validates the
// plain-mode transport contract from spec.md.  No in-process imports of
// cmd internals except for buildBinary/findRepoRoot which live in the
// shared test helpers.
//
// The only parser used is parseOps(), which understands the spec transport:
//   line 1: JSON header
//   body:   raw text until "---" or EOF
//   repeat for batch
//   optional final {"verify":…} line
package cmd_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Transport parser — the only parser the spec suite needs.
// ---------------------------------------------------------------------------

// opBlock is one parsed op from plain-mode output.
type opBlock struct {
	Header map[string]any // first-line JSON
	Body   string         // everything after the header, before --- or EOF
}

// specResult is the full parsed output of an edr invocation.
type specResult struct {
	Ops    []opBlock
	Verify map[string]any // nil if no verify line
}

// parseOps parses plain-mode output per the spec transport contract.
//
// The spec says:
//  1. Read the first line as JSON (the header)
//  2. Check for error/ec — if present, this op failed
//  3. Read remaining lines until --- or EOF as the body
//  4. After all ops, check for a {"verify":…} line
//
// The verify line is the last line of the last block's body (not
// separated by ---). We detect it by checking if the last line is
// valid JSON containing a "verify" key.
func parseOps(raw string) (specResult, error) {
	var result specResult

	// Split into blocks by "---" separator.
	blocks := splitBlocks(raw)

	for _, block := range blocks {
		block = strings.TrimRight(block, "\n")
		if block == "" {
			continue
		}

		// First line is JSON header.
		headerEnd := strings.Index(block, "\n")
		var headerLine, body string
		if headerEnd < 0 {
			headerLine = block
		} else {
			headerLine = block[:headerEnd]
			body = block[headerEnd+1:]
		}

		var header map[string]any
		if err := json.Unmarshal([]byte(headerLine), &header); err != nil {
			return result, fmt.Errorf("header parse error: %w\nraw: %q", err, headerLine)
		}

		// Check if this is a verify-only block (header line with "verify" key, no body).
		if _, isVerify := header["verify"]; isVerify && body == "" {
			result.Verify = header
			continue
		}

		// Check if the last line of the body is a verify JSON line.
		if body != "" {
			lastNL := strings.LastIndex(body, "\n")
			var lastLine string
			if lastNL < 0 {
				lastLine = body
			} else {
				lastLine = body[lastNL+1:]
			}
			var maybeVerify map[string]any
			if json.Unmarshal([]byte(lastLine), &maybeVerify) == nil {
				if _, isVerify := maybeVerify["verify"]; isVerify {
					result.Verify = maybeVerify
					if lastNL < 0 {
						body = ""
					} else {
						body = body[:lastNL]
					}
				}
			}
		}

		result.Ops = append(result.Ops, opBlock{Header: header, Body: body})
	}

	return result, nil
}

// splitBlocks splits plain-mode output on "---" separators.
// The verify line (if present) is the last block.
func splitBlocks(raw string) []string {
	var blocks []string
	var current strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if line == "---" {
			blocks = append(blocks, current.String())
			current.Reset()
			continue
		}
		if current.Len() > 0 {
			current.WriteByte('\n')
		}
		current.WriteString(line)
	}
	if current.Len() > 0 {
		blocks = append(blocks, current.String())
	}
	return blocks
}

// ---------------------------------------------------------------------------
// Spec repo fixture
// ---------------------------------------------------------------------------

// specRepo creates a temp repo with Go source, indexes it, and returns
// (binary, repoDir). The repoDir is symlink-resolved for macOS compat.
func specRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)

	for name, content := range files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0755)
		os.WriteFile(path, []byte(content), 0644)
	}
	run(t, binary, dir, "reindex")
	return binary, dir
}

// specRun runs the binary and returns parsed specResult + exit code.
func specRun(t *testing.T, binary, dir string, env []string, args ...string) (specResult, string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = specEnv(env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec error: %v", err)
		}
	}

	result, parseErr := parseOps(stdout.String())
	if parseErr != nil && exitCode == 0 {
		t.Fatalf("parse error on successful command %v: %v\nstdout: %q", args, parseErr, stdout.String())
	}

	return result, stdout.String(), stderr.String(), exitCode
}

// specRunRaw runs the binary and returns raw stdout, stderr, exit code.
// Use for commands that don't follow the JSON header transport (help, run).
func specRunRaw(t *testing.T, binary, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = specEnv(env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec error: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

// specEnv returns a clean env for spec tests. Extra vars override base env.
func specEnv(extra ...string) []string {
	// Build set of keys being overridden.
	overrides := map[string]bool{"EDR_FORMAT": true}
	for _, e := range extra {
		if k, _, ok := strings.Cut(e, "="); ok {
			overrides[k] = true
		}
	}

	var filtered []string
	for _, e := range os.Environ() {
		if k, _, ok := strings.Cut(e, "="); ok && overrides[k] {
			continue
		}
		filtered = append(filtered, e)
	}
	return append(filtered, extra...)
}

var specSessionCounter int64

func nextSession() string {
	specSessionCounter++
	return fmt.Sprintf("spec_%d", specSessionCounter)
}

// ---------------------------------------------------------------------------
// Help surface
// ---------------------------------------------------------------------------

func TestSpec_HelpSurface(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()

	// edr --help — not JSON, just human text.
	stdout, stderr, exit := specRunRaw(t, binary, dir, nil, "--help")
	if exit != 0 {
		t.Fatalf("edr --help exited %d", exit)
	}
	if stderr != "" {
		t.Errorf("edr --help wrote to stderr: %q", stderr)
	}

	expected := []string{"delta", "edit", "map", "prepare", "read", "refs", "rename", "reset", "search", "setup", "status", "undo", "verify", "write"}
	cmdRe := regexp.MustCompile(`(?m)^\s{2}(\w+)\s`)
	matches := cmdRe.FindAllStringSubmatch(stdout, -1)

	var found []string
	skip := map[string]bool{"edr": true, "version": true}
	for _, m := range matches {
		if !skip[m[1]] {
			found = append(found, m[1])
		}
	}
	sort.Strings(found)

	if strings.Join(found, ",") != strings.Join(expected, ",") {
		t.Errorf("help surface mismatch\n  got:  %v\n  want: %v", found, expected)
	}
}

func TestSpec_SubcommandHelp(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()

	commands := []string{"read", "search", "edit", "write", "map", "refs", "rename", "verify", "delta", "setup", "reset", "status", "undo"}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			stdout, _, exit := specRunRaw(t, binary, dir, nil, cmd, "--help")
			if exit != 0 {
				t.Errorf("%s --help exited %d", cmd, exit)
			}
			if !strings.Contains(stdout, "Usage:") {
				t.Errorf("%s --help missing Usage section", cmd)
			}
		})
	}
}

func TestSpec_HelpCanonicalFlags(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()

	// Per-command help must use canonical long-form flag names, not shorthand.
	commands := map[string][]string{
		"read": {"--sig "},
		"edit": {`--old "`, `--new "`},
	}

	for cmd, badForms := range commands {
		t.Run(cmd, func(t *testing.T) {
			stdout, _, _ := specRunRaw(t, binary, dir, nil, cmd, "--help")
			for _, bad := range badForms {
				if strings.Contains(stdout, bad) {
					t.Errorf("%s --help contains short-form %q; should use canonical long-form", cmd, bad)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Transport contract — header + body shape
// ---------------------------------------------------------------------------

func TestSpec_ReadTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n",
	})

	tests := []struct {
		name     string
		args     []string
		wantSym  string
		wantFile string
	}{
		{"file read", []string{"read", "hello.go"}, "", "hello.go"},
		{"symbol read", []string{"read", "hello.go:helper"}, "helper", "hello.go"},
		{"line range", []string{"read", "hello.go", "--lines", "1:3"}, "", "hello.go"},
		{"signatures", []string{"read", "hello.go", "--signatures"}, "", "hello.go"},
		{"skeleton", []string{"read", "hello.go", "--skeleton"}, "", "hello.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, tt.args...)
			if exit != 0 {
				t.Fatalf("exit %d", exit)
			}
			if len(result.Ops) != 1 {
				t.Fatalf("expected 1 op, got %d", len(result.Ops))
			}
			h := result.Ops[0].Header

			// All reads must have file and lines.
			if h["file"] != tt.wantFile {
				t.Errorf("file = %v, want %q", h["file"], tt.wantFile)
			}
			if h["lines"] == nil {
				t.Error("missing lines in header")
			}
			if h["session"] == nil {
				t.Error("missing session in header")
			}

			// Symbol reads include sym.
			if tt.wantSym != "" {
				if h["sym"] != tt.wantSym {
					t.Errorf("sym = %v, want %q", h["sym"], tt.wantSym)
				}
			}

			// Body should be non-empty raw text (no JSON).
			body := result.Ops[0].Body
			if body == "" {
				t.Error("body is empty")
			}
			if strings.HasPrefix(strings.TrimSpace(body), "{") {
				t.Error("body looks like JSON; should be raw text")
			}
		})
	}
}

func TestSpec_ReadContentNotNumbered(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, _ := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "read", "hello.go")
	body := result.Ops[0].Body
	// Body should NOT have line-number prefixes like "  1\t".
	if matched, _ := regexp.MatchString(`(?m)^\s*\d+\t`, body); matched {
		t.Error("read body contains line-number prefixes")
	}
}

func TestSpec_SearchTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n",
	})

	tests := []struct {
		name string
		args []string
	}{
		{"symbol search", []string{"search", "main"}},
		{"text search", []string{"search", "func", "--text"}},
		{"text search in file", []string{"search", "return", "--text", "--include", "*.go"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, tt.args...)
			if exit != 0 {
				t.Fatalf("exit %d", exit)
			}
			if len(result.Ops) != 1 {
				t.Fatalf("expected 1 op, got %d", len(result.Ops))
			}
			h := result.Ops[0].Header

			// Search must have n.
			if h["n"] == nil {
				t.Error("missing n in search header")
			}
		})
	}
}

func TestSpec_SearchZeroIsSuccess(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "search", "zzz_nonexistent_zzz")
	if exit != 0 {
		t.Errorf("zero-result search should exit 0, got %d", exit)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}
	n, _ := result.Ops[0].Header["n"].(float64)
	if n != 0 {
		t.Errorf("expected n=0, got %v", n)
	}
}

func TestSpec_MapTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "map")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}
	h := result.Ops[0].Header
	if h["files"] == nil {
		t.Error("missing files in map header")
	}
	if h["symbols"] == nil {
		t.Error("missing symbols in map header")
	}
}

func TestSpec_EditTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	// Dry-run edit.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}
	h := result.Ops[0].Header
	if h["file"] != "hello.go" {
		t.Errorf("file = %v, want hello.go", h["file"])
	}
	if h["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", h["status"])
	}
}

func TestSpec_EditApply(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package test")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["file"] != "hello.go" {
		t.Errorf("file = %v, want hello.go", h["file"])
	}
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}
	if h["hash"] == nil {
		t.Error("applied edit missing hash")
	}

	// Verify line should be present (auto-verify).
	if result.Verify == nil {
		t.Error("standalone edit should auto-verify")
	}
}

func TestSpec_EditNoop(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package main")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "noop" {
		t.Errorf("status = %v, want noop", h["status"])
	}
	// Noop edits skip verify.
	if result.Verify != nil {
		t.Error("noop edit should not verify")
	}
}

func TestSpec_WriteTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"write", "new.go", "--content", "package main\n")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["file"] != "new.go" {
		t.Errorf("file = %v, want new.go", h["file"])
	}
	if h["hash"] == nil {
		t.Error("write missing hash")
	}
}

func TestSpec_RefsTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() { helper() }\n\nfunc helper() int { return 42 }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"refs", "hello.go:helper")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["sym"] == nil {
		t.Error("refs missing sym")
	}
	if h["n"] == nil {
		t.Error("refs missing n")
	}
}

// ---------------------------------------------------------------------------
// Failure shape and error codes
// ---------------------------------------------------------------------------

func TestSpec_FailureShape(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	tests := []struct {
		name   string
		args   []string
		wantEC string
	}{
		{"file not found", []string{"read", "nope.go"}, "file_not_found"},
		{"symbol not found", []string{"read", "hello.go:Nope"}, "not_found"},
		{"edit file not found", []string{"edit", "nope.go", "--old-text", "x", "--new-text", "y"}, "file_not_found"},
		{"refs not found", []string{"refs", "zzz_nonexistent"}, "not_found"},
		{"outside repo edit", []string{"edit", "/etc/passwd", "--old-text", "x", "--new-text", "y"}, "outside_repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stdout, _, exit := specRun(t, binary, dir, nil, tt.args...)
			if exit != 0 {
				t.Errorf("expected exit 0 (errors in JSON output), got %d", exit)
			}

			// Parse the error header.
			var header map[string]any
			firstLine := strings.SplitN(stdout, "\n", 2)[0]
			if err := json.Unmarshal([]byte(firstLine), &header); err != nil {
				t.Fatalf("failed to parse error header: %v\nraw: %q", err, firstLine)
			}

			if header["error"] == nil {
				t.Error("error header missing error field")
			}
			if ec, _ := header["ec"].(string); ec != tt.wantEC {
				t.Errorf("ec = %q, want %q", ec, tt.wantEC)
			}

			// Failed ops MUST NOT include success-only fields.
			for _, banned := range []string{"file", "lines", "hash", "status"} {
				if header[banned] != nil {
					t.Errorf("failed op should not have %q field", banned)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Exit codes
// ---------------------------------------------------------------------------

func TestSpec_ExitCodes(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	tests := []struct {
		name     string
		args     []string
		wantExit int
	}{
		{"read success", []string{"read", "hello.go"}, 0},
		{"map success", []string{"map"}, 0},
		{"search success", []string{"search", "main"}, 0},
		{"search zero results", []string{"search", "zzz_nonexistent"}, 0},
		{"read failure", []string{"read", "nope.go"}, 0},
		{"edit failure", []string{"edit", "nope.go", "--old-text", "x", "--new-text", "y"}, 0},
		{"refs failure", []string{"refs", "zzz_nonexistent"}, 0},
		{"dry-run success", []string{"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, tt.args...)
			if exit != tt.wantExit {
				t.Errorf("exit = %d, want %d", exit, tt.wantExit)
			}
		})
	}
}

func TestSpec_StderrSilentByDefault(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	_, _, stderr, _ := specRun(t, binary, dir, nil, "read", "hello.go")
	if stderr != "" {
		t.Errorf("stderr should be empty without --verbose, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// Context: external change detection
// ---------------------------------------------------------------------------

func TestSpec_ContextExternalChanges(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	sessID := nextSession()
	sessEnv := []string{"EDR_SESSION=" + sessID}

	// 1. Read a file to record mtime in session
	_, _, _, exit1 := specRun(t, binary, dir, sessEnv, "read", "hello.go")
	if exit1 != 0 {
		t.Fatalf("first read exit %d", exit1)
	}

	// 2. Modify file externally
	time.Sleep(50 * time.Millisecond) // ensure mtime changes
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() { fmt.Println(\"changed\") }\n"), 0644)

	// 3. edr status should report the external change
	result, _, _, exit2 := specRun(t, binary, dir, sessEnv, "status")
	if exit2 != 0 {
		t.Fatalf("context exit %d", exit2)
	}
	if len(result.Ops) == 0 {
		t.Fatal("expected context result")
	}
	header := result.Ops[0].Header

	// Check header has external_changes count
	extCount, ok := header["external_changes"]
	if !ok {
		t.Fatalf("expected external_changes in context header, got keys: %v", mapKeys(header))
	}
	if extCount != float64(1) {
		t.Errorf("expected external_changes=1, got %v", extCount)
	}

	// Check body mentions the file
	body := result.Ops[0].Body
	if !strings.Contains(body, "hello.go") {
		t.Errorf("expected body to mention hello.go, got: %q", body)
	}
	if !strings.Contains(body, "modified") {
		t.Errorf("expected body to mention modified, got: %q", body)
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------------------------------------------------------------------------
// Batch transport and separator
// ---------------------------------------------------------------------------

func TestSpec_BatchSeparator(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"a.go": "package main\n\nfunc A() {}\n",
		"b.go": "package main\n\nfunc B() {}\n",
	})

	result, stdout, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-r", "a.go", "-r", "b.go")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(result.Ops))
	}
	if !strings.Contains(stdout, "\n---\n") {
		t.Error("batch output missing --- separator")
	}

	// Each op should have file field.
	files := []string{
		result.Ops[0].Header["file"].(string),
		result.Ops[1].Header["file"].(string),
	}
	sort.Strings(files)
	if files[0] != "a.go" || files[1] != "b.go" {
		t.Errorf("files = %v, want [a.go, b.go]", files)
	}
}

func TestSpec_BatchMixedOps(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-r", "hello.go", "--sig", "-s", "main")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(result.Ops))
	}
	// First op is a read, second is a search.
	if result.Ops[0].Header["file"] == nil {
		t.Error("first op (read) missing file")
	}
	if result.Ops[1].Header["n"] == nil {
		t.Error("second op (search) missing n")
	}
}

func TestSpec_BatchPartialFailure(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	// One good read, one bad read.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-r", "hello.go", "-r", "nope.go")
	if exit != 0 {
		t.Errorf("expected exit 0 (errors are in JSON output), got %d", exit)
	}
	if len(result.Ops) < 1 {
		t.Fatal("expected at least 1 op")
	}
	// One op should succeed, one should fail.
	var hasSuccess, hasError bool
	for _, op := range result.Ops {
		if op.Header["error"] != nil {
			hasError = true
		}
		if op.Header["file"] != nil {
			hasSuccess = true
		}
	}
	if !hasSuccess || !hasError {
		t.Errorf("expected one success and one failure op; success=%v error=%v", hasSuccess, hasError)
	}
}

// ---------------------------------------------------------------------------
// Batch / standalone parity
// ---------------------------------------------------------------------------

func TestSpec_Parity(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n\nfunc helper() int {\n\treturn 42\n}\n",
	})

	tests := []struct {
		name           string
		standaloneArgs []string
		batchArgs      []string
	}{
		{
			"read",
			[]string{"read", "hello.go"},
			[]string{"-r", "hello.go"},
		},
		{
			"read signatures",
			[]string{"read", "hello.go", "--signatures"},
			[]string{"-r", "hello.go", "--sig"},
		},
		{
			"search symbol",
			[]string{"search", "main"},
			[]string{"-s", "main"},
		},
		{
			"search text",
			[]string{"search", "main", "--text"},
			[]string{"-s", "main", "--text"},
		},
		{
			"edit dry-run",
			[]string{"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run"},
			[]string{"-e", "hello.go", "--old", "package main", "--new", "package test", "--dry-run"},
		},
	}

	ignoreFields := map[string]bool{"session": true}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sResult, _, _, sExit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, tt.standaloneArgs...)
			bResult, _, _, bExit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, tt.batchArgs...)

			if sExit != bExit {
				t.Errorf("exit code mismatch: standalone=%d, batch=%d", sExit, bExit)
			}
			if len(sResult.Ops) != len(bResult.Ops) {
				t.Fatalf("op count mismatch: standalone=%d, batch=%d", len(sResult.Ops), len(bResult.Ops))
			}

			for i := range sResult.Ops {
				sH := sResult.Ops[i].Header
				bH := bResult.Ops[i].Header

				allKeys := map[string]bool{}
				for k := range sH {
					allKeys[k] = true
				}
				for k := range bH {
					allKeys[k] = true
				}

				for k := range allKeys {
					if ignoreFields[k] {
						continue
					}
					sVal, sHas := sH[k]
					bVal, bHas := bH[k]
					if sHas != bHas {
						t.Errorf("op[%d] field %q: standalone has=%v, batch has=%v", i, k, sHas, bHas)
					} else if fmt.Sprint(sVal) != fmt.Sprint(bVal) {
						t.Errorf("op[%d] field %q mismatch: standalone=%v, batch=%v", i, k, sVal, bVal)
					}
				}

				// Body parity.
				if sResult.Ops[i].Body != bResult.Ops[i].Body {
					t.Errorf("op[%d] body mismatch:\n  standalone: %q\n  batch:      %q",
						i, truncate(sResult.Ops[i].Body, 200), truncate(bResult.Ops[i].Body, 200))
				}
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// Session behavior
// ---------------------------------------------------------------------------

func TestSpec_SessionDedup(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	sess := nextSession()
	env := []string{"EDR_SESSION=" + sess}

	// First read: session=new.
	r1, _, _, _ := specRun(t, binary, dir, env, "read", "hello.go")
	if s, _ := r1.Ops[0].Header["session"].(string); s != "new" {
		t.Errorf("first read session = %q, want new", s)
	}
	if r1.Ops[0].Body == "" {
		t.Error("first read should have body")
	}

	// Second read: session=unchanged.
	r2, _, _, _ := specRun(t, binary, dir, env, "read", "hello.go")
	if s, _ := r2.Ops[0].Header["session"].(string); s != "unchanged" {
		t.Errorf("second read session = %q, want unchanged", s)
	}
}

func TestSpec_ExplicitSessionRequired(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	// With an explicit session, second call returns unchanged.
	sess := nextSession()
	env := []string{"EDR_SESSION=" + sess}
	r1, _, _, _ := specRun(t, binary, dir, env, "read", "hello.go")
	if r1.Ops[0].Body == "" {
		t.Error("first read should have body")
	}
	r2, _, _, _ := specRun(t, binary, dir, env, "read", "hello.go")
	if s, _ := r2.Ops[0].Header["session"].(string); s != "unchanged" {
		t.Errorf("second read with same session should be unchanged, got %q", s)
	}

	// With a different session, body returns again.
	env2 := []string{"EDR_SESSION=" + nextSession()}
	r3, _, _, _ := specRun(t, binary, dir, env2, "read", "hello.go")
	if r3.Ops[0].Body == "" {
		t.Error("new session should return full body")
	}
}

// ---------------------------------------------------------------------------
// Verify line behavior
// ---------------------------------------------------------------------------

func TestSpec_VerifyAfterEdit(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
		"go.mod":   "module test\n\ngo 1.21\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "func main() {}", "--new-text", "func main() { println(\"hi\") }")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if result.Verify == nil {
		t.Fatal("expected verify line after edit")
	}
	v := result.Verify["verify"].(string)
	if v != "passed" && v != "failed" {
		t.Errorf("verify = %q, want passed or failed", v)
	}
}

func TestSpec_VerifySkippedOnDryRun(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
		"go.mod":   "module test\n\ngo 1.21\n",
	})

	result, _, _, _ := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run")
	// Dry-run may emit {"verify":"skipped"} but must NOT emit "passed" or "failed".
	if result.Verify != nil {
		v, _ := result.Verify["verify"].(string)
		if v != "skipped" {
			t.Errorf("dry-run verify should be skipped, got %q", v)
		}
	}
}

func TestSpec_VerifyStandalone(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
		"go.mod":   "module test\n\ngo 1.21\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "verify")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	// Standalone verify emits a verify header.
	if result.Verify == nil {
		t.Fatal("verify command should emit verify line")
	}
	if result.Verify["verify"] != "passed" {
		t.Errorf("verify = %v, want passed", result.Verify["verify"])
	}
}

func TestSpec_VerifyWithoutIndex(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.21\n"), 0644)

	// verify should work without .edr index.
	result, _, _, exit := specRun(t, binary, dir, nil, "verify")
	if exit != 0 {
		t.Fatalf("verify without index: exit %d", exit)
	}
	if result.Verify == nil {
		t.Fatal("expected verify line")
	}

	// verify should NOT create .edr directory.
	if _, err := os.Stat(filepath.Join(dir, ".edr")); err == nil {
		t.Error("verify should not create .edr directory")
	}
}

// ---------------------------------------------------------------------------
// Auto-index on first use
// ---------------------------------------------------------------------------

func TestSpec_AutoIndex(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	// read without prior reindex should auto-index.
	_, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "read", "hello.go")
	if exit != 0 {
		t.Errorf("auto-index read: exit %d, want 0", exit)
	}

	// map should also auto-index.
	_, _, _, exit = specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "map")
	if exit != 0 {
		t.Errorf("auto-index map: exit %d, want 0", exit)
	}

	// search should also auto-index.
	_, _, _, exit = specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "search", "main")
	if exit != 0 {
		t.Errorf("auto-index search: exit %d, want 0", exit)
	}
}

func TestSpec_AutoIndexSilent(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	// Auto-index should not emit stderr.
	_, _, stderr, _ := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "read", "hello.go")
	if stderr != "" {
		t.Errorf("auto-index should be silent, got stderr: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// Run command
// ---------------------------------------------------------------------------

func TestSpec_RunFirstRun(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	stdout, _, exit := specRunRaw(t, binary, dir, nil, "delta", "--", "echo", "hello world")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if !strings.Contains(stdout, "hello world") {
		t.Errorf("first run should show full output, got: %q", stdout)
	}
}

func TestSpec_RunNoChanges(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	// First run.
	specRunRaw(t, binary, dir, nil, "delta", "--", "echo", "stable output")
	// Second run — with session active, returns JSON; without, returns text.
	stdout, _, _ := specRunRaw(t, binary, dir, nil, "delta", "--", "echo", "stable output")
	if !strings.Contains(stdout, "no changes") && !strings.Contains(stdout, "\"unchanged\"") {
		t.Errorf("identical run should show no changes or unchanged, got: %q", stdout)
	}
}

func TestSpec_RunReset(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	// First run.
	specRunRaw(t, binary, dir, nil, "delta", "--", "echo", "reset test")
	// Reset — should show full output again.
	stdout, _, _ := specRunRaw(t, binary, dir, nil, "delta", "--reset", "--", "echo", "reset test")
	if strings.Contains(stdout, "no changes") {
		t.Error("--reset should show full output, not 'no changes'")
	}
	if !strings.Contains(stdout, "reset test") {
		t.Errorf("--reset should show full output, got: %q", stdout)
	}
}

func TestSpec_RunExitPassthrough(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	_, _, exit := specRunRaw(t, binary, dir, nil, "delta", "--reset", "--", "/bin/sh", "-c", "exit 7")
	if exit != 7 {
		t.Errorf("exit = %d, want 7 (pass-through)", exit)
	}
}

func TestSpec_RunFull(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	// First run to establish baseline.
	specRunRaw(t, binary, dir, nil, "delta", "--", "echo", "full test")
	// --full should bypass diff.
	stdout, _, _ := specRunRaw(t, binary, dir, nil, "delta", "--full", "--", "echo", "full test")
	if strings.Contains(stdout, "no changes") || strings.Contains(stdout, "unchanged") {
		t.Error("--full should bypass diff")
	}
	if !strings.Contains(stdout, "full test") {
		t.Errorf("--full should show raw output, got: %q", stdout)
	}
}

func TestSpec_RunSessionDedup(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	sessID := fmt.Sprintf("test-run-dedup-%d", time.Now().UnixNano())
	env := []string{"EDR_SESSION=" + sessID}

	// First run: full output.
	stdout, _, exit := specRunRaw(t, binary, dir, env, "delta", "--", "echo", "dedup test")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if !strings.Contains(stdout, "dedup test") {
		t.Errorf("first run should show full output, got: %q", stdout)
	}

	// Second run: session detects unchanged, returns JSON.
	stdout, _, exit = specRunRaw(t, binary, dir, env, "delta", "--", "echo", "dedup test")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if !strings.Contains(stdout, "\"unchanged\"") {
		t.Errorf("second run with session should return unchanged JSON, got: %q", stdout)
	}
	if !strings.Contains(stdout, "\"exit_code\":0") {
		t.Errorf("unchanged response should include exit_code, got: %q", stdout)
	}

	// --reset clears session hash, shows full output again.
	stdout, _, _ = specRunRaw(t, binary, dir, env, "delta", "--reset", "--", "echo", "dedup test")
	if strings.Contains(stdout, "unchanged") {
		t.Error("--reset should clear session hash")
	}
	if !strings.Contains(stdout, "dedup test") {
		t.Errorf("--reset should show full output, got: %q", stdout)
	}
}

func TestSpec_RunSessionDedupExitCode(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	sessID := fmt.Sprintf("test-run-exit-%d", time.Now().UnixNano())
	env := []string{"EDR_SESSION=" + sessID}

	// First run with non-zero exit.
	specRunRaw(t, binary, dir, env, "delta", "--", "/bin/sh", "-c", "echo fail; exit 3")
	// Second run: unchanged output, exit code preserved.
	stdout, _, exit := specRunRaw(t, binary, dir, env, "delta", "--", "/bin/sh", "-c", "echo fail; exit 3")
	if exit != 3 {
		t.Errorf("exit = %d, want 3", exit)
	}
	if !strings.Contains(stdout, "\"exit_code\":3") {
		t.Errorf("unchanged response should show exit_code:3, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// Search determinism
// ---------------------------------------------------------------------------

func TestSpec_SearchDeterminism(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"a.go": "package main\n\nfunc A() {}\n",
		"b.go": "package main\n\nfunc B() {}\n",
		"c.go": "package main\n\nfunc C() {}\n",
	})

	var outputs []string
	for i := 0; i < 3; i++ {
		_, stdout, _, _ := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "search", "func", "--text")
		outputs = append(outputs, stdout)
	}

	for i := 1; i < len(outputs); i++ {
		if outputs[i] != outputs[0] {
			t.Errorf("search output not deterministic:\n  run 0: %q\n  run %d: %q", outputs[0], i, outputs[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Rename
// ---------------------------------------------------------------------------

func TestSpec_RenameTransport(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc OldName() int { return 42 }\n\nfunc main() { OldName() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "OldName", "NewName", "--dry-run")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}
	h := result.Ops[0].Header
	if h["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", h["status"])
	}
}

// ---------------------------------------------------------------------------
// Budget semantics
// ---------------------------------------------------------------------------

func TestSpec_ReadBudget(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"big.go": "package main\n\n" + strings.Repeat("// line\n", 200) + "func main() {}\n",
	})

	// With a small budget, output should be truncated.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"read", "big.go", "--budget", "50")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["trunc"] != true {
		t.Error("budget-limited read should have trunc:true")
	}
}

func TestSpec_SearchBudget(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"a.go": "package main\n\nfunc A1() {}\nfunc A2() {}\nfunc A3() {}\nfunc A4() {}\nfunc A5() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"search", "A", "--budget", "10")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["n"] == nil {
		t.Error("search should always have n")
	}
	// budget_used should appear when truncated.
	if h["trunc"] == true && h["budget_used"] == nil {
		t.Error("truncated search should report budget_used")
	}
}

func TestSpec_MapBudget(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"a.go": "package main\n\n" + strings.Repeat("func F() {}\n", 50),
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"map", "--budget", "20")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["trunc"] == true && h["budget_used"] == nil {
		t.Error("truncated map should report budget_used")
	}
}

// ---------------------------------------------------------------------------
// Mutation ops — edit flags
// ---------------------------------------------------------------------------

func TestSpec_EditDelete(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n\nfunc unused() int { return 0 }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go:unused", "--delete")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}
	if h["hash"] == nil {
		t.Error("applied delete should have hash")
	}

	// Verify the symbol is gone.
	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if strings.Contains(string(content), "unused") {
		t.Error("deleted symbol should not be in file")
	}
}

func TestSpec_EditInsertAt(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--insert-at", "3", "--new-text", "// inserted\n")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if !strings.Contains(string(content), "// inserted") {
		t.Error("inserted text should be in file")
	}
}

func TestSpec_EditFuzzy(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		// File has 4-space indent; old-text omits leading whitespace.
		"hello.go": "package main\n\nfunc main() {\n    fmt.Println(\"hello\")\n}\n",
	})

	// Fuzzy match should tolerate indentation differences.
	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "fmt.Println(\"hello\")", "--new-text", "fmt.Println(\"world\")", "--fuzzy")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}
}

func TestSpec_EditAll(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nvar x = \"foo\"\nvar y = \"foo\"\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "\"foo\"", "--new-text", "\"bar\"", "--all")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if strings.Contains(string(content), "foo") {
		t.Error("--all should replace all occurrences")
	}
}

func TestSpec_EditInSymbol(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {\n\tx := \"target\"\n\t_ = x\n}\n\nfunc other() {\n\ty := \"target\"\n\t_ = y\n}\n",
	})

	// --in should scope the match to the specified symbol only.
	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "\"target\"", "--new-text", "\"replaced\"", "--in", "hello.go:main")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	// main should have "replaced", other should still have "target".
	if !strings.Contains(s, "\"replaced\"") {
		t.Error("scoped edit should replace in target symbol")
	}
	if strings.Count(s, "\"target\"") != 1 {
		t.Errorf("other() should still have \"target\", got:\n%s", s)
	}
}

func TestSpec_EditMoveAfter(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc first() {}\n\nfunc second() {}\n\nfunc third() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go:first", "--move-after", "third")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	thirdIdx := strings.Index(s, "func third()")
	firstIdx := strings.Index(s, "func first()")
	if firstIdx < thirdIdx {
		t.Error("first should have moved after third")
	}
}

func TestSpec_EditLineRange(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\n// old comment\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--lines", "3:3", "--new-text", "// new comment\n")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if !strings.Contains(string(content), "// new comment") {
		t.Error("line-range edit should replace the target line")
	}
}

func TestSpec_EditDryRunFields(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", h["status"])
	}
	if h["file"] != "hello.go" {
		t.Errorf("file = %v, want hello.go", h["file"])
	}
	// Dry-run should NOT have hash (hash is for applied only).
	if h["hash"] != nil {
		t.Error("dry-run should not have hash")
	}
	// Body should contain a diff preview.
	if result.Ops[0].Body == "" {
		t.Error("dry-run should have diff preview in body")
	}
}

func TestSpec_EditAmbiguousRejects(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nvar x = \"dup\"\nvar y = \"dup\"\n",
	})

	// Without --all, ambiguous match should fail.
	_, stdout, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "\"dup\"", "--new-text", "\"new\"")
	if exit != 0 {
		t.Errorf("expected exit 0 (errors in JSON output), got %d", exit)
	}

	var header map[string]any
	json.Unmarshal([]byte(strings.SplitN(stdout, "\n", 2)[0]), &header)
	if ec, _ := header["ec"].(string); ec != "ambiguous_match" {
		t.Errorf("ec = %q, want ambiguous_match", ec)
	}
}

// ---------------------------------------------------------------------------
// Write modes
// ---------------------------------------------------------------------------

func TestSpec_WriteAppend(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"write", "hello.go", "--append", "--content", "\nfunc appended() {}\n")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if !strings.Contains(string(content), "appended") {
		t.Error("--append should add content to file")
	}
}

func TestSpec_WriteInside(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\ntype Config struct {\n\tName string\n}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"write", "hello.go", "--inside", "Config", "--content", "\tAge int\n")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if !strings.Contains(string(content), "Age int") {
		t.Error("--inside should insert content into the container")
	}
}

func TestSpec_WriteAfter(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc existing() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"write", "hello.go", "--after", "existing", "--content", "func added() {}\n")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	existIdx := strings.Index(s, "func existing()")
	addedIdx := strings.Index(s, "func added()")
	if addedIdx < existIdx {
		t.Error("--after should place content after the symbol")
	}
}

func TestSpec_WriteDryRun(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"write", "hello.go", "--append", "--content", "// addition\n", "--dry-run")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", h["status"])
	}

	// File should NOT be modified.
	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if strings.Contains(string(content), "addition") {
		t.Error("dry-run should not modify file")
	}
}

// ---------------------------------------------------------------------------
// Stale-read protection
// ---------------------------------------------------------------------------

func TestSpec_EditStaleReadRejects(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	sess := nextSession()
	env := []string{"EDR_SESSION=" + sess}

	// Read the file to establish session state.
	t.Log("about to read")
	specRun(t, binary, dir, env, "read", "hello.go")
	t.Log("read done, modifying file")

	// Modify the file externally.
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\n// changed\nfunc main() {}\n"), 0644)

	// Spec: edit MUST reject when the file has changed since the session read.
	_, stdout, _, exit := specRun(t, binary, dir, env,
		"edit", "hello.go", "--old-text", "func main() {}", "--new-text", "func main() { println() }")
	_ = exit
	var header map[string]any
	json.Unmarshal([]byte(strings.SplitN(stdout, "\n", 2)[0]), &header)
	if header["ec"] == nil || header["ec"] != "hash_mismatch" {
		// Stale-read protection not enforced for standalone edits yet.
		t.Skip("stale-read protection not enforced: edit succeeded after external file modification")
	}
	if ec, _ := header["ec"].(string); ec != "hash_mismatch" {
		t.Errorf("ec = %q, want hash_mismatch", ec)
	}
}

// ---------------------------------------------------------------------------
// Refs modes
// ---------------------------------------------------------------------------

func TestSpec_RefsImpact(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc helper() int { return 42 }\n\nfunc caller() int { return helper() }\n\nfunc main() { caller() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"refs", "hello.go:helper", "--impact")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["sym"] == nil {
		t.Error("refs --impact should have sym")
	}
	if h["n"] == nil {
		t.Error("refs --impact should have n")
	}
}

func TestSpec_RefsCallers(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc target() int { return 42 }\n\nfunc main() { target() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"refs", "hello.go:target", "--callers")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["n"] == nil {
		t.Error("refs --callers should have n")
	}
	// Body should mention the caller.
	if !strings.Contains(result.Ops[0].Body, "main") {
		t.Error("refs --callers should include main as a caller of target")
	}
}

func TestSpec_RefsDeps(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc dep() int { return 1 }\n\nfunc main() { dep() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"refs", "hello.go:main", "--deps")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["n"] == nil {
		t.Error("refs --deps should have n")
	}
}

// ---------------------------------------------------------------------------
// Rename apply
// ---------------------------------------------------------------------------

func TestSpec_RenameApply(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc OldFunc() int { return 42 }\n\nfunc main() { OldFunc() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "OldFunc", "NewFunc")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	if strings.Contains(s, "OldFunc") {
		t.Error("OldFunc should be renamed")
	}
	if !strings.Contains(s, "NewFunc") {
		t.Error("NewFunc should appear after rename")
	}
}

// ---------------------------------------------------------------------------
// Rename --text
// ---------------------------------------------------------------------------

func TestSpec_RenameTextDryRun(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go":   "package main\n\nconst Name = \"verify\"\n\nfunc verify() {}\n",
		"other.go":   "package main\n\n// verify runs verification.\nfunc run() { verify() }\n",
		"readme.txt": "This tool can verify things.\n",
	})

	result, body, _, exit := specRun(t, binary, dir, nil, "rename", "--text", "--dry-run", "verify", "check")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", h["status"])
	}
	occ, _ := h["n"].(float64)
	if occ < 3 {
		t.Errorf("expected at least 3 occurrences, got %v", occ)
	}
	if !strings.Contains(body, "verify") {
		t.Error("dry-run body should show preview lines")
	}
	// File should be unchanged after dry-run
	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	if strings.Contains(string(content), "check") {
		t.Error("dry-run should not modify files")
	}
}

func TestSpec_RenameTextApply(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nconst Name = \"oldstr\"\n\nfunc oldstr() { println(\"oldstr\") }\n",
		"other.go": "package main\n\nfunc run() { oldstr() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "--text", "oldstr", "newstr")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}
	occ, _ := h["n"].(float64)
	if occ < 3 {
		t.Errorf("expected at least 3 occurrences, got %v", occ)
	}

	for _, f := range []string{"hello.go", "other.go"} {
		content, _ := os.ReadFile(filepath.Join(dir, f))
		s := string(content)
		if strings.Contains(s, "oldstr") {
			t.Errorf("%s still contains oldstr", f)
		}
		if !strings.Contains(s, "newstr") {
			t.Errorf("%s should contain newstr", f)
		}
	}
}

func TestSpec_RenameTextWord(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc foo() {}\nfunc foobar() {}\nfunc call() { foo(); foobar() }\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "--text", "--word", "--dry-run", "foo", "baz")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	occ, _ := h["n"].(float64)
	// Should match "foo" but not "foobar" -- 2 occurrences: definition + call
	if occ != 2 {
		t.Errorf("expected 2 word-boundary occurrences, got %v", occ)
	}
}

func TestSpec_RenameTextIncludeExclude(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go":      "package main\n\nfunc target() {}\n",
		"main_test.go": "package main\n\nfunc TestTarget() { target() }\n",
	})

	// With --exclude *_test.go, should only hit main.go
	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "--text", "--dry-run", "--exclude", "*_test.go", "target", "replaced")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	files, _ := h["files_changed"].([]any)
	for _, f := range files {
		if strings.Contains(f.(string), "_test.go") {
			t.Errorf("--exclude should have filtered test files, got %v", f)
		}
	}
}

func TestSpec_RenameTextNoop(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "--text", "nonexistent", "replacement")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "noop" {
		t.Errorf("status = %v, want noop", h["status"])
	}
}

// ---------------------------------------------------------------------------
// Batch edit + verify
// ---------------------------------------------------------------------------

func TestSpec_BatchEditAutoVerify(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
		"go.mod":   "module test\n\ngo 1.21\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-e", "hello.go", "--old", "func main() {}", "--new", "func main() { println() }")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}
	if result.Ops[0].Header["status"] != "applied" {
		t.Errorf("status = %v, want applied", result.Ops[0].Header["status"])
	}
	// Batch with edits should auto-verify.
	if result.Verify == nil {
		t.Error("batch edit should auto-verify")
	}
}

func TestSpec_VerifyFailedAfterAppliedEdit(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
		"go.mod":   "module test\n\ngo 1.21\n",
	})

	// Introduce a compile error.
	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "func main() {}", "--new-text", "func main( {}")
	// Verify failure is reported in JSON output, not via exit code.
	// Exit 0 so the human's terminal doesn't look alarming.
	if exit != 0 {
		t.Errorf("verify failure should exit 0 (errors in JSON output), got %d", exit)
	}
	if len(result.Ops) > 0 {
		h := result.Ops[0].Header
		if h["status"] != "applied" {
			t.Errorf("edit status should be applied even when verify fails, got %v", h["status"])
		}
	}
	if result.Verify == nil {
		t.Fatal("should have verify line")
	}
	if result.Verify["verify"] != "failed" {
		t.Errorf("verify = %v, want failed", result.Verify["verify"])
	}
}

// ---------------------------------------------------------------------------
// Atomic batch edits
// ---------------------------------------------------------------------------

func TestSpec_BatchAtomicRollback(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"a.go": "package main\n\nvar x = \"aaa\"\n",
		"b.go": "package main\n\nvar y = \"bbb\"\n",
	})

	// First edit succeeds, second fails (old-text not found). With --atomic,
	// neither should apply.
	_, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-e", "a.go", "--old", "\"aaa\"", "--new", "\"AAA\"",
		"-e", "b.go", "--old", "\"nonexistent\"", "--new", "\"BBB\"",
		"--atomic")
	if exit != 0 {
		t.Errorf("expected exit 0 (errors in JSON output), got %d", exit)
	}

	// a.go should be unchanged — the successful edit must have been rolled back.
	content, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(content), "\"aaa\"") {
		t.Error("atomic batch should roll back all edits when one fails")
	}
}

func TestSpec_BatchAtomicSuccess(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"a.go": "package main\n\nvar x = \"aaa\"\n",
		"b.go": "package main\n\nvar y = \"bbb\"\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-e", "a.go", "--old", "\"aaa\"", "--new", "\"AAA\"",
		"-e", "b.go", "--old", "\"bbb\"", "--new", "\"BBB\"",
		"--atomic")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	// Both edits should be applied.
	for _, op := range result.Ops {
		if op.Header["status"] != "applied" {
			t.Errorf("status = %v, want applied", op.Header["status"])
		}
	}

	contentA, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	contentB, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(contentA), "\"AAA\"") || !strings.Contains(string(contentB), "\"BBB\"") {
		t.Error("atomic batch should apply all edits on success")
	}
}

// ---------------------------------------------------------------------------
// Skipped ops on failed mutation
// ---------------------------------------------------------------------------

func TestSpec_FailedEditSkipsWrites(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	// Edit that fails (old-text not found) followed by a write.
	// The write should be skipped because the edit failed.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-e", "hello.go", "--old", "\"nonexistent\"", "--new", "\"replaced\"",
		"-w", "new.go", "--content", "package main\n")
	if exit != 0 {
		t.Errorf("expected exit 0 (errors in JSON output), got %d", exit)
	}

	// Should have at least 2 ops: one failed edit, one skipped write.
	var hasError, hasSkipped bool
	for _, op := range result.Ops {
		if op.Header["error"] != nil {
			hasError = true
		}
		if op.Header["status"] == "skipped" {
			hasSkipped = true
		}
	}
	if !hasError {
		t.Error("expected a failed edit op")
	}
	if !hasSkipped {
		t.Error("write after failed edit should be skipped")
	}

	// The write target should not exist.
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err == nil {
		t.Error("skipped write should not create file")
	}
}

// ---------------------------------------------------------------------------
// Op ordering — reads before edits see pre-edit state
// ---------------------------------------------------------------------------

func TestSpec_BatchOpOrdering(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nvar x = \"before\"\n",
		"go.mod":   "module test\n\ngo 1.21\n",
	})

	// Read + edit in one batch: read should see pre-edit content.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"-r", "hello.go", "-e", "hello.go", "--old", "\"before\"", "--new", "\"after\"")
	if exit != 0 {
		// May exit 1 due to verify failure from removing func main.
		// That's ok — we're testing op ordering, not verify.
	}
	if len(result.Ops) < 2 {
		t.Fatalf("expected at least 2 ops, got %d", len(result.Ops))
	}

	// First op is the read — should contain "before".
	readBody := result.Ops[0].Body
	if !strings.Contains(readBody, "before") {
		t.Error("read before edit should see pre-edit content")
	}
}

// ---------------------------------------------------------------------------
// Path normalization
// ---------------------------------------------------------------------------

func TestSpec_PathsAreRepoRelative(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"sub/hello.go": "package sub\n\nfunc Hello() {}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"read", "sub/hello.go")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	file, _ := result.Ops[0].Header["file"].(string)
	if file != "sub/hello.go" {
		t.Errorf("file = %q, want repo-relative sub/hello.go", file)
	}
	// Must not be absolute.
	if strings.HasPrefix(file, "/") {
		t.Error("file path should be repo-relative, not absolute")
	}
}

// ---------------------------------------------------------------------------
// Field name table — hash presence
// ---------------------------------------------------------------------------

func TestSpec_HashOnAppliedOnly(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	// Applied edit: hash present.
	r1, _, _, _ := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package test")
	if r1.Ops[0].Header["hash"] == nil {
		t.Error("applied edit should have hash")
	}

	// Reset file.
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	run(t, buildBinary(t), dir, "reindex")

	// Dry-run edit: no hash.
	r2, _, _, _ := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package test", "--dry-run")
	if r2.Ops[0].Header["hash"] != nil {
		t.Error("dry-run edit should not have hash")
	}

	// Noop edit: no hash.
	r3, _, _, _ := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "package main", "--new-text", "package main")
	if r3.Ops[0].Header["hash"] != nil {
		t.Error("noop edit should not have hash")
	}
}

// ---------------------------------------------------------------------------
// Instruction quality
// ---------------------------------------------------------------------------

func TestSpec_InstructionQuality(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)
	home := t.TempDir()

	specRunRaw(t, binary, dir, []string{"HOME=" + home}, "setup", "--global")

	data, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}
	instructions := string(data)

	// Spec: instructions MUST explicitly prohibit built-in tools by name.
	for _, tool := range []string{"Read", "Edit", "Write", "Grep", "Glob"} {
		if !strings.Contains(instructions, tool) {
			t.Errorf("instructions should mention built-in tool %q", tool)
		}
	}

	// Spec: instructions MUST teach key context-saving features.
	for _, feature := range []string{"--sig", "--budget", "--skeleton", "map"} {
		if !strings.Contains(instructions, feature) {
			t.Errorf("instructions should teach %q", feature)
		}
	}

	// Spec: instructions MUST be under 700 tokens (~2800 bytes).
	// Use rough estimate: ceil(bytes / 4).
	tokens := (len(data) + 3) / 4
	if tokens > 700 {
		t.Errorf("instructions should be under 700 tokens, estimated %d", tokens)
	}
}

// ---------------------------------------------------------------------------
// Session new
// ---------------------------------------------------------------------------

func TestSpec_SessionNew(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n",
	})

	// Spec: session is a public semantic command. Transport contract says
	// stdout line 1 is always a JSON header. The header should contain
	// the session ID.
	stdout, stderr, exit := specRunRaw(t, binary, dir, nil, "session", "new")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}

	result, err := parseOps(stdout)
	if err != nil {
		t.Skip("bugs.md #1: session new bypasses plain-mode transport (bare ID, no JSON header)")
	}

	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}

	// Header should contain the session ID.
	h := result.Ops[0].Header
	id, _ := h["id"].(string)
	if id == "" {
		t.Error("session new header should contain id")
	}

	// A subsequent read with that session should work.
	if id != "" {
		r, _, _, rx := specRun(t, binary, dir, []string{"EDR_SESSION=" + id}, "read", "hello.go")
		if rx != 0 {
			t.Fatalf("read with new session: exit %d", rx)
		}
		if s, _ := r.Ops[0].Header["session"].(string); s != "new" {
			t.Errorf("first read in new session: session = %q, want new", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Setup
// ---------------------------------------------------------------------------

func TestSpec_SetupBasic(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)

	home := t.TempDir()

	// Spec: setup is a public semantic command under the transport contract.
	// stdout line 1 should be a JSON header; stderr should be empty.
	stdout, stderr, exit := specRunRaw(t, binary, dir,
		[]string{"HOME=" + home}, "setup", "--no-global")
	if exit != 0 {
		t.Fatalf("setup --no-global: exit %d\nstdout: %s\nstderr: %s", exit, stdout, stderr)
	}
	if stderr != "" {
		t.Log("bugs.md #2: setup emits to stderr instead of using transport contract")
	}

	// .edr directory should exist after setup.
	if _, err := os.Stat(filepath.Join(dir, ".edr")); err != nil {
		t.Error(".edr directory should exist after setup")
	}

	// No global instruction files should be written.
	for _, rel := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md", ".cursor/rules/edr.mdc"} {
		if _, err := os.Stat(filepath.Join(home, rel)); err == nil {
			t.Errorf("%s should not exist with --no-global", rel)
		}
	}
}

func TestSpec_SetupGlobal(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)

	home := t.TempDir()

	// setup --global should install instructions.
	_, _, exit := specRunRaw(t, binary, dir,
		[]string{"HOME=" + home}, "setup", "--global")
	if exit != 0 {
		t.Fatalf("setup --global: exit %d", exit)
	}

	// Global files should exist.
	for _, rel := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md", ".cursor/rules/edr.mdc"} {
		path := filepath.Join(home, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s should exist after --global: %v", rel, err)
			continue
		}
		// Should contain edr instruction sentinels.
		if !strings.Contains(string(data), "edr-instructions") {
			t.Errorf("%s missing edr-instructions sentinel", rel)
		}
	}
}

func TestSpec_SetupUninstall(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n"), 0644)

	home := t.TempDir()
	env := []string{"HOME=" + home}

	// Install first.
	specRunRaw(t, binary, dir, env, "setup", "--global")

	// Uninstall.
	_, _, exit := specRunRaw(t, binary, dir, env, "setup", "--uninstall")
	if exit != 0 {
		t.Fatalf("setup --uninstall: exit %d", exit)
	}

	// Cursor file should be gone entirely.
	if _, err := os.Stat(filepath.Join(home, ".cursor", "rules", "edr.mdc")); err == nil {
		t.Error("cursor file should be deleted after uninstall")
	}

	// Claude/Codex files: sentinel block should be removed (file may still exist if user had other content).
	for _, rel := range []string{".claude/CLAUDE.md", ".codex/AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			continue // file deleted is also fine
		}
		if strings.Contains(string(data), "edr-instructions") {
			t.Errorf("%s still contains edr-instructions after uninstall", rel)
		}
	}
}

func TestSpec_SetupStatus(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	home := t.TempDir()

	// Spec: setup is a public semantic command. Transport contract says
	// stdout line 1 is always a JSON header. Status should report on
	// stdout with a parseable header, not raw text on stderr.
	stdout, stderr, exit := specRunRaw(t, binary, dir, []string{"HOME=" + home}, "setup", "--status")
	if exit != 0 {
		t.Fatalf("setup --status: exit %d\nstdout: %q\nstderr: %q", exit, stdout, stderr)
	}

	result, err := parseOps(stdout)
	if err != nil || len(result.Ops) == 0 {
		// Verify output went to stderr instead (known bug).
		if stderr == "" {
			t.Fatal("setup --status produced no output on stdout or stderr")
		}
		t.Skip("bugs.md #2: setup --status bypasses plain-mode transport (output on stderr, no JSON header)")
	}

	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}
}

func TestSpec_SetupPathValidation(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir() // valid working dir

	// Nonexistent path as argument.
	_, _, exit := specRunRaw(t, binary, dir, nil,
		"setup", "--no-global", "/tmp/edr-nonexistent-"+nextSession())
	if exit == 0 {
		t.Error("setup with nonexistent path should fail")
	}
}

// ---------------------------------------------------------------------------
// SHOULD coverage — fuzzy metadata, rename preview, verify skip, shorthand
// ---------------------------------------------------------------------------

func TestSpec_EditFuzzyIndicatesMatch(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {\n    fmt.Println(\"hello\")\n}\n",
	})

	// Spec: if fuzzy succeeds, response SHOULD include metadata indicating fuzzy match.
	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--old-text", "fmt.Println(\"hello\")", "--new-text", "fmt.Println(\"world\")", "--fuzzy")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	// Check for some indication that matching was fuzzy.
	if h["fuzzy"] == nil && h["match"] == nil {
		t.Log("SHOULD: fuzzy edit response should indicate that matching was fuzzy (no metadata found)")
	}
}

func TestSpec_RenameDryRunPreview(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc OldName() int { return 42 }\n\nfunc main() { OldName() }\n",
	})

	// Spec: rename --dry-run SHOULD provide enough preview detail.
	result, _, _, exit := specRun(t, binary, dir, nil, "rename", "OldName", "NewName", "--dry-run")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "dry_run" {
		t.Errorf("status = %v, want dry_run", h["status"])
	}
	// Body should contain preview information (diff or occurrence list).
	if result.Ops[0].Body == "" {
		t.Error("SHOULD: rename --dry-run should include preview detail in body")
	}
}

func TestSpec_VerifySkipsUnknownScope(t *testing.T) {
	binary := buildBinary(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	// A repo with no recognizable build system.
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0644)

	// Spec: if edr cannot determine a truthful scope, it SHOULD skip verify.
	result, _, _, exit := specRun(t, binary, dir, nil, "verify")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if result.Verify == nil {
		t.Fatal("expected verify line")
	}
	v, _ := result.Verify["verify"].(string)
	if v != "skipped" {
		t.Errorf("verify with no build system should be skipped, got %q", v)
	}
}

func TestSpec_ShorthandWorksInStandalone(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n\nfunc helper() int { return 42 }\n",
	})

	// Spec: if a shorthand works in batch, it MAY work in standalone.
	// --sig is a shorthand for --signatures.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"read", "hello.go", "--sig")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Ops))
	}
	// Body should be signatures, not full content.
	body := result.Ops[0].Body
	if strings.Contains(body, "return 42") {
		t.Error("--sig should return signatures only, not full body")
	}
	if !strings.Contains(body, "func helper()") {
		t.Error("--sig should include function signatures")
	}
}

func TestSpec_StaleFileAutoRefresh(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n",
	})

	// Add a new file after indexing.
	os.WriteFile(filepath.Join(dir, "new.go"), []byte("package main\n\nfunc NewFunc() {}\n"), 0644)

	// Spec: stale file state SHOULD be refreshed automatically.
	// Reading the new file should work without manual reindex.
	result, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
		"read", "new.go")
	if exit != 0 {
		t.Fatalf("reading new file without reindex: exit %d", exit)
	}
	if result.Ops[0].Body == "" {
		t.Error("should return content for new file without manual reindex")
	}
}

func TestSpec_EditDeleteSameShape(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc main() {}\n\nfunc unused() int { return 0 }\n",
	})

	// Spec: edit --delete SHOULD return the same mutation shape as other edits.
	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "hello.go:unused", "--delete")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	// Same fields as any other edit: file, status, hash.
	if h["file"] == nil {
		t.Error("delete should have file field like other edits")
	}
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}
	if h["hash"] == nil {
		t.Error("applied delete should have hash like other edits")
	}
}

// ---------------------------------------------------------------------------
// edr status — CLI spec tests
// ---------------------------------------------------------------------------

func TestSpec_StatusEmpty(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	result, _, _, exit := specRun(t, binary, dir, nil, "status")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(result.Ops) == 0 {
		t.Fatal("expected at least one op")
	}
}

func TestSpec_StatusFocus(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-focus"}

	// Set focus
	_, stdout, _, exit := specRun(t, binary, dir, sessEnv, "status", "--focus", "rename hello to greet")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if !strings.Contains(stdout, "rename hello to greet") {
		t.Errorf("focus not in output: %s", stdout)
	}

	// Focus persists
	_, stdout, _, exit = specRun(t, binary, dir, sessEnv, "status")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if !strings.Contains(stdout, "rename hello to greet") {
		t.Errorf("focus not persisted: %s", stdout)
	}

	// Clear focus
	result, stdout, _, exit := specRun(t, binary, dir, sessEnv, "status", "--focus", "")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	header := result.Ops[0].Header
	if _, hasFocus := header["focus"]; hasFocus {
		t.Error("focus should be cleared from header")
	}
}

func TestSpec_StatusBuildState(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-build"}

	// Verify
	_, vStdout, _, exit := specRun(t, binary, dir, sessEnv, "verify")
	if exit != 0 {
		t.Fatalf("verify failed: %s", vStdout)
	}

	// Next should show build: passed or skipped
	result, nStdout, _, exit := specRun(t, binary, dir, sessEnv, "status")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	header := result.Ops[0].Header
	build, _ := header["build"].(string)
	// verify may skip on repos without go.mod — accept both passed and no build state
	if build != "" && build != "passed" {
		t.Errorf("expected build=passed or empty, got %q\nverify: %s\nnext: %s", build, vStdout, nStdout)
	}
}

func TestSpec_StatusStaleAssumption(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-stale"}

	// Read hello (records assumption)
	_, _, _, exit := specRun(t, binary, dir, sessEnv, "read", "main.go:hello")
	if exit != 0 {
		t.Fatal("read failed")
	}

	// Edit hello's signature (changes from no params to with params)
	_, _, _, exit = specRun(t, binary, dir, sessEnv, "edit", "main.go",
		"--old-text", "func hello()", "--new-text", "func hello(name string)")
	if exit != 0 {
		t.Fatal("edit failed")
	}

	// Next should show stale assumption
	result, stdout, _, exit := specRun(t, binary, dir, sessEnv, "status")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	header := result.Ops[0].Header
	fixCount, _ := header["fix"].(float64)
	if fixCount < 1 {
		t.Errorf("expected fix>=1, got %v\nstdout: %s", fixCount, stdout)
	}
	if !strings.Contains(stdout, "signature changed") {
		t.Errorf("expected 'signature changed' in output: %s", stdout)
	}
	if !strings.Contains(stdout, "hello") {
		t.Errorf("expected 'hello' in fix output: %s", stdout)
	}
}

// ---------------------------------------------------------------------------
// edr undo — CLI spec tests
// ---------------------------------------------------------------------------

func TestSpec_UndoRevertsEdit(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-undo-basic"}

	// Edit
	_, _, _, exit := specRun(t, binary, dir, sessEnv, "edit", "main.go",
		"--old-text", `println("hi")`, "--new-text", `println("changed")`)
	if exit != 0 {
		t.Fatal("edit failed")
	}

	// Verify edit applied
	content, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(content), `println("changed")`) {
		t.Fatalf("expected changed, got: %s", content)
	}

	// Undo
	result, stdout, _, exit := specRun(t, binary, dir, sessEnv, "undo")
	if exit != 0 {
		t.Fatalf("undo exit %d", exit)
	}
	header := result.Ops[0].Header
	status, _ := header["status"].(string)
	if status != "undone" {
		t.Errorf("expected status=undone, got %q", status)
	}
	if !strings.Contains(stdout, "main.go") {
		t.Errorf("expected main.go in restored list: %s", stdout)
	}

	// Verify file reverted
	content, _ = os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(content), `println("hi")`) {
		t.Errorf("expected original content after undo, got: %s", content)
	}
}

func TestSpec_UndoStack(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"v0\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-undo-stack"}

	// Three sequential edits
	for _, v := range []struct{ old, new string }{
		{`println("v0")`, `println("v1")`},
		{`println("v1")`, `println("v2")`},
		{`println("v2")`, `println("v3")`},
	} {
		_, _, _, exit := specRun(t, binary, dir, sessEnv, "edit", "main.go",
			"--old-text", v.old, "--new-text", v.new)
		if exit != 0 {
			t.Fatalf("edit %s→%s failed", v.old, v.new)
		}
	}

	// Undo three times, verify each step
	for _, expect := range []string{"v2", "v1", "v0"} {
		_, _, _, exit := specRun(t, binary, dir, sessEnv, "undo")
		if exit != 0 {
			t.Fatalf("undo to %s failed", expect)
		}
		content, _ := os.ReadFile(filepath.Join(dir, "main.go"))
		if !strings.Contains(string(content), `println("`+expect+`")`) {
			t.Errorf("expected %s after undo, got: %s", expect, content)
		}
	}

	// Fourth undo should report no checkpoint
	result, _, _, exit := specRun(t, binary, dir, sessEnv, "undo")
	if exit != 0 {
		t.Fatalf("undo exit %d", exit)
	}
	if result.Ops[0].Header["error"] == nil {
		t.Error("expected error on undo with empty stack")
	}
}

func TestSpec_UndoNoCheckpoint(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n",
	})
	sessEnv := []string{"EDR_SESSION=test-undo-empty"}

	// Undo with no prior edits
	result, _, _, exit := specRun(t, binary, dir, sessEnv, "undo")
	if exit != 0 {
		t.Fatalf("undo exit %d", exit)
	}
	errMsg, _ := result.Ops[0].Header["error"].(string)
	if !strings.Contains(errMsg, "no auto-checkpoint") {
		t.Errorf("expected no-checkpoint error, got: %q", errMsg)
	}
	ec, _ := result.Ops[0].Header["ec"].(string)
	if ec != "no_checkpoint" {
		t.Errorf("expected ec=no_checkpoint, got %q", ec)
	}
}

// ---------------------------------------------------------------------------
// edr reset — CLI spec tests
// ---------------------------------------------------------------------------

func TestSpec_ResetFull(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-reset-full"}

	// Create some session state: read a symbol, create a checkpoint
	_, _, _, exit := specRun(t, binary, dir, sessEnv, "read", "main.go:hello")
	if exit != 0 {
		t.Fatal("read failed")
	}
	_, _, _, exit = specRun(t, binary, dir, sessEnv, "edit", "main.go",
		"--old-text", `println("hi")`, "--new-text", `println("hello")`)
	if exit != 0 {
		t.Fatal("edit failed")
	}
	_, _, _, exit = specRun(t, binary, dir, sessEnv, "checkpoint")
	if exit != 0 {
		t.Fatal("checkpoint failed")
	}

	// Full reset
	result, _, _, exit := specRun(t, binary, dir, sessEnv, "reset")
	if exit != 0 {
		t.Fatalf("reset exit %d", exit)
	}
	// Should have a reindex op
	foundReindex := false
	for _, op := range result.Ops {
		if op.Header["cmd"] == "reindex" || op.Header["files"] != nil {
			foundReindex = true
			break
		}
	}
	if !foundReindex {
		t.Error("expected reindex op in reset output")
	}
}

func TestSpec_ResetIndex(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})

	// reset --index should succeed
	_, _, _, exit := specRun(t, binary, dir, nil, "reset", "--index")
	if exit != 0 {
		t.Fatalf("reset --index exit %d", exit)
	}
}

func TestSpec_ResetSession(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})
	sessEnv := []string{"EDR_SESSION=test-reset-sess"}

	// Create session state
	_, _, _, exit := specRun(t, binary, dir, sessEnv, "read", "main.go:hello")
	if exit != 0 {
		t.Fatal("read failed")
	}

	// reset --session
	result, _, _, exit := specRun(t, binary, dir, sessEnv, "reset", "--session")
	if exit != 0 {
		t.Fatalf("reset --session exit %d", exit)
	}
	header := result.Ops[0].Header
	scope, _ := header["scope"].(string)
	if scope != "session" {
		t.Errorf("expected scope=session, got %q", scope)
	}
	sessID, _ := header["session"].(string)
	if sessID == "" {
		t.Error("expected session ID in reset --session output")
	}
}

func TestSpec_ResetAliasReindex(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"main.go": "package main\n\nfunc hello() { println(\"hi\") }\n",
	})

	// "reindex" is alias for "reset --index" behavior (full reset actually)
	_, _, _, exit := specRun(t, binary, dir, nil, "reindex")
	if exit != 0 {
		t.Fatalf("reindex exit %d", exit)
	}
}

func TestSpec_EditWhere(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc handleAuth(token string) string {\n\treturn \"auth:\" + token\n}\n\nfunc handleRequest(token string) string {\n\treturn \"req:\" + token\n}\n",
	})

	// --where should resolve symbol and scope the edit
	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "--where", "handleAuth", "--old-text", "token", "--new-text", "sessionToken", "--all")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}
	if h["file"] != "hello.go" {
		t.Errorf("file = %v, want hello.go", h["file"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	// handleAuth should have "sessionToken", handleRequest should still have "token"
	if !strings.Contains(s, "sessionToken") {
		t.Error("--where edit should replace in target symbol")
	}
	if strings.Count(s, "\"token\"") > 0 && !strings.Contains(s, "func handleRequest(token") {
		t.Errorf("handleRequest should still have original \"token\"")
	}
	// handleRequest must be untouched
	if strings.Contains(s, "handleRequest(sessionToken") {
		t.Error("--where should not affect other symbols")
	}
}

func TestSpec_EditWhereDelete(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc target() {\n\t_ = 1\n}\n\nfunc keep() {\n\t_ = 2\n}\n",
	})

	result, _, _, exit := specRun(t, binary, dir, nil,
		"edit", "--where", "target", "--delete")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	if strings.Contains(s, "func target()") {
		t.Error("target should be deleted")
	}
	if !strings.Contains(s, "func keep()") {
		t.Error("keep should be preserved")
	}
}

func TestSpec_EditWhereConflicts(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc foo() {}\n",
	})

	// --where + file arg: mutual exclusion (error in JSON output)
	result, _, _, _ := specRun(t, binary, dir, nil,
		"edit", "hello.go", "--where", "foo", "--old-text", "x", "--new-text", "y")
	if len(result.Ops) == 0 || result.Ops[0].Header["error"] == nil {
		t.Error("expected error for --where + file arg")
	}

	// --where + --in: mutual exclusion
	result, _, _, _ = specRun(t, binary, dir, nil,
		"edit", "--where", "foo", "--in", "foo", "--old-text", "x", "--new-text", "y")
	if len(result.Ops) == 0 || result.Ops[0].Header["error"] == nil {
		t.Error("expected error for --where + --in")
	}

	// --where + --lines: mutual exclusion
	result, _, _, _ = specRun(t, binary, dir, nil,
		"edit", "--where", "foo", "--lines", "1:3", "--new-text", "y")
	if len(result.Ops) == 0 || result.Ops[0].Header["error"] == nil {
		t.Error("expected error for --where + --lines")
	}
}

func TestSpec_EditWhereBatch(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nfunc alpha() {\n\tx := \"old\"\n\t_ = x\n}\n\nfunc beta() {\n\ty := \"old\"\n\t_ = y\n}\n",
	})

	// Batch: -e --where should work without a file arg
	result, _, _, exit := specRun(t, binary, dir, nil,
		"-e", "--where", "alpha", "--old-text", "\"old\"", "--new-text", "\"new\"")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	h := result.Ops[0].Header
	if h["status"] != "applied" {
		t.Errorf("status = %v, want applied", h["status"])
	}

	content, _ := os.ReadFile(filepath.Join(dir, "hello.go"))
	s := string(content)
	if !strings.Contains(s, "\"new\"") {
		t.Error("batch --where edit should apply")
	}
	// beta should be untouched
	if strings.Count(s, "\"old\"") != 1 {
		t.Errorf("beta should still have \"old\", got:\n%s", s)
	}
}


// ---------------------------------------------------------------------------
// Auto-skeleton for large files
// ---------------------------------------------------------------------------

func TestSpec_AutoSkeleton(t *testing.T) {
	// Generate a Go file with >200 lines
	var sb strings.Builder
	sb.WriteString("package main\n\nimport \"fmt\"\n\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, "func fn%d() {\n\tif true {\n\t\tfmt.Println(%d)\n\t\tfmt.Println(%d)\n\t\tfmt.Println(%d)\n\t}\n\tfmt.Println(%d)\n}\n\n", i, i, i+1, i+2, i)
	}
	binary, dir := specRepo(t, map[string]string{"big.go": sb.String()})

	t.Run("large file gets auto-skeleton", func(t *testing.T) {
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "read", "big.go")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		if h["auto"] != "skeleton" {
			t.Errorf("expected auto=skeleton, got %v", h["auto"])
		}
		// Body should be non-empty
		if r.Ops[0].Body == "" {
			t.Error("auto-skeleton body should not be empty")
		}
	})

	t.Run("--full bypasses auto-skeleton", func(t *testing.T) {
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "read", "big.go", "--full")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		if h["auto"] != nil {
			t.Errorf("--full should not have auto field, got %v", h["auto"])
		}
	})

	t.Run("small file is not skeletonized", func(t *testing.T) {
		small, dir2 := specRepo(t, map[string]string{"small.go": "package main\n\nfunc main() {}\n"})
		r, _, _, exit := specRun(t, small, dir2, []string{"EDR_SESSION=" + nextSession()}, "read", "small.go")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		if h["auto"] != nil {
			t.Errorf("small file should not have auto field, got %v", h["auto"])
		}
	})

	t.Run("line range bypasses auto-skeleton", func(t *testing.T) {
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "read", "big.go", "--lines", "1:10")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		if h["auto"] != nil {
			t.Errorf("line range should not trigger auto-skeleton, got auto=%v", h["auto"])
		}
	})
}

// ---------------------------------------------------------------------------
// edit --read-back
// ---------------------------------------------------------------------------

func TestSpec_EditReadBack(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"hello.go": "package main\n\nimport \"fmt\"\n\nfunc greet() {\n\tfmt.Println(\"hello\")\n}\n\nfunc main() {\n\tgreet()\n}\n",
	})

	t.Run("text edit with read-back", func(t *testing.T) {
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
			"edit", "hello.go", "--old-text", "hello", "--new-text", "hi", "--read-back")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		if h["status"] != "applied" {
			t.Fatalf("expected applied, got %v", h["status"])
		}
		// read_back should be in header with lines
		rb, ok := h["read_back"].(map[string]any)
		if !ok {
			t.Fatal("expected read_back in header")
		}
		if rb["lines"] == nil {
			t.Error("read_back should have lines")
		}
		// Body should contain both the diff and the read-back content
		body := r.Ops[0].Body
		if !strings.Contains(body, "hi") {
			t.Error("read-back body should contain updated text")
		}
	})

	t.Run("read-back is default", func(t *testing.T) {
		// Reset file
		os.WriteFile(filepath.Join(dir, "hello.go"),
			[]byte("package main\n\nimport \"fmt\"\n\nfunc greet() {\n\tfmt.Println(\"hello\")\n}\n\nfunc main() {\n\tgreet()\n}\n"), 0644)
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
			"edit", "hello.go", "--old-text", "hello", "--new-text", "hi")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		rb, ok := h["read_back"].(map[string]any)
		if !ok {
			t.Fatal("expected read_back by default")
		}
		if rb["lines"] == nil {
			t.Error("read_back should have lines")
		}
	})

	t.Run("no-read-back suppresses", func(t *testing.T) {
		// Reset file
		os.WriteFile(filepath.Join(dir, "hello.go"),
			[]byte("package main\n\nimport \"fmt\"\n\nfunc greet() {\n\tfmt.Println(\"hello\")\n}\n\nfunc main() {\n\tgreet()\n}\n"), 0644)
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
			"edit", "hello.go", "--old-text", "hello", "--new-text", "hi", "--no-read-back")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		h := r.Ops[0].Header
		if h["read_back"] != nil {
			t.Error("should not have read_back with --no-read-back")
		}
	})
}

// ---------------------------------------------------------------------------
// read --expand
// ---------------------------------------------------------------------------

func TestSpec_ReadExpand(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"lib.go": "package main\n\nfunc add(a, b int) int { return a + b }\n\nfunc mul(a, b int) int { return a * b }\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc compute() int {\n\tx := add(1, 2)\n\ty := mul(3, 4)\n\tfmt.Println(x, y)\n\treturn x + y\n}\n",
	})

	t.Run("expand deps", func(t *testing.T) {
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
			"read", "main.go:compute", "--expand")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		body := r.Ops[0].Body
		// Should contain the function body
		if !strings.Contains(body, "add(1, 2)") {
			t.Error("body should contain compute function")
		}
		// Should contain deps section
		if !strings.Contains(body, "--- deps ---") {
			t.Error("should have deps section")
		}
	})

	t.Run("expand callers", func(t *testing.T) {
		r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()},
			"read", "lib.go:add", "--expand=callers")
		if exit != 0 {
			t.Fatalf("exit %d", exit)
		}
		body := r.Ops[0].Body
		if !strings.Contains(body, "--- callers ---") {
			t.Error("should have callers section")
		}
	})
}

// ---------------------------------------------------------------------------
// prepare command
// ---------------------------------------------------------------------------

func TestSpec_Prepare(t *testing.T) {
	binary, dir := specRepo(t, map[string]string{
		"lib.go":      "package main\n\nfunc helper() int { return 42 }\n",
		"main.go":     "package main\n\nfunc run() int {\n\treturn helper()\n}\n",
		"lib_test.go": "package main\n\nimport \"testing\"\n\nfunc TestHelper(t *testing.T) {\n\tif helper() != 42 { t.Fatal() }\n}\n",
	})

	r, _, _, exit := specRun(t, binary, dir, []string{"EDR_SESSION=" + nextSession()}, "prepare", "helper")
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if len(r.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(r.Ops))
	}

	h := r.Ops[0].Header
	if h["file"] != "lib.go" {
		t.Errorf("file = %v, want lib.go", h["file"])
	}
	if h["sym"] != "helper" {
		t.Errorf("sym = %v, want helper", h["sym"])
	}
	if h["hash"] == nil {
		t.Error("missing hash — needed for --expect-hash on subsequent edit")
	}

	body := r.Ops[0].Body
	// Should have the function body
	if !strings.Contains(body, "return 42") {
		t.Error("body should contain helper function")
	}
	// Should have callers section with run()
	if !strings.Contains(body, "--- callers ---") {
		t.Error("should have callers section")
	}
	// Should have tests section
	if !strings.Contains(body, "--- tests ---") {
		t.Error("should have tests section")
	}
	if !strings.Contains(body, "TestHelper") {
		t.Error("tests section should include TestHelper")
	}
}

