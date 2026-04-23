package namespace

import (
	"path/filepath"
	"strings"
)

// cCanonicalPathForFile returns the canonical path that file-scope
// exported decls hash with in a C translation unit. Convention:
//
//	<directory>/<basename without extension>
//
// so `src/foo.c` and `src/foo.h` both map to `src/foo` and share
// DeclIDs for their exported top-level decls. A prototype in the
// header becomes identity-equal to the matching definition in the
// source file.
//
// Limitation: separately-located headers and sources (e.g.,
// `include/foo.h` + `src/foo.c`) DO NOT merge. This is a v1
// concession — teams using split include/src layouts will see
// prototypes and definitions as separate identities for now.
func cCanonicalPathForFile(file string) string {
	abs, err := filepath.Abs(file)
	if err != nil {
		return ""
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Join(dir, stem))
}
