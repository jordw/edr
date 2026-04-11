package ranking

// RankResult holds a candidate's model score alongside its index.
type RankResult struct {
	Index int
	Score float32
}

// Rank scores candidates using the transformer model.
// Returns results sorted by score descending.
// If weights are nil, returns nil (caller should fall back to heuristic).
func Rank(w *Weights, query string, candidates []CandidateFeatures) []RankResult {
	if w == nil || len(candidates) == 0 {
		return nil
	}

	features := ExtractAll(query, candidates)

	// Build string arrays for char encoder
	queries := make([]string, len(candidates))
	paths := make([]string, len(candidates))
	for i, c := range candidates {
		queries[i] = query
		paths[i] = c.File
	}

	scores := Score(w, features, queries, paths, len(candidates))
	if scores == nil {
		return nil
	}

	results := make([]RankResult, len(candidates))
	for i, s := range scores {
		results[i] = RankResult{Index: i, Score: s}
	}

	// Sort by score descending
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].Score > results[j-1].Score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}

	return results
}
