package idx

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
)

// ImportGraph stores the file-level import/include graph.
// Edges: file A imports/includes file B.
// Inbound counts are precomputed for fast ranking lookups.
const (
	ImportGraphFile    = "import_graph.bin"
	importGraphMagic   = 0x45494D47 // "EIMG"
	importGraphVersion = 1
)

// ImportGraphData is the in-memory representation of the import graph.
type ImportGraphData struct {
	Files   []string // sorted file paths (relative to repo root)
	fileIdx map[string]uint32

	// Edges: sorted (importer, imported) pairs as file indices.
	Edges []ImportEdge

	// InboundCount[i] = number of files that import Files[i].
	InboundCount []uint32
}

// ImportEdge represents one import relationship.
type ImportEdge struct {
	Importer uint32 // index into Files
	Imported uint32 // index into Files
}

// Inbound returns the import count for a relative file path.
// Returns 0 if the file is not in the graph.
func (g *ImportGraphData) Inbound(relPath string) int {
	if g == nil {
		return 0
	}
	id, ok := g.fileIdx[relPath]
	if !ok {
		return 0
	}
	return int(g.InboundCount[id])
}

// Importers returns the files that import the given file.
func (g *ImportGraphData) Importers(relPath string) []string {
	if g == nil {
		return nil
	}
	id, ok := g.fileIdx[relPath]
	if !ok {
		return nil
	}
	var result []string
	for _, e := range g.Edges {
		if e.Imported == id {
			result = append(result, g.Files[e.Importer])
		}
	}
	return result
}

// Imports returns the files imported by the given file.
func (g *ImportGraphData) Imports(relPath string) []string {
	if g == nil {
		return nil
	}
	id, ok := g.fileIdx[relPath]
	if !ok {
		return nil
	}
	var result []string
	for _, e := range g.Edges {
		if e.Importer == id {
			result = append(result, g.Files[e.Imported])
		}
	}
	return result
}

// BuildImportGraph constructs the graph from raw edges.
// files: all relative file paths in the repo (sorted).
// rawEdges: (importer_path, imported_path) pairs.
func BuildImportGraph(files []string, rawEdges [][2]string) *ImportGraphData {
	// Build file index
	fileIdx := make(map[string]uint32, len(files))
	for i, f := range files {
		fileIdx[f] = uint32(i)
	}

	// Resolve raw edges to file IDs, dedup
	type edgeKey struct{ a, b uint32 }
	seen := map[edgeKey]bool{}
	var edges []ImportEdge
	for _, raw := range rawEdges {
		importerID, ok1 := fileIdx[raw[0]]
		importedID, ok2 := fileIdx[raw[1]]
		if !ok1 || !ok2 || importerID == importedID {
			continue
		}
		k := edgeKey{importerID, importedID}
		if seen[k] {
			continue
		}
		seen[k] = true
		edges = append(edges, ImportEdge{Importer: importerID, Imported: importedID})
	}

	// Sort edges for binary search
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Imported != edges[j].Imported {
			return edges[i].Imported < edges[j].Imported
		}
		return edges[i].Importer < edges[j].Importer
	})

	// Compute inbound counts
	inbound := make([]uint32, len(files))
	for _, e := range edges {
		inbound[e.Imported]++
	}

	return &ImportGraphData{
		Files:        files,
		fileIdx:      fileIdx,
		Edges:        edges,
		InboundCount: inbound,
	}
}

// WriteImportGraph writes the graph to the edr directory.
func WriteImportGraph(edrDir string, g *ImportGraphData) error {
	path := filepath.Join(edrDir, ImportGraphFile)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// Header
	binary.Write(f, binary.LittleEndian, uint32(importGraphMagic))
	binary.Write(f, binary.LittleEndian, uint32(importGraphVersion))
	binary.Write(f, binary.LittleEndian, uint32(len(g.Files)))
	binary.Write(f, binary.LittleEndian, uint32(len(g.Edges)))

	// Files: length-prefixed strings
	for _, p := range g.Files {
		binary.Write(f, binary.LittleEndian, uint16(len(p)))
		f.Write([]byte(p))
	}

	// Edges
	for _, e := range g.Edges {
		binary.Write(f, binary.LittleEndian, e.Importer)
		binary.Write(f, binary.LittleEndian, e.Imported)
	}

	// Inbound counts
	for _, c := range g.InboundCount {
		binary.Write(f, binary.LittleEndian, c)
	}

	return nil
}

// ReadImportGraph loads the graph from the edr directory.
// Returns nil if the file doesn't exist or is corrupt.
func ReadImportGraph(edrDir string) *ImportGraphData {
	path := filepath.Join(edrDir, ImportGraphFile)
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 16 {
		return nil
	}

	// Header
	magic := binary.LittleEndian.Uint32(data[0:4])
	version := binary.LittleEndian.Uint32(data[4:8])
	numFiles := binary.LittleEndian.Uint32(data[8:12])
	numEdges := binary.LittleEndian.Uint32(data[12:16])

	if magic != importGraphMagic || version != importGraphVersion {
		return nil
	}

	pos := 16

	// Files
	files := make([]string, numFiles)
	fileIdx := make(map[string]uint32, numFiles)
	for i := uint32(0); i < numFiles; i++ {
		if pos+2 > len(data) {
			return nil
		}
		pLen := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+pLen > len(data) {
			return nil
		}
		files[i] = string(data[pos : pos+pLen])
		fileIdx[files[i]] = i
		pos += pLen
	}

	// Edges
	edges := make([]ImportEdge, numEdges)
	for i := uint32(0); i < numEdges; i++ {
		if pos+8 > len(data) {
			return nil
		}
		edges[i] = ImportEdge{
			Importer: binary.LittleEndian.Uint32(data[pos : pos+4]),
			Imported: binary.LittleEndian.Uint32(data[pos+4 : pos+8]),
		}
		pos += 8
	}

	// Inbound counts
	inbound := make([]uint32, numFiles)
	for i := uint32(0); i < numFiles; i++ {
		if pos+4 > len(data) {
			return nil
		}
		inbound[i] = binary.LittleEndian.Uint32(data[pos : pos+4])
		pos += 4
	}

	return &ImportGraphData{
		Files:        files,
		fileIdx:      fileIdx,
		Edges:        edges,
		InboundCount: inbound,
	}
}

// HasImportGraph returns true if an import graph exists in the edr directory.
func HasImportGraph(edrDir string) bool {
	_, err := os.Stat(filepath.Join(edrDir, ImportGraphFile))
	return err == nil
}
