package typescript

// #cgo CFLAGS: -std=c11 -fPIC
// #cgo CPPFLAGS: -I${SRCDIR}/ts
// #include "ts/parser.c"
// #include "ts/scanner.c"
import "C"

import "unsafe"

func LanguageTypescript() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_typescript())
}
