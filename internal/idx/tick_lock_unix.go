//go:build !windows

package idx

import (
	"os"
	"syscall"
)

// tryLockFile acquires an exclusive non-blocking flock on f. Returns
// nil on success; an error (typically syscall.EWOULDBLOCK) when
// another process holds the lock.
func tryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the flock on f. Best-effort: the kernel also
// releases it when the fd is closed, so a failure here (e.g. already
// unlocked) is not actionable.
func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
