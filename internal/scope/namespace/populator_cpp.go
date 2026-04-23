package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

func cppIncludePathFromSignature(sig string) (path, quote string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

// CppPopulator surfaces decls reachable through quoted `#include`
// directives plus sibling source/header files. Mirrors the C
// populator: only Exported file-scope decls are added.
func CppPopulator(r *CppResolver) Populator {
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
				if !d.Exported {
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
			path, quote := cppIncludePathFromSignature(d.Signature)
			if path == "" || quote == "<" {
				continue
			}
			for _, f := range r.FilesForImport(path, ns.File) {
				surface(f, SourceIncluded)
			}
		}
	}
}
