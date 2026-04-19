package idx

import (
	"os"
	"path/filepath"
)

// TickLockFile is the filename of the per-edrDir advisory lock that
// serializes IncrementalTick's critical section. Exported so tests in
// the same repo can sanity-check its presence.
const TickLockFile = ".tick.lock"

// acquireTickLock attempts a non-blocking exclusive lock on
// edrDir/.tick.lock. Returns (unlock, true) when the caller owns the
// lock; returns (nil, false) when another process holds it. The
// caller is expected to skip the tick in the second case — next time
// through, the fast path will already be satisfied by the holder's
// stamped git mtime.
//
// The lock is an OS-level flock (see tick_lock_unix.go /
// tick_lock_windows.go), so if the holder crashes the kernel
// releases it on fd close. No stale-lock cleanup needed.
//
// edrDir is created if missing so the lock file has a home even on
// the first tick of a brand-new repo.
func acquireTickLock(edrDir string) (unlock func(), ok bool) {
	if err := os.MkdirAll(edrDir, 0o700); err != nil {
		return nil, false
	}
	path := filepath.Join(edrDir, TickLockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false
	}
	if err := tryLockFile(f); err != nil {
		f.Close()
		return nil, false
	}
	return func() {
		unlockFile(f)
		f.Close()
	}, true
}
