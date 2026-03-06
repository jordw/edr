package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// Span represents a byte range within a file.
type Span struct {
	StartByte uint32
	EndByte   uint32
}

// Edit represents a single edit operation on a file.
type Edit struct {
	File        string
	Span        Span
	Replacement string
	ExpectHash  string // optional; if set, the file hash is checked before applying
}

// FileHash returns the first 8 characters of the hex-encoded SHA256 hash of the
// file at the given path.
func FileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("filehash: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:8], nil
}

// ReplaceSpan reads the file at path, optionally verifies its hash against
// expectHash, replaces the byte range [startByte, endByte) with replacement,
// and writes the result back. If expectHash is non-empty and does not match the
// current file hash, an error is returned without modifying the file.
func ReplaceSpan(path string, startByte, endByte uint32, replacement string, expectHash string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("replacespan: read: %w", err)
	}

	if expectHash != "" {
		sum := sha256.Sum256(data)
		actual := hex.EncodeToString(sum[:])[:8]
		if actual != expectHash {
			return fmt.Errorf("replacespan: hash mismatch for %s: expected %s, got %s", path, expectHash, actual)
		}
	}

	if int(startByte) > len(data) || int(endByte) > len(data) || startByte > endByte {
		return fmt.Errorf("replacespan: invalid byte range [%d, %d) for file of length %d", startByte, endByte, len(data))
	}

	result := make([]byte, 0, int(startByte)+len(replacement)+len(data)-int(endByte))
	result = append(result, data[:startByte]...)
	result = append(result, []byte(replacement)...)
	result = append(result, data[endByte:]...)

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("replacespan: stat: %w", err)
	}

	if err := os.WriteFile(path, result, info.Mode()); err != nil {
		return fmt.Errorf("replacespan: write: %w", err)
	}

	return nil
}

// ReplaceSymbol replaces the byte span of a named symbol in the file. It
// delegates to ReplaceSpan using the provided startByte and endByte for the
// symbol's span.
func ReplaceSymbol(path string, symbolName string, startByte, endByte uint32, replacement string, expectHash string) error {
	_ = symbolName // used for documentation / intent; the span is authoritative
	return ReplaceSpan(path, startByte, endByte, replacement, expectHash)
}

// InsertAfterSpan inserts content into the file at path immediately after the
// given byte position afterEndByte.
func InsertAfterSpan(path string, afterEndByte uint32, content string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("insertafterspan: read: %w", err)
	}

	if int(afterEndByte) > len(data) {
		return fmt.Errorf("insertafterspan: position %d beyond file length %d", afterEndByte, len(data))
	}

	result := make([]byte, 0, len(data)+len(content))
	result = append(result, data[:afterEndByte]...)
	result = append(result, []byte(content)...)
	result = append(result, data[afterEndByte:]...)

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("insertafterspan: stat: %w", err)
	}

	if err := os.WriteFile(path, result, info.Mode()); err != nil {
		return fmt.Errorf("insertafterspan: write: %w", err)
	}

	return nil
}
