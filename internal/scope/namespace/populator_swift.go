package namespace

import (
	"github.com/jordw/edr/internal/scope"
)

// SwiftPopulator surfaces file-scope decls from same-directory
// siblings. Namespace-clause-based imports aren't modeled yet; the
// dispatch branch falls through to the generic ref-filter path
// when this populator can't resolve a cross-file match.
func SwiftPopulator(r *SwiftResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}
		for _, sib := range r.SamePackageFiles(ns.File) {
			res := r.Result(sib)
			if res == nil {
				continue
			}
			for i := range res.Decls {
				d := &res.Decls[i]
				if d.Kind == scope.KindImport {
					continue
				}
				if !d.Exported {
					continue
				}
				if d.Scope != scope.ScopeID(1) {
					continue
				}
				ns.Entries[d.Name] = append(ns.Entries[d.Name], Entry{
					DeclID: d.ID,
					Source: SourceSamePackage,
					File:   sib,
				})
			}
		}
	}
}
