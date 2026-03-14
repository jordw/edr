package typescript

// #cgo CFLAGS: -std=c11 -fPIC
// #cgo CPPFLAGS: -I${SRCDIR}/tsx
// #include "tsx/parser.c"
// #include "tsx/scanner.c"
import "C"

import "unsafe"

func LanguageTSX() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_tsx())
}
