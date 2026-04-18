package builtins

// Java language builtins: primitive types and common java.lang.* names
// that are auto-imported. Not exhaustive — extend as dogfood surfaces
// real gaps.
var Java = from(
	// Primitives
	"boolean", "byte", "short", "int", "long", "float", "double",
	"char", "void",

	// java.lang types (implicitly imported)
	"Object", "String", "StringBuilder", "StringBuffer",
	"Integer", "Long", "Short", "Byte", "Float", "Double",
	"Boolean", "Character", "Number",
	"Math", "System", "Runtime", "Thread", "ThreadLocal",
	"Class", "ClassLoader", "Package", "Process", "ProcessBuilder",
	"Iterable", "Comparable", "Cloneable", "Runnable",
	"Exception", "RuntimeException", "Error", "Throwable",
	"IllegalArgumentException", "IllegalStateException",
	"NullPointerException", "IndexOutOfBoundsException",
	"ArithmeticException", "ArrayIndexOutOfBoundsException",
	"ClassCastException", "NumberFormatException",
	"UnsupportedOperationException",
	"Enum", "Record", "Void",
	"Override", "Deprecated", "SuppressWarnings", "FunctionalInterface",
	"SafeVarargs",

	// Common literal keywords that may appear as identifiers in some positions
	"null", "true", "false",
)
