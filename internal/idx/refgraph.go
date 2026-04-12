package idx

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
)

const (
	RefGraphFile    = "refs.bin"
	refGraphMagic   = 0x45524546 // "EREF"
	refGraphVersion = 1
)

// RefEdge represents one symbol-to-symbol reference.
type RefEdge struct {
	Caller uint32 // index into symbol table
	Callee uint32 // index into symbol table
}

// RefGraphData is the in-memory representation of the reference graph.
type RefGraphData struct {
	NumSymbols uint32
	Edges      []RefEdge // sorted by (Callee, Caller) for fast "who references X?"
}

// Callers returns the symbol IDs that reference the given symbol ID.
func (g *RefGraphData) Callers(calleeID uint32) []uint32 {
	if g == nil {
		return nil
	}
	// Binary search for first edge with this callee.
	lo := sort.Search(len(g.Edges), func(i int) bool {
		return g.Edges[i].Callee >= calleeID
	})
	var result []uint32
	for i := lo; i < len(g.Edges) && g.Edges[i].Callee == calleeID; i++ {
		result = append(result, g.Edges[i].Caller)
	}
	return result
}

// Callees returns the symbol IDs referenced by the given symbol ID.
func (g *RefGraphData) Callees(callerID uint32) []uint32 {
	if g == nil {
		return nil
	}
	var result []uint32
	for _, e := range g.Edges {
		if e.Caller == callerID {
			result = append(result, e.Callee)
		}
	}
	return result
}

// BuildRefGraph constructs the graph from raw edges, deduplicating and sorting.
func BuildRefGraph(numSymbols uint32, raw []RefEdge) *RefGraphData {
	// Dedup
	type edgeKey struct{ a, b uint32 }
	seen := make(map[edgeKey]bool, len(raw))
	edges := make([]RefEdge, 0, len(raw))
	for _, e := range raw {
		k := edgeKey{e.Caller, e.Callee}
		if !seen[k] {
			seen[k] = true
			edges = append(edges, e)
		}
	}
	// Sort by (Callee, Caller) for fast callers lookup.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Callee != edges[j].Callee {
			return edges[i].Callee < edges[j].Callee
		}
		return edges[i].Caller < edges[j].Caller
	})
	return &RefGraphData{NumSymbols: numSymbols, Edges: edges}
}

// WriteRefGraph writes the graph to the edr directory.
func WriteRefGraph(edrDir string, g *RefGraphData) error {
	path := filepath.Join(edrDir, RefGraphFile)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	binary.Write(f, binary.LittleEndian, uint32(refGraphMagic))
	binary.Write(f, binary.LittleEndian, uint32(refGraphVersion))
	binary.Write(f, binary.LittleEndian, g.NumSymbols)
	binary.Write(f, binary.LittleEndian, uint32(len(g.Edges)))

	for _, e := range g.Edges {
		binary.Write(f, binary.LittleEndian, e.Caller)
		binary.Write(f, binary.LittleEndian, e.Callee)
	}
	return nil
}

// ReadRefGraph loads the reference graph from the edr directory.
// Returns nil if the file does not exist or is corrupt.
func ReadRefGraph(edrDir string) *RefGraphData {
	path := filepath.Join(edrDir, RefGraphFile)
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 16 {
		return nil
	}

	magic := binary.LittleEndian.Uint32(data[0:4])
	version := binary.LittleEndian.Uint32(data[4:8])
	numSymbols := binary.LittleEndian.Uint32(data[8:12])
	numEdges := binary.LittleEndian.Uint32(data[12:16])

	if magic != refGraphMagic || version != refGraphVersion {
		return nil
	}

	pos := 16
	edges := make([]RefEdge, numEdges)
	for i := uint32(0); i < numEdges; i++ {
		if pos+8 > len(data) {
			return nil
		}
		edges[i] = RefEdge{
			Caller: binary.LittleEndian.Uint32(data[pos : pos+4]),
			Callee: binary.LittleEndian.Uint32(data[pos+4 : pos+8]),
		}
		pos += 8
	}

	return &RefGraphData{NumSymbols: numSymbols, Edges: edges}
}

// HasRefGraph returns true if a reference graph exists in the edr directory.
func HasRefGraph(edrDir string) bool {
	_, err := os.Stat(filepath.Join(edrDir, RefGraphFile))
	return err == nil
}
