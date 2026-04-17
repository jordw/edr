package builtins

// Go predeclared identifiers: types, constants, functions, and common
// names from the universe block. Not exhaustive — add as dogfood surfaces
// real gaps.
var Go = from(
	// Predeclared types
	"bool", "byte", "complex64", "complex128",
	"error", "float32", "float64",
	"int", "int8", "int16", "int32", "int64",
	"rune", "string",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"any", "comparable",

	// Predeclared constants
	"true", "false", "iota", "nil",

	// Predeclared functions
	"append", "cap", "clear", "close", "complex", "copy", "delete",
	"imag", "len", "make", "max", "min", "new", "panic", "print",
	"println", "real", "recover",

	// Blank identifier
	"_",
)
