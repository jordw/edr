package namespace

import (
	"github.com/jordw/edr/internal/scope"
)

// RubyPopulator surfaces module-level decls from same-directory
// siblings and `require_relative`-loaded files. Ruby has no
// per-symbol import syntax — requiring a file loads its entire
// top-level — so the populator treats every module-level decl as
// visible.
func RubyPopulator(r *RubyResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}
		surface := func(file string, source Source) {
			res := r.Result(file)
			if res == nil {
				return
			}
			for i := range res.Decls {
				d := &res.Decls[i]
				if d.Kind == scope.KindImport {
					continue
				}
				if d.Scope != scope.ScopeID(1) {
					continue
				}
				ns.Entries[d.Name] = append(ns.Entries[d.Name], Entry{
					DeclID: d.ID,
					Source: source,
					File:   file,
				})
			}
		}
		for _, sib := range r.SamePackageFiles(ns.File) {
			surface(sib, SourceSamePackage)
		}
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			// Ruby builder stores the raw path in Decl.Name (no
			// separate Signature encoding). Treat the name as the
			// require_relative spec.
			for _, f := range r.FilesForImport(d.Name, ns.File) {
				surface(f, SourceImported)
			}
		}
	}
}
