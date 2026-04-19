package dispatch

import (
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
)

// DefaultSymbolExtractor returns the canonical SymbolExtractFn used by
// edr. It wraps index.Parse (pure-Go regex symbol extraction) and
// translates each index.SymbolInfo into an idx.SymbolEntry. The
// FileID is left zero — callers set it when they know which file
// table slot the entries belong to.
//
// This helper exists so the full-index path (runIndex) and the
// incremental-tick path (IncrementalTick → PatchDirtyFiles) use the
// same extractor. A single source of truth means modify/add symbol
// recovery in the tick matches what a full rebuild would produce.
func DefaultSymbolExtractor() idx.SymbolExtractFn {
	return func(path string, data []byte) []idx.SymbolEntry {
		syms := index.Parse(path, data)
		if len(syms) == 0 {
			return nil
		}
		entries := make([]idx.SymbolEntry, len(syms))
		for i, s := range syms {
			entries[i] = idx.SymbolEntry{
				Name:      s.Name,
				Kind:      idx.ParseKind(s.Type),
				StartLine: s.StartLine,
				EndLine:   s.EndLine,
				StartByte: s.StartByte,
				EndByte:   s.EndByte,
			}
		}
		return entries
	}
}
