package idx

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"

	atomicio "github.com/jordw/edr/internal/atomic"
)

const (
	PopularityFile    = "popularity.bin"
	popularityMagic   = 0x45504F50 // "EPOP"
	popularityVersion = 1
	maxCallersPerName = 50000
)

// ComputePopularity computes per-symbol popularity scores.
// For each symbol S defining name X in file F, the score is the number of
// files that both (a) contain a symbol referencing X and (b) import F.
// This directly measures how many call sites resolve to each definition.
func ComputePopularity(symbols []SymbolEntry, files []FileEntry, graph *ImportGraphData, refGraph *RefGraphData) []uint16 {
	scores := make([]uint16, len(symbols))
	if graph == nil || refGraph == nil || len(symbols) == 0 {
		return scores
	}

	// Build importer sets from import graph edges: importedGraphFileID → set of importerGraphFileIDs.
	// O(edges), ~35ms on 92K-file Linux kernel.
	importerSets := make([]map[uint32]bool, len(graph.Files))
	for _, e := range graph.Edges {
		if importerSets[e.Imported] == nil {
			importerSets[e.Imported] = make(map[uint32]bool)
		}
		importerSets[e.Imported][e.Importer] = true
	}

	// Map index file paths → graph file IDs (the two file tables may differ).
	graphFileIdx := make(map[string]uint32, len(graph.Files))
	for i, f := range graph.Files {
		graphFileIdx[f] = uint32(i)
	}
	indexToGraph := make([]int32, len(files))
	for i, f := range files {
		if gid, ok := graphFileIdx[f.Path]; ok {
			indexToGraph[i] = int32(gid)
		} else {
			indexToGraph[i] = -1
		}
	}

	// Precompute per-graph-file caller weight: 1 + log2(1 + inboundCount).
	// A caller file that is itself heavily imported counts more — one step
	// of PageRank. A mock or test file (inbound = 0) contributes weight 1;
	// a canonical package file with ~100 imports contributes ~7.7. This
	// deranks mock-heavy defs (mocks are only referenced by tests, which
	// are referenced by nothing) without a path-pattern rule.
	callerWeights := make([]float64, len(graph.Files))
	for i := range graph.Files {
		inb := uint32(0)
		if i < len(graph.InboundCount) {
			inb = graph.InboundCount[i]
		}
		callerWeights[i] = 1.0 + math.Log2(1.0+float64(inb))
	}

	// Group symbols by name hash.
	type symRef struct {
		id     uint32
		fileID uint32
	}
	nameGroups := make(map[uint64][]symRef, len(symbols)/3)
	for i, s := range symbols {
		h := NameHash(s.Name)
		nameGroups[h] = append(nameGroups[h], symRef{id: uint32(i), fileID: s.FileID})
	}

	// For each name, intersect caller files with each definition's importers.
	for nh, defs := range nameGroups {
		callerIDs := refGraph.CallersByName(nh)
		if len(callerIDs) == 0 {
			continue
		}

		// Cap callers for very common names. For capped names, fall back
		// to the file's inbound import count as a proxy.
		if len(callerIDs) > maxCallersPerName {
			for _, d := range defs {
				if int(d.fileID) >= len(files) {
					continue
				}
				gfid := indexToGraph[d.fileID]
				if gfid < 0 || int(gfid) >= len(graph.InboundCount) {
					continue
				}
				score := graph.InboundCount[gfid]
				if score > 65535 {
					score = 65535
				}
				scores[d.id] = uint16(score)
			}
			continue
		}

		// Map callers to graph file IDs (deduplicate).
		callerGraphFiles := make(map[uint32]bool, len(callerIDs)/4+1)
		for _, cid := range callerIDs {
			if int(cid) < len(symbols) {
				fid := symbols[cid].FileID
				if int(fid) < len(files) {
					gfid := indexToGraph[fid]
					if gfid >= 0 {
						callerGraphFiles[uint32(gfid)] = true
					}
				}
			}
		}

		// For each definition, sum caller-quality weights for callers whose
		// file imports this def's file.
		for _, d := range defs {
			if int(d.fileID) >= len(files) {
				continue
			}
			gfid := indexToGraph[d.fileID]
			if gfid < 0 || int(gfid) >= len(importerSets) {
				continue
			}
			importers := importerSets[gfid]
			if len(importers) == 0 {
				continue
			}

			weighted := 0.0
			if len(callerGraphFiles) < len(importers) {
				for cf := range callerGraphFiles {
					if importers[cf] && int(cf) < len(callerWeights) {
						weighted += callerWeights[cf]
					}
				}
			} else {
				for imp := range importers {
					if callerGraphFiles[imp] && int(imp) < len(callerWeights) {
						weighted += callerWeights[imp]
					}
				}
			}
			if weighted > 65535 {
				weighted = 65535
			}
			scores[d.id] = uint16(weighted)
		}
	}

	return scores
}

// WritePopularity writes per-symbol popularity scores to the edr directory.
func WritePopularity(edrDir string, scores []uint16) error {
	buf := make([]byte, 12+2*len(scores))
	binary.LittleEndian.PutUint32(buf[0:], popularityMagic)
	binary.LittleEndian.PutUint32(buf[4:], popularityVersion)
	binary.LittleEndian.PutUint32(buf[8:], uint32(len(scores)))
	for i, s := range scores {
		binary.LittleEndian.PutUint16(buf[12+2*i:], s)
	}
	return atomicio.WriteFile(filepath.Join(edrDir, PopularityFile), buf)
}

// ReadPopularity loads per-symbol popularity scores from the edr directory.
// Returns nil if the file doesn't exist, is corrupt, or numSymbols doesn't match.
func ReadPopularity(edrDir string, numSymbols int) []uint16 {
	data, err := os.ReadFile(filepath.Join(edrDir, PopularityFile))
	if err != nil {
		return nil
	}
	if len(data) < 12 {
		return nil
	}
	if binary.LittleEndian.Uint32(data[0:]) != popularityMagic {
		return nil
	}
	if binary.LittleEndian.Uint32(data[4:]) != popularityVersion {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(data[8:]))
	if n != numSymbols {
		return nil // stale — symbol count changed
	}
	if len(data) < 12+2*n {
		return nil
	}
	scores := make([]uint16, n)
	for i := range scores {
		scores[i] = binary.LittleEndian.Uint16(data[12+2*i:])
	}
	return scores
}
