package builtins

// PHP builtins: type keywords, common built-in functions, SPL classes,
// and exception types. PHP has a huge global function namespace — this
// list covers the most commonly-used names; extend as dogfood surfaces
// real gaps. Function names are case-insensitive in PHP but conventionally
// written lowercase.
var PHP = from(
	// Type keywords (appear in type positions)
	"int", "integer", "float", "double", "bool", "boolean",
	"string", "array", "object", "callable", "iterable",
	"void", "mixed", "never", "self", "static", "parent",
	"true", "false", "null",

	// Pseudo-constants
	"__FILE__", "__LINE__", "__DIR__", "__FUNCTION__", "__CLASS__",
	"__METHOD__", "__NAMESPACE__", "__TRAIT__",
	"PHP_EOL", "PHP_INT_MAX", "PHP_INT_MIN", "PHP_INT_SIZE",
	"PHP_FLOAT_MAX", "PHP_FLOAT_MIN", "PHP_FLOAT_EPSILON",
	"PHP_VERSION", "PHP_MAJOR_VERSION", "PHP_MINOR_VERSION",
	"PHP_OS", "PHP_SAPI", "DIRECTORY_SEPARATOR", "PATH_SEPARATOR",
	"E_ERROR", "E_WARNING", "E_NOTICE", "E_PARSE", "E_ALL",
	"E_USER_ERROR", "E_USER_WARNING", "E_USER_NOTICE",
	"M_PI", "M_E", "INF", "NAN",

	// Language-construct-ish functions (also keywords in some positions)
	"echo", "print", "die", "exit",
	"isset", "empty", "unset",
	"include", "include_once", "require", "require_once",
	"array", "list",

	// String functions
	"strlen", "strpos", "strrpos", "stripos", "strripos",
	"str_replace", "str_ireplace", "str_contains", "str_starts_with",
	"str_ends_with", "str_split", "str_repeat", "str_pad",
	"substr", "substr_count", "substr_replace",
	"strtolower", "strtoupper", "ucfirst", "ucwords", "lcfirst",
	"trim", "ltrim", "rtrim", "chop",
	"explode", "implode", "join",
	"sprintf", "printf", "fprintf", "vsprintf", "vprintf",
	"number_format", "nl2br", "htmlspecialchars", "htmlentities",
	"strip_tags", "addslashes", "stripslashes",
	"preg_match", "preg_match_all", "preg_replace", "preg_replace_callback",
	"preg_split", "preg_quote",
	"chr", "ord", "bin2hex", "hex2bin",
	"base64_encode", "base64_decode", "urlencode", "urldecode",
	"rawurlencode", "rawurldecode",
	"md5", "sha1", "hash", "hash_hmac", "crc32",
	"json_encode", "json_decode",

	// Array functions
	"count", "sizeof", "array_keys", "array_values", "array_merge",
	"array_combine", "array_flip", "array_reverse", "array_unique",
	"array_map", "array_filter", "array_reduce", "array_walk",
	"array_slice", "array_splice", "array_chunk",
	"array_search", "array_key_exists", "array_key_first", "array_key_last",
	"array_push", "array_pop", "array_shift", "array_unshift",
	"array_fill", "array_fill_keys", "array_column", "array_count_values",
	"array_diff", "array_diff_key", "array_intersect", "array_intersect_key",
	"array_sum", "array_product", "range",
	"in_array", "compact", "extract",
	"sort", "rsort", "usort", "uasort", "uksort",
	"ksort", "krsort", "asort", "arsort", "natsort", "natcasesort",
	"array_reverse", "array_flip", "array_pad",
	"end", "reset", "next", "prev", "current", "key",
	"each", // deprecated but appears

	// Type / value checks
	"is_string", "is_int", "is_integer", "is_long",
	"is_float", "is_double", "is_numeric", "is_array",
	"is_bool", "is_object", "is_null", "is_callable",
	"is_iterable", "is_countable", "is_resource",
	"is_a", "is_subclass_of", "intval", "floatval", "strval", "boolval",
	"settype", "gettype", "get_debug_type",

	// Reflection-ish
	"function_exists", "class_exists", "interface_exists",
	"method_exists", "property_exists", "enum_exists",
	"defined", "define", "constant",
	"get_class", "get_parent_class", "get_called_class",
	"get_object_vars", "get_class_vars", "get_class_methods",

	// File I/O
	"file", "file_get_contents", "file_put_contents",
	"fopen", "fclose", "fread", "fwrite", "fgets", "fgetc",
	"feof", "fseek", "ftell", "rewind", "fflush",
	"file_exists", "is_file", "is_dir", "is_readable", "is_writable",
	"mkdir", "rmdir", "unlink", "rename", "copy",
	"dirname", "basename", "pathinfo", "realpath",
	"glob", "scandir", "opendir", "readdir", "closedir",

	// Date / time
	"time", "microtime", "date", "strtotime", "mktime",
	"checkdate", "date_create", "date_format",

	// Math
	"abs", "ceil", "floor", "round", "intdiv",
	"min", "max", "pow", "sqrt", "exp", "log", "log10",
	"sin", "cos", "tan", "asin", "acos", "atan", "atan2",
	"pi", "deg2rad", "rad2deg",
	"rand", "mt_rand", "random_int", "random_bytes", "srand", "mt_srand",

	// Error / debugging
	"error_reporting", "error_log", "trigger_error",
	"set_error_handler", "restore_error_handler",
	"set_exception_handler", "restore_exception_handler",
	"debug_backtrace", "debug_print_backtrace",
	"print_r", "var_dump", "var_export",

	// Variable scope helpers
	"func_get_args", "func_num_args", "func_get_arg",

	// Core classes
	"Exception", "Error", "TypeError", "ValueError",
	"ArgumentCountError", "DivisionByZeroError", "AssertionError",
	"ArithmeticError", "ParseError", "CompileError",
	"RuntimeException", "LogicException",
	"InvalidArgumentException", "OutOfRangeException",
	"OutOfBoundsException", "OverflowException", "UnderflowException",
	"DomainException", "RangeException", "LengthException",
	"BadFunctionCallException", "BadMethodCallException",
	"UnexpectedValueException",
	"Throwable", "Stringable",
	"Generator", "Closure", "Fiber",
	"Iterator", "IteratorAggregate", "Traversable",
	"Countable", "ArrayAccess", "SeekableIterator",
	"ArrayObject", "ArrayIterator", "SplObjectStorage",
	"SplStack", "SplQueue", "SplDoublyLinkedList",
	"SplFixedArray", "SplHeap", "SplMinHeap", "SplMaxHeap",
	"SplPriorityQueue", "SplFileInfo", "SplFileObject",
	"WeakMap", "WeakReference",
	"DateTime", "DateTimeImmutable", "DateTimeZone",
	"DateInterval", "DatePeriod",
	"PDO", "PDOStatement", "PDOException",
	"ReflectionClass", "ReflectionMethod", "ReflectionFunction",
	"ReflectionParameter", "ReflectionProperty", "ReflectionException",
	"Attribute", "AllowDynamicProperties", "ReturnTypeWillChange",
)
