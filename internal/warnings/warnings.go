// Package warnings detects when the agent's view of the world is stale.
// It checks for files modified externally and returns structured results
// that edr context includes in its response.
package warnings

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/session"
)

// Warning represents a single proactive alert.
type Warning struct {
	Key     string // dedup key
	Message string
	File    string // relative path
	Kind    string // "modified", "deleted"
	OpID    string // op when file was last read
}

// Check inspects session state for conditions the agent should know about.
// It returns warnings for:
//   - Files modified externally since the agent last read them
//   - Stale assumptions (signature changes detected by the index)
func Check(sess *session.Session, root string) []Warning {
	var out []Warning

	// 1. External file modifications
	out = append(out, checkExternalMods(sess, root)...)

	// 2. Stale assumptions are added by the caller via CheckStaleAssumptions,
	//    since they require a DB handle that the warnings package doesn't import.

	return out
}

// checkExternalMods compares tracked file mtimes against current disk state.
func checkExternalMods(sess *session.Session, root string) []Warning {
	tracked := sess.GetFileMtimes()
	if len(tracked) == 0 {
		return nil
	}

	var out []Warning
	for relPath, entry := range tracked {
		absPath := relPath
		if root != "" && !strings.HasPrefix(relPath, "/") {
			absPath = root + "/" + relPath
		}

		info, err := os.Stat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				out = append(out, Warning{
					Key:     "ext_del:" + relPath,
					Message: fmt.Sprintf("%s was deleted externally since op %s", relPath, entry.OpID),
					File:    relPath,
					Kind:    "deleted",
					OpID:    entry.OpID,
				})
			}
			continue
		}

		currentMtime := info.ModTime().UnixMicro()
		if currentMtime == entry.Mtime {
			continue // mtime unchanged, skip expensive hash
		}

		// Mtime changed — rehash to confirm actual content change (touch without edit is harmless)
		currentHash := fileHash(absPath)
		if currentHash == entry.Hash {
			// File was touched but content is the same — update mtime silently
			sess.UpdateFileMtime(relPath, currentMtime)
			continue
		}

		out = append(out, Warning{
			Key:     "ext_mod:" + relPath + ":" + currentHash,
			Message: fmt.Sprintf("%s modified externally since op %s", relPath, entry.OpID),
			File:    relPath,
			Kind:    "modified",
			OpID:    entry.OpID,
		})
	}

	return out
}

// fileHash returns the SHA-256 prefix of a file's content.
func fileHash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}
// changed
