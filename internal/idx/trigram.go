// Package idx implements a trigram index for fast text search pre-filtering.
//
// A trigram is a 3-byte sequence. Given a search query, we extract its trigrams
// and intersect their posting lists to find candidate files that *might* contain
// the query. Those candidates are then verified with a full read (the existing
// body-search path). This eliminates reading files that can't possibly match.
package idx

// ExtractTrigrams returns the set of unique trigrams in data.
// A trigram is a contiguous 3-byte sequence. Returns nil for inputs < 3 bytes.
func ExtractTrigrams(data []byte) []Trigram {
	if len(data) < 3 {
		return nil
	}
	// Use a flat array as a bitset: 256^3 = 16M bits = 2MB.
	// This is the fastest approach for large files and avoids map overhead.
	const size = (1 << 24) / 8
	var seen [size]byte

	for i := 0; i <= len(data)-3; i++ {
		t := uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
		seen[t/8] |= 1 << (t % 8)
	}

	// Count first, then allocate exact size.
	n := 0
	for _, b := range seen {
		n += popcount(b)
	}
	out := make([]Trigram, 0, n)
	for i := range seen {
		b := seen[i]
		for b != 0 {
			bit := b & (-b)         // lowest set bit
			pos := uint32(i)*8 + uint32(bitIndex(bit))
			out = append(out, trigramFromUint(pos))
			b ^= bit
		}
	}
	return out
}

// QueryTrigrams returns the trigrams for a search query string.
// For case-insensitive search, pass the lowercased query.
func QueryTrigrams(query string) []Trigram {
	if len(query) < 3 {
		return nil
	}
	seen := make(map[Trigram]struct{})
	b := []byte(query)
	for i := 0; i <= len(b)-3; i++ {
		t := Trigram{b[i], b[i+1], b[i+2]}
		seen[t] = struct{}{}
	}
	out := make([]Trigram, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	return out
}

// Trigram is a 3-byte sequence.
type Trigram [3]byte

func trigramFromUint(v uint32) Trigram {
	return Trigram{byte(v >> 16), byte(v >> 8), byte(v)}
}

// ToUint32 converts a trigram to a sortable uint32.
func (t Trigram) ToUint32() uint32 {
	return uint32(t[0])<<16 | uint32(t[1])<<8 | uint32(t[2])
}

func popcount(b byte) int {
	// Brian Kernighan's algorithm
	n := 0
	for b != 0 {
		b &= b - 1
		n++
	}
	return n
}

func bitIndex(b byte) int {
	switch b {
	case 1:
		return 0
	case 2:
		return 1
	case 4:
		return 2
	case 8:
		return 3
	case 16:
		return 4
	case 32:
		return 5
	case 64:
		return 6
	case 128:
		return 7
	}
	return 0
}
