package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// pyImportPartsFromSignature unpacks the Signature that a Python
// KindImport decl carries. The Python builder encodes
// `from foo.bar import baz as qux` as Name="qux",
// Signature="foo.bar\x00baz". Returns (modulePath, origName).
func pyImportPartsFromSignature(sig string) (modulePath, origName string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return sig, ""
	}
	return sig[:i], sig[i+1:]
}

// PythonPopulator returns a Populator that resolves `from X import Y`
// statements and same-package siblings, adding their module-level
// decls as namespace entries.
func PythonPopulator(r *PythonResolver) Populator {
	return func(ns *Namespace, sr *scope.Result, _ Resolver) {
		if sr == nil {
			return
		}

		addFileDecl := func(file, importedName, localName string, source Source) {
			impRes := r.Result(file)
			if impRes == nil {
				return
			}
			for i := range impRes.Decls {
				id := &impRes.Decls[i]
				if id.Name != importedName {
					continue
				}
				if id.Kind == scope.KindImport {
					continue
				}
				if id.Scope != scope.ScopeID(1) {
					continue
				}
				ns.Entries[localName] = append(ns.Entries[localName], Entry{
					DeclID: id.ID,
					Source: source,
					File:   file,
				})
			}
		}

		// Same-package siblings: add every module-level decl under
		// its own name. Python has no export list (well, __all__
		// exists but we don't model it yet); any module-level name
		// is technically visible via `from pkg.sibling import name`.
		for _, sib := range r.SamePackageFiles(ns.File) {
			impRes := r.Result(sib)
			if impRes == nil {
				continue
			}
			for i := range impRes.Decls {
				id := &impRes.Decls[i]
				if id.Kind == scope.KindImport {
					continue
				}
				if id.Scope != scope.ScopeID(1) {
					continue
				}
				ns.Entries[id.Name] = append(ns.Entries[id.Name], Entry{
					DeclID: id.ID,
					Source: SourceSamePackage,
					File:   sib,
				})
			}
		}

		// `from X import Y` decls.
		for _, d := range sr.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			modPath, origName := pyImportPartsFromSignature(d.Signature)
			if modPath == "" || origName == "" {
				continue
			}
			files := r.FilesForImport(modPath, ns.File)
			for _, f := range files {
				addFileDecl(f, origName, d.Name, SourceImported)
			}
		}
	}
}
