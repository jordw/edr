package cmd_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPython_Wrapper exercises the embedded edr.py wrapper end-to-end against
// a throwaway repo. It runs a Python script that asserts the wrapper's
// invariants; any failure surfaces as a non-zero exit with context on stderr.
func TestPython_Wrapper(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}

	binary, dir := specRepo(t, map[string]string{
		"main.go": `package main

func Greet(name string) string {
	return "hi " + name
}

func Greet2(name string) string {
	return "hello " + name
}

func main() {
	_ = Greet("world")
	_ = Greet2("world")
}
`,
		"other/greet.go": `package other

func Greet(name string) string {
	return "yo " + name
}
`,
	})

	// Extract the embedded module.
	pyPath := strings.TrimSpace(runCapture(t, binary, dir, []string{"python-path"}))
	if pyPath == "" {
		t.Fatalf("edr python-path returned empty")
	}

	binDir := filepath.Dir(binary)
	script := pythonTestScript

	cmd := exec.Command("python3", "-c", script)
	cmd.Dir = dir
	cmd.Env = specEnv(
		"PATH="+binDir+":/usr/bin:/bin:/usr/local/bin",
		"EDR_PYTHON_PATH="+pyPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python test script failed: %v\n--- output ---\n%s", err, out)
	}
	if !strings.Contains(string(out), "ALL_OK") {
		t.Fatalf("script did not report ALL_OK:\n%s", out)
	}
}

func runCapture(t *testing.T, binary, dir string, args []string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = specEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("edr %v failed: %v", args, err)
	}
	return string(out)
}

const pythonTestScript = `
import os, sys, traceback
sys.path.insert(0, os.environ["EDR_PYTHON_PATH"])
import edr

def check(cond, msg):
    if not cond:
        raise AssertionError(msg)

# --- focus: direct path:symbol ---
s = edr.focus("main.go:Greet")
check(s.file == "main.go", f"file={s.file!r}")
check(s.name == "Greet", f"name={s.name!r}")
check(s.start_line > 0 and s.end_line >= s.start_line, f"lines={s.start_line}-{s.end_line}")
check(s.content and "return" in s.content, "content missing body")

# --- focus: sig=True populates .signature (not .content) ---
s = edr.focus("main.go:Greet", sig=True)
check(s.signature is not None, ".signature should be set when sig=True")
check("Greet" in s.signature, f"signature should contain name: {s.signature!r}")
check(s.content is None, f".content should be empty when sig=True, got {s.content!r}")

# --- focus: file-only read ---
s = edr.focus("main.go")
check(s.file == "main.go", f"file={s.file!r}")
check(s.content and "package main" in s.content, "file content missing")

# --- focus: nonexistent file → EdrError with code ---
try:
    edr.focus("missing.go")
    raise AssertionError("expected EdrError for missing file")
except edr.EdrError as e:
    check(e.code in ("file_not_found", None), f"unexpected code: {e.code!r}")

# --- focus: nonexistent symbol → EdrError ---
try:
    edr.focus("main.go:NoSuchSym")
    raise AssertionError("expected EdrError for missing symbol")
except edr.EdrError as e:
    pass

# --- focus: ambiguous bare name → AmbiguousError with candidates ---
try:
    edr.focus("Greet")
    raise AssertionError("expected AmbiguousError for 'Greet'")
except edr.AmbiguousError as e:
    check(len(e.candidates) >= 2, f"expected >=2 candidates, got {len(e.candidates)}")
    files = {c.file for c in e.candidates}
    check("main.go" in files and "other/greet.go" in files, f"candidates={files}")
    for c in e.candidates:
        check(c.name == "Greet", f"candidate name={c.name!r}")
        check(c.start_line > 0, f"candidate line={c.start_line}")
        check(c.type, f"candidate type empty: {c!r}")

# --- orient: returns OrientResult with truncation metadata ---
r = edr.orient(grep="Greet")
check(isinstance(r, list), "OrientResult should behave like a list")
check(hasattr(r, "total") and hasattr(r, "truncated"), "missing truncation attrs")
check(r.total >= 2, f"total={r.total}")
names = {s.name for s in r}
check("Greet" in names, f"orient missed Greet: {names}")

# --- orient: budget=1 sets truncated=True ---
r = edr.orient(grep="Greet", budget=1)
check(r.total >= 2, f"total={r.total} (should reflect pre-budget count)")
check(r.truncated, "truncated should be True at budget=1")

# --- edit → undo roundtrip ---
res = edr.edit("main.go", old='"hi "', new='"howdy "')
check(res.status == "applied", f"edit status={res.status}")
s = edr.focus("main.go:Greet")
check('"howdy "' in (s.content or ""), "edit did not persist")
edr.undo()
s = edr.focus("main.go:Greet")
check('"hi "' in (s.content or ""), "undo did not revert")

# --- edit with --dry-run does not mutate ---
res = edr.edit("main.go", old='"hi "', new='"yo "', dry_run=True)
check(res.status in ("dry_run", "applied"), f"dry_run status={res.status}")
s = edr.focus("main.go:Greet")
check('"hi "' in (s.content or ""), f"dry-run should not mutate: {s.content!r}")

# --- files() fulltext search ---
hits = edr.files("Greet2")
check("main.go" in hits, f"files() missed main.go: {hits}")

# --- files(regex=True) ---
hits = edr.files(r"Greet\d", regex=True)
check("main.go" in hits, f"files(regex=True) missed main.go: {hits}")

# --- callers() parses body when header lacks structured callers ---
s = edr.focus("main.go:Greet")
cs = edr.callers(s)
check(len(cs) >= 1, f"callers() returned nothing: {cs}")
check(all(c.file for c in cs), f"callers() missing files: {cs}")
check(any(c.signature for c in cs), f"callers() missing signatures: {cs}")

# --- callees() returns what main() calls ---
m = edr.focus("main.go:main")
cs = edr.callees(m)
# main() calls Greet and Greet2; ref-graph may produce both or a superset.
names = {c.name for c in cs if c.name}
check("Greet" in names or "Greet2" in names, f"callees() missed Greet/Greet2: {names}")

# --- usages() returns files referencing a symbol's name, excluding its own file ---
g = edr.focus("main.go:Greet")
us = edr.usages(g)
check(isinstance(us, list), f"usages() should return list, got {type(us)}")
check("main.go" not in us, f"usages() should exclude defining file: {us}")

# --- changesig() dry-run carries diff ---
res = edr.changesig("main.go:Greet", add="x int", at=1, callarg="0", dry_run=True)
check(res.status == "dry_run", f"changesig status={res.status}")
check(res.diff and "---" in res.diff and "+++" in res.diff, f"changesig diff missing: {res.diff!r}")
check("x int" in res.diff, f"changesig diff lacks new param: {res.diff!r}")

# --- extract() dry-run carries diff ---
res = edr.extract("main.go:Greet", name="GreetInner", lines="4-4", dry_run=True)
check(res.diff, f"extract diff missing: {res!r}")

# --- status() returns a dict ---
st = edr.status()
check(isinstance(st, dict), f"status={type(st)}")

# --- transaction: rollback restores all files across multiple edits ---
with open("main.go") as f: pre = f.read()
try:
    with edr.transaction() as tx:
        edr.edit("main.go", old='"hi "', new='"X "')
        edr.edit("main.go", old='"hello "', new='"Y "')
        with open("main.go") as f:
            mid = f.read()
        check('"X "' in mid and '"Y "' in mid, "edits should apply inside txn")
        d = tx.diff
        check("--- a/main.go" in d and "+++ b/main.go" in d, f"txn diff missing: {d!r}")
        raise RuntimeError("force rollback")
except RuntimeError:
    pass
with open("main.go") as f: post = f.read()
check(post == pre, f"rollback failed to restore file")

# --- transaction: commit path persists ---
with edr.transaction() as tx:
    edr.edit("main.go", old='"hi "', new='"howdy "')
with open("main.go") as f: after = f.read()
check('"howdy "' in after, "commit should persist")

print("ALL_OK")
`
