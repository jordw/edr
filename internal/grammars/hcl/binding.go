package hcl

// #cgo CFLAGS: -std=c11 -fPIC
// #cgo CXXFLAGS: -std=c++14 -fPIC
// #include "src/parser.c"
import "C"

import "unsafe"

func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_hcl())
}
