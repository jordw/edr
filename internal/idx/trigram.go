// Package idx implements a trigram index for fast text search pre-filtering.
//
// A trigram is a 3-byte sequence. Given a search query, we extract its trigrams
// and intersect their posting lists to find candidate files that *might* contain
// the query. Those candidates are then verified with a full read (the existing
// body-search path). This eliminates reading files that can't possibly match.
package idx

import "sync"

// ExtractTrigrams returns the set of unique trigrams in data.
// A trigram is a contiguous 3-byte sequence. Returns nil for inputs < 3 bytes.
// trigramBitset is a pooled 2MB bitset for trigram extraction.
// Heap-allocated and pooled to avoid 2MB stack frames in goroutines.
var trigramPool = sync.Pool{
	New: func() any {
		b := make([]byte, (1<<24)/8)
		return &b
	},
}

func ExtractTrigrams(data []byte) []Trigram {
	if len(data) < 3 {
		return nil
	}
	// Use a flat array as a bitset: 256^3 = 16M bits = 2MB.
	// Pooled on the heap to avoid stack growth in goroutines.
	const size = (1 << 24) / 8
	bp := trigramPool.Get().(*[]byte)
	seen := *bp

	// Track which bytes in the bitset were touched so we only scan/clear those.
	// Max unique trigrams = min(len(data)-2, 2^24). Each trigram touches 1 byte,
	// so dirty tracks at most that many distinct byte positions.
	dirty := make([]uint32, 0, 1024)

	for i := 0; i <= len(data)-3; i++ {
		b0, b1, b2 := data[i], data[i+1], data[i+2]
		if b0 >= 'A' && b0 <= 'Z' { b0 |= 0x20 }
		if b1 >= 'A' && b1 <= 'Z' { b1 |= 0x20 }
		if b2 >= 'A' && b2 <= 'Z' { b2 |= 0x20 }
		t := uint32(b0)<<16 | uint32(b1)<<8 | uint32(b2)
		idx := t / 8
		bit := byte(1 << (t % 8))
		if seen[idx]&bit == 0 {
			seen[idx] |= bit
			dirty = append(dirty, idx)
		}
	}

	out := make([]Trigram, 0, len(dirty))
	for _, idx := range dirty {
		b := seen[idx]
		for b != 0 {
			lo := b & (-b)
			pos := idx*8 + uint32(bitIndex(lo))
			out = append(out, trigramFromUint(pos))
			b ^= lo
		}
	}

	// Clear only touched bytes instead of the full 2MB.
	for _, idx := range dirty {
		seen[idx] = 0
	}
	trigramPool.Put(bp)
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
