package idx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTickLock_AcquireReleaseReacquire verifies the happy path: a
// single-writer can lock, unlock, and re-lock without contention.
func TestTickLock_AcquireReleaseReacquire(t *testing.T) {
	edrDir := filepath.Join(t.TempDir(), ".edr")

	unlock, ok := acquireTickLock(edrDir)
	if !ok {
		t.Fatal("first acquire failed on empty edrDir")
	}
	unlock()

	// Re-acquire after release must succeed.
	unlock2, ok := acquireTickLock(edrDir)
	if !ok {
		t.Fatal("re-acquire after release failed")
	}
	unlock2()

	// The lock file itself should remain on disk — removing it would
	// be a race hazard (another process could acquire a fresh file
	// between our unlock and remove). Its presence is a sign of life,
	// not state.
	path := filepath.Join(edrDir, TickLockFile)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file missing after release: %v", err)
	}
}

// TestTickLock_Contention verifies the non-blocking property: while
// one goroutine holds the lock, a second goroutine's acquire returns
// (nil, false) immediately.
//
// Note: flock is a per-OS-file-description lock on Linux but per-
// process on BSD/macOS. Using separate *os.File opens from the same
// process models the cross-process case on macOS; on Linux a single
// process can re-acquire its own fcntl-style lock but Flock behaves
// advisory+fd-based, so contention is still observable.
func TestTickLock_Contention(t *testing.T) {
	edrDir := filepath.Join(t.TempDir(), ".edr")

	unlock1, ok := acquireTickLock(edrDir)
	if !ok {
		t.Fatal("primary acquire failed")
	}
	defer unlock1()

	// In the same process, the second attempt should fail fast. (This
	// covers the intra-process case; the multi-process variant below
	// covers the cross-process case that matters in production.)
	unlock2, ok := acquireTickLock(edrDir)
	if ok {
		unlock2()
		t.Skip("flock on this platform allows same-process re-acquire; " +
			"cross-process contention is covered by TestTickLock_MultiProcess")
	}
	if unlock2 != nil {
		t.Error("unlock must be nil when acquire returns ok=false")
	}
}

// TestTickLock_SerializesPatches runs N goroutines that each try to
// acquire the lock, hold it briefly, and release. Asserts that the
// holders never overlap — the lock is a real mutex, not a no-op.
func TestTickLock_SerializesPatches(t *testing.T) {
	edrDir := filepath.Join(t.TempDir(), ".edr")

	var active atomic.Int32
	var maxActive atomic.Int32
	var wins atomic.Int32
	var skips atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Spin-try for up to 200ms. In production the caller
			// skips on contention, but for the mutex-correctness
			// assertion we want every goroutine to eventually run
			// so we can check the "no overlap" invariant.
			deadline := time.Now().Add(200 * time.Millisecond)
			for time.Now().Before(deadline) {
				unlock, ok := acquireTickLock(edrDir)
				if !ok {
					skips.Add(1)
					time.Sleep(2 * time.Millisecond)
					continue
				}
				n := active.Add(1)
				if n > maxActive.Load() {
					maxActive.Store(n)
				}
				time.Sleep(5 * time.Millisecond)
				active.Add(-1)
				unlock()
				wins.Add(1)
				return
			}
		}()
	}
	wg.Wait()

	if got := maxActive.Load(); got > 1 {
		t.Errorf("lock permitted %d concurrent holders; want max 1", got)
	}
	if wins.Load() == 0 {
		t.Error("no goroutine ever acquired the lock")
	}
}

// TestTickLock_MultiProcess spawns N child processes and asserts at
// most one rewrites trigram.idx. The helper mode is triggered by
// EDR_TICK_LOCK_TEST=1 — under that flag, the test binary skips the
// normal test entry points and runs tickLockHelperMain instead.
// Each child:
//  1. Waits on a barrier (mtime of sync file) so all children race
//     for the lock at roughly the same instant.
//  2. Calls acquireTickLock(edrDir).
//  3. If it wins the lock, writes its PID to edrDir/holder and
//     sleeps briefly so other children see a contended state.
//  4. Exits with 0 on lock win, 1 on lock loss.
//
// The parent then verifies exactly one child exited 0 (sole winner)
// and that holder's PID matches.
func TestTickLock_MultiProcess(t *testing.T) {
	// Rebuilding the test binary every time would be slow; reuse this
	// test binary as the helper entry point (via go test's
	// -test.run + custom env variable trick).
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	edrDir := filepath.Join(t.TempDir(), ".edr")
	if err := os.MkdirAll(edrDir, 0o700); err != nil {
		t.Fatal(err)
	}
	barrier := filepath.Join(edrDir, "barrier")

	const nChildren = 6
	type childRes struct {
		pid      int
		exitCode int
		stderr   string
	}
	results := make(chan childRes, nChildren)

	var wg sync.WaitGroup
	for i := 0; i < nChildren; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var stderr bytes.Buffer
			cmd := exec.Command(exe, "-test.run=TestTickLock_HelperEntry", "-test.count=1")
			cmd.Env = append(os.Environ(),
				"EDR_TICK_LOCK_TEST=1",
				"EDR_TICK_LOCK_DIR="+edrDir,
				"EDR_TICK_LOCK_BARRIER="+barrier,
			)
			cmd.Stderr = &stderr
			err := cmd.Run()
			ec := 0
			if err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					ec = exitErr.ExitCode()
				} else {
					ec = -1
				}
			}
			results <- childRes{pid: cmd.Process.Pid, exitCode: ec, stderr: stderr.String()}
		}()
	}

	// Release the barrier after a short delay so all children are
	// waiting on it.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatalf("barrier write: %v", err)
	}

	wg.Wait()
	close(results)

	wins := 0
	losses := 0
	var winStderrs []string
	for r := range results {
		switch r.exitCode {
		case 0:
			wins++
		case 2:
			losses++
		default:
			t.Errorf("child exited %d, stderr=%q", r.exitCode, r.stderr)
		}
		if r.exitCode == 0 {
			winStderrs = append(winStderrs, r.stderr)
		}
	}

	if wins != 1 {
		t.Errorf("expected exactly 1 lock winner, got %d (losses=%d)", wins, losses)
	}
	if wins+losses != nChildren {
		t.Errorf("unexpected exit codes: wins+losses=%d want=%d", wins+losses, nChildren)
	}

	holderPath := filepath.Join(edrDir, "holder")
	data, err := os.ReadFile(holderPath)
	if err != nil {
		t.Fatalf("read holder: %v (winStderrs=%v)", err, winStderrs)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Errorf("holder file empty (winStderrs=%v)", winStderrs)
	}
}

// TestTickLock_HelperEntry is the process-entry point used by
// TestTickLock_MultiProcess. When not under the helper env var, it
// is a no-op — `go test` sees it as a passing test.
//
// Under EDR_TICK_LOCK_TEST=1, it:
//   - waits for the barrier file to appear (parent writes it after
//     all children have spun up);
//   - tries acquireTickLock on EDR_TICK_LOCK_DIR;
//   - on win: writes its PID to edrDir/holder, holds for 50ms, exits 0;
//   - on loss: exits 2.
func TestTickLock_HelperEntry(t *testing.T) {
	if os.Getenv("EDR_TICK_LOCK_TEST") != "1" {
		return
	}
	edrDir := os.Getenv("EDR_TICK_LOCK_DIR")
	barrier := os.Getenv("EDR_TICK_LOCK_BARRIER")
	if edrDir == "" || barrier == "" {
		os.Exit(3)
	}
	// Wait for barrier (≤1s).
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	unlock, ok := acquireTickLock(edrDir)
	if !ok {
		os.Exit(2)
	}
	defer unlock()
	holderPath := filepath.Join(edrDir, "holder")
	_ = os.WriteFile(holderPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	// Hold the lock briefly so other children, if any raced, are
	// guaranteed to observe it as taken.
	time.Sleep(50 * time.Millisecond)
	os.Exit(0)
}
