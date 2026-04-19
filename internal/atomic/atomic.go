// Package atomic writes files atomically via temp-file + rename.
//
// Two shapes are provided. WriteFile takes a fully buffered payload —
// the common case for small artifacts (session state, trigram index,
// popularity table). WriteVia takes a callback that streams into an
// io.Writer — used for payloads large enough that buffering the whole
// thing in memory is undesirable (e.g. scope.bin on large repos).
//
// Both create the parent directory (mode 0o700) if missing, write the
// temp file with mode 0o600, and rename into place. The temp file is
// created with os.CreateTemp so concurrent writers to the same final
// path cannot collide on a shared ".tmp" name.
package atomic

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// WriteFile writes body to path atomically. The write happens via a
// temp file in the same directory as path, followed by a rename. If
// the write or rename fails, the temp file is removed.
func WriteFile(path string, body []byte) error {
	return WriteVia(path, func(w io.Writer) error {
		_, err := w.Write(body)
		return err
	})
}

// WriteVia is the streaming variant of WriteFile. It creates a temp
// file in the parent directory of path, calls fn with it as an
// io.Writer, fsyncs, closes, and renames into place. If fn returns a
// non-nil error or any of the follow-up steps fail, the temp file is
// removed and no rename happens.
func WriteVia(path string, fn func(w io.Writer) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("atomic: mkdir %s: %w", dir, err)
	}

	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("atomic: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	// Best-effort mode fix — CreateTemp uses 0o600 by default, but
	// on some platforms users may have umasks that widen it. Keep it
	// narrow so partially-visible temps don't leak.
	_ = os.Chmod(tmpPath, 0o600)

	// Any error after this point must remove the temp file.
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := fn(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("atomic: sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("atomic: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("atomic: rename %s -> %s: %w", tmpPath, path, err)
	}
	failed = false
	return nil
}
