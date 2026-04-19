package builtins

// Kotlin stdlib: primitive/wrapper types, core collections, common
// scope/extension functions, and the exception hierarchy. Not
// exhaustive — extend as dogfood surfaces real gaps.
var Kotlin = from(
	// Primitive types and their reference aliases
	"Boolean", "Byte", "Short", "Int", "Long", "Float", "Double",
	"Char", "String", "Unit", "Nothing", "Any", "Number",

	// Collection interfaces
	"List", "MutableList", "Set", "MutableSet", "Map", "MutableMap",
	"Collection", "MutableCollection", "Iterable", "MutableIterable",
	"Iterator", "MutableIterator", "ListIterator", "MutableListIterator",

	// Array types
	"Array", "IntArray", "LongArray", "FloatArray", "DoubleArray",
	"BooleanArray", "CharArray", "ByteArray", "ShortArray",

	// Sequence / pair / triple
	"Sequence", "Pair", "Triple",

	// Stdlib factory functions
	"listOf", "mutableListOf", "setOf", "mutableSetOf", "mapOf",
	"mutableMapOf", "arrayOf", "arrayListOf", "hashMapOf", "hashSetOf",
	"emptyList", "emptyMap", "emptySet", "emptyArray",

	// Top-level / scope / check functions
	"println", "print", "error", "check", "require",
	"TODO", "run", "let", "apply", "also", "with", "takeIf", "takeUnless",
	"lazy", "lazyOf",

	// Exception hierarchy
	"Throwable", "Exception", "RuntimeException", "Error",
	"IllegalArgumentException", "IllegalStateException",
	"NullPointerException", "IndexOutOfBoundsException",
	"ClassCastException", "UnsupportedOperationException",

	// Functional / comparator
	"Comparable", "Comparator", "Function", "Runnable",

	// Constants appearing as value idents
	"null", "true", "false",
)
