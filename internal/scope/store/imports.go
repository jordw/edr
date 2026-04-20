package store

import "strings"

// resolveImports is Phase 1 of the cross-file import graph. After
// per-file parsing + within/cross-file decl merging, each language's
// resolver rewrites Ref.Binding so that refs to local KindImport decls
// point at the actual exported Decl in the source file.
//
// Each language's resolver lives in imports_<lang>.go and is a no-op
// on files that don't belong to that language. Callers can add a new
// language by writing imports_<lang>.go and adding a call here.
//
// `root` is the repo root (absolute filesystem path). Resolvers that
// need to read sidecar files (go.mod, tsconfig.json, __init__.py) pull
// them from disk; resolvers that work entirely off the parsed Results
// ignore the parameter.
func resolveImports(parsed []parsedFile, root string) {
	resolveImportsTS(parsed)
	resolveImportsGo(parsed, root)
	resolveImportsPython(parsed)
	resolveImportsJava(parsed)
	resolveImportsKotlin(parsed)
	resolveImportsRust(parsed)
	resolveImportsRuby(parsed)
	resolveImportsSwift(parsed)
	resolveImportsPHP(parsed)
	resolveImportsCSharp(parsed)
	resolveImportsC(parsed)
	resolveImportsCpp(parsed)
}

// parseImportSignature splits a Decl.Signature value stamped by a
// language's scope builder for KindImport decls. Convention (shared
// across all languages): "<modulePath>\x00<origName>". `<origName>`
// is the name as it appears in the source module (differs from the
// binding name when the import is aliased). Returns ("", "") on
// malformed input.
func parseImportSignature(sig string) (path, orig string) {
	i := strings.IndexByte(sig, 0)
	if i < 0 {
		return "", ""
	}
	return sig[:i], sig[i+1:]
}

// isRelativeImport reports whether a module specifier is a relative
// path (starts with "./" or "../"). Absolute paths ("/…") and bare
// specifiers ("react", "@acme/ui") are not relative.
func isRelativeImport(path string) bool {
	return strings.HasPrefix(path, "./") || strings.HasPrefix(path, "../")
}
