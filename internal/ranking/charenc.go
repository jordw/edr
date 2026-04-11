package ranking

// Character-level encoder for symbol names and file paths.
// Produces a fixed-size embedding from variable-length strings
// using trigram hashing + learned embeddings + max-pooling.
//
// This captures string similarity without vocabulary:
// "sched_tick" in query and "sched/core.c" in path share trigrams
// "sch", "che", "hed" and produce similar embeddings automatically.

const (
	// CharDim is the dimension of character trigram embeddings.
	CharDim = 8
	// NumBuckets is the number of trigram hash buckets.
	// 256 buckets × 8-dim = 2,048 embedding parameters.
	NumBuckets = 256
	// EncoderOut is the output dimension of the char encoder.
	// Two pooled trigram vectors (query + path) concatenated = 2 * CharDim = 16.
	EncoderOut = CharDim * 2
)

// CharEncoderWeights holds the trigram embedding table.
type CharEncoderWeights struct {
	// TrigramEmbed is [NumBuckets][CharDim] — shared between query and path.
	TrigramEmbed [NumBuckets * CharDim]float32
}

// EncodeString produces a CharDim-dimensional embedding for a string
// by hashing its character trigrams, looking up embeddings, and max-pooling.
func EncodeString(w *CharEncoderWeights, s string) [CharDim]float32 {
	var out [CharDim]float32
	if len(s) < 3 {
		// Pad short strings
		s = s + "   "
	}

	lower := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		lower[i] = c
	}

	first := true
	for i := 0; i <= len(lower)-3; i++ {
		// Hash trigram to bucket
		h := (uint(lower[i])*31*31 + uint(lower[i+1])*31 + uint(lower[i+2])) % NumBuckets
		off := h * CharDim
		// Max-pool: take element-wise max across all trigrams
		for d := 0; d < CharDim; d++ {
			v := w.TrigramEmbed[off+uint(d)]
			if first || v > out[d] {
				out[d] = v
			}
		}
		first = false
	}
	return out
}

// EncodePair produces the combined embedding for a query + path pair.
// Returns [EncoderOut]float32 = concat(query_embed, path_embed).
func EncodePair(w *CharEncoderWeights, query, path string) [EncoderOut]float32 {
	var out [EncoderOut]float32
	q := EncodeString(w, query)
	p := EncodeString(w, path)
	copy(out[:CharDim], q[:])
	copy(out[CharDim:], p[:])
	return out
}
