package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// tsImportPartsFromSignature unpacks the Signature that a TS
// KindImport decl carries. The TS builder encodes
// `import { foo as bar } from './baz'` as Name="bar",
// Signature="./baz\x00foo". Returns (modulePath, origName).
func tsImportPartsFromSignature(sig string) (modulePath, origName string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

// TSPopulator returns a Populator that resolves TS/JS import decls
// and adds their targets as namespace entries. For each KindImport
// decl, the resolver maps the import specifier to a file on disk;
// the populator then finds the decl in that file whose name matches
// the import's original name (not the local alias) and surfaces it
// under the local name with the target's DeclID.
func TSPopulator(r *TSResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			modPath, origName := tsImportPartsFromSignature(d.Signature)
			if modPath == "" || origName == "" {
				continue
			}
			files := r.FilesForImport(modPath, ns.File)
			for _, f := range files {
				impRes := r.Result(f)
				if impRes == nil {
					continue
				}
				for i := range impRes.Decls {
					id := &impRes.Decls[i]
					if id.Name != origName {
						continue
					}
					if id.Kind == scope.KindImport {
						continue
					}
					if id.Scope != scope.ScopeID(1) {
						continue
					}
					// Surface under the LOCAL name (d.Name), which
					// may differ from origName due to `as` aliasing.
					ns.Entries[d.Name] = append(ns.Entries[d.Name], Entry{
						DeclID: id.ID,
						Source: SourceImported,
						File:   f,
					})
				}
			}
		}
	}
}
