package builtins

// C++ language builtins: primitive types, stdlib (std::*) types that
// commonly appear unqualified under `using namespace std;`, and common
// stream / algorithm / memory names. Not exhaustive.
var CPP = from(
	// C primitives (C++ shares these)
	"void", "char", "short", "int", "long", "float", "double",
	"signed", "unsigned", "_Bool", "bool",
	"size_t", "ssize_t", "ptrdiff_t", "wchar_t",
	"char8_t", "char16_t", "char32_t",
	"intptr_t", "uintptr_t", "intmax_t", "uintmax_t",
	"int8_t", "int16_t", "int32_t", "int64_t",
	"uint8_t", "uint16_t", "uint32_t", "uint64_t",
	"nullptr_t", "byte",

	// Constants / keywords appearing as idents
	"true", "false", "nullptr", "NULL",
	"this", "noexcept",

	// std containers / utilities (often unqualified via `using namespace std`)
	"string", "wstring", "u8string", "u16string", "u32string",
	"string_view", "wstring_view", "u8string_view",
	"vector", "array", "deque", "list", "forward_list",
	"map", "multimap", "set", "multiset",
	"unordered_map", "unordered_multimap",
	"unordered_set", "unordered_multiset",
	"queue", "stack", "priority_queue",
	"pair", "tuple", "optional", "variant", "any",
	"unique_ptr", "shared_ptr", "weak_ptr", "auto_ptr",
	"function", "bind", "ref", "cref",
	"initializer_list", "bitset", "valarray",

	// std iostream / fs
	"cout", "cin", "cerr", "clog", "endl", "flush",
	"ostream", "istream", "iostream", "ofstream", "ifstream", "fstream",
	"stringstream", "ostringstream", "istringstream",
	"filesystem", "path",

	// std algorithms (commonly seen bare)
	"move", "forward", "swap", "exchange",
	"make_unique", "make_shared", "make_pair", "make_tuple",
	"min", "max", "abs", "clamp",
	"sort", "stable_sort", "find", "find_if", "count", "count_if",
	"copy", "copy_if", "transform", "accumulate", "reduce",
	"begin", "end", "rbegin", "rend", "cbegin", "cend",
	"distance", "advance", "next", "prev",
	"for_each", "all_of", "any_of", "none_of",

	// std exceptions
	"exception", "runtime_error", "logic_error",
	"invalid_argument", "out_of_range", "length_error",
	"domain_error", "range_error", "overflow_error", "underflow_error",
	"bad_alloc", "bad_cast", "bad_typeid", "bad_function_call",
	"bad_variant_access", "bad_optional_access",

	// std type traits / numeric limits
	"numeric_limits", "type_info", "nullptr_t",
	"is_same", "is_base_of", "is_convertible", "enable_if",
	"decay_t", "remove_reference_t", "remove_const_t", "remove_pointer_t",
	"conditional_t", "void_t", "declval",

	// Common typedefs
	"ptrdiff_t", "max_align_t", "off_t", "time_t",

	// Threading / atomic
	"thread", "mutex", "lock_guard", "unique_lock", "shared_lock",
	"condition_variable", "future", "promise", "async",
	"atomic", "atomic_flag",

	// Namespace names that appear as idents
	"std",
)
