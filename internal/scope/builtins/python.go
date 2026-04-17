package builtins

// Python 3 builtins: types, constants, functions, and common names.
// Not exhaustive — add as dogfood surfaces real gaps.
var Python = from(
	// Keywords that also appear in value positions
	"None", "True", "False", "self", "cls",

	// Built-in types
	"int", "float", "complex", "bool", "str", "bytes", "bytearray",
	"memoryview", "list", "tuple", "set", "frozenset", "dict",
	"object", "type", "slice", "range",

	// Built-in functions
	"abs", "all", "any", "ascii", "bin", "breakpoint", "callable",
	"chr", "classmethod", "compile", "delattr", "dir", "divmod",
	"enumerate", "eval", "exec", "exit", "filter", "format", "frozenset",
	"getattr", "globals", "hasattr", "hash", "help", "hex", "id",
	"input", "isinstance", "issubclass", "iter", "len", "locals",
	"map", "max", "min", "next", "oct", "open", "ord", "pow",
	"print", "property", "quit", "repr", "reversed", "round",
	"setattr", "sorted", "staticmethod", "sum", "super", "vars", "zip",
	"__import__",

	// Exceptions
	"BaseException", "Exception", "ArithmeticError", "AssertionError",
	"AttributeError", "BlockingIOError", "BrokenPipeError", "BufferError",
	"BytesWarning", "ChildProcessError", "ConnectionAbortedError",
	"ConnectionError", "ConnectionRefusedError", "ConnectionResetError",
	"DeprecationWarning", "EOFError", "EnvironmentError", "FileExistsError",
	"FileNotFoundError", "FloatingPointError", "FutureWarning",
	"GeneratorExit", "IOError", "ImportError", "ImportWarning",
	"IndentationError", "IndexError", "InterruptedError", "IsADirectoryError",
	"KeyError", "KeyboardInterrupt", "LookupError", "MemoryError",
	"ModuleNotFoundError", "NameError", "NotADirectoryError",
	"NotImplementedError", "OSError", "OverflowError", "PendingDeprecationWarning",
	"PermissionError", "ProcessLookupError", "RecursionError",
	"ReferenceError", "ResourceWarning", "RuntimeError", "RuntimeWarning",
	"StopAsyncIteration", "StopIteration", "SyntaxError", "SyntaxWarning",
	"SystemError", "SystemExit", "TabError", "TimeoutError", "TypeError",
	"UnboundLocalError", "UnicodeDecodeError", "UnicodeEncodeError",
	"UnicodeError", "UnicodeTranslateError", "UnicodeWarning",
	"UserWarning", "ValueError", "Warning", "ZeroDivisionError",
	"NotImplemented", "Ellipsis", "__debug__",
)
