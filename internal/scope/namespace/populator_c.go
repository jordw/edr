package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// cIncludePathFromSignature unpacks the Signature a C KindImport
// decl carries. The C builder encodes `#include "foo.h"` as
// Name="foo.h", Signature="foo.h\x00\"". Returns (path, quoteStyle).
// For angle-bracket includes the quote style is "<".
func cIncludePathFromSignature(sig string) (path, quoteStyle string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

// CPopulator returns a Populator that surfaces decls reachable
// through `#include "…"` directives plus the sibling .c/.h file.
// Angle-bracket includes are skipped (system headers live outside
// the repo). Only Exported decls at file scope are added — static
// decls are translation-unit-local.
//
// v1 deferred:
//   - Transitive includes (only one level is walked).
//   - Preprocessor conditionals (#ifdef, #if) — code inside is
//     parsed unconditionally by the builder anyway.
//   - Macro-defined names from imported macros don't propagate.
func CPopulator(r *CResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}

		addFileDecls := func(file string, source Source) {
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

		// Sibling .c/.h — treat as SourceSamePackage since the decls
		// merge by canonical path.
		for _, sib := range r.SamePackageFiles(ns.File) {
			addFileDecls(sib, SourceSamePackage)
		}

		// #include'd headers.
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			path, quote := cIncludePathFromSignature(d.Signature)
			if path == "" || quote == "<" {
				continue
			}
			files := r.FilesForImport(path, ns.File)
			for _, f := range files {
				addFileDecls(f, SourceIncluded)
			}
		}
	}
}
