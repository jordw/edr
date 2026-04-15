package ledger

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// SiteKeyVersion is the prefix used in SiteKey serialization. Bump to
// invalidate all previously-computed keys if the input format changes.
const SiteKeyVersion = "sitekey/v1"

// ComputeSiteKey returns the canonical SiteKey for a site.
//
// The hash input is:
//
//	sha256( "sitekey/v1" || file || 0x00 || byte_start_be32 || byte_end_be32 || 0x00 || tier || 0x00 || old_bytes )
//
// where:
//   - file is the repo-relative path with forward slashes, no leading "./",
//     no trailing "/", symlinks resolved by the caller.
//   - byte_start_be32 / byte_end_be32 are 4-byte big-endian unsigned integers.
//   - tier is the raw Tier string.
//   - old_bytes is the literal bytes currently at [byte_start, byte_end).
//
// Output is lowercase hex, 64 characters.
func ComputeSiteKey(file string, byteStart, byteEnd int, tier Tier, oldBytes []byte) string {
	h := sha256.New()
	h.Write([]byte(SiteKeyVersion))
	h.Write([]byte(file))
	h.Write([]byte{0})
	var be [4]byte
	binary.BigEndian.PutUint32(be[:], uint32(byteStart))
	h.Write(be[:])
	binary.BigEndian.PutUint32(be[:], uint32(byteEnd))
	h.Write(be[:])
	h.Write([]byte{0})
	h.Write([]byte(tier))
	h.Write([]byte{0})
	h.Write(oldBytes)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}
