package builtins

// C language + common libc names. Covers primitive types, the types
// declared via <stdint.h>/<stddef.h>, and the most-used stdio/stdlib/
// string/ctype/math functions. Not exhaustive — extend as dogfood
// surfaces real gaps.
var C = from(
	// Primitive type keywords (also appear as type names in contexts
	// where scope resolution fires on them)
	"void", "char", "short", "int", "long", "float", "double",
	"signed", "unsigned", "_Bool", "_Complex", "_Imaginary",
	"size_t", "ssize_t", "ptrdiff_t", "wchar_t", "char16_t", "char32_t",
	"intptr_t", "uintptr_t", "intmax_t", "uintmax_t",
	"int8_t", "int16_t", "int32_t", "int64_t",
	"uint8_t", "uint16_t", "uint32_t", "uint64_t",
	"int_least8_t", "int_least16_t", "int_least32_t", "int_least64_t",
	"uint_least8_t", "uint_least16_t", "uint_least32_t", "uint_least64_t",
	"int_fast8_t", "int_fast16_t", "int_fast32_t", "int_fast64_t",
	"uint_fast8_t", "uint_fast16_t", "uint_fast32_t", "uint_fast64_t",
	"off_t", "time_t", "clock_t", "pid_t", "uid_t", "gid_t",
	"mode_t", "dev_t", "ino_t", "nlink_t", "blkcnt_t", "blksize_t",
	"FILE", "fpos_t", "va_list", "jmp_buf", "sig_atomic_t",
	"div_t", "ldiv_t",

	// Constant-like macros / keywords
	"NULL", "EOF", "true", "false", "bool",
	"stdin", "stdout", "stderr",
	"errno",

	// stdio.h
	"printf", "fprintf", "sprintf", "snprintf", "vprintf", "vfprintf",
	"vsprintf", "vsnprintf",
	"scanf", "fscanf", "sscanf", "vscanf", "vfscanf", "vsscanf",
	"fopen", "freopen", "fclose", "fflush", "setvbuf", "setbuf",
	"fread", "fwrite", "fseek", "ftell", "fgetpos", "fsetpos", "rewind",
	"feof", "ferror", "clearerr", "perror", "remove", "rename", "tmpfile",
	"fgetc", "getc", "getchar", "fputc", "putc", "putchar",
	"fgets", "fputs", "puts", "gets", "ungetc",

	// stdlib.h
	"malloc", "calloc", "realloc", "free",
	"abort", "exit", "_Exit", "quick_exit", "atexit", "at_quick_exit",
	"getenv", "setenv", "unsetenv", "system",
	"atoi", "atol", "atoll", "atof",
	"strtol", "strtoll", "strtoul", "strtoull", "strtof", "strtod", "strtold",
	"rand", "srand", "qsort", "bsearch",
	"abs", "labs", "llabs", "div", "ldiv", "lldiv",
	"mblen", "mbtowc", "wctomb", "mbstowcs", "wcstombs",

	// string.h
	"strcpy", "strncpy", "strcat", "strncat",
	"strcmp", "strncmp", "strcoll", "strxfrm",
	"strlen", "strchr", "strrchr", "strstr", "strpbrk",
	"strspn", "strcspn", "strtok", "strerror", "strdup", "strndup",
	"memcpy", "memmove", "memset", "memcmp", "memchr",

	// ctype.h
	"isalnum", "isalpha", "iscntrl", "isdigit", "isgraph",
	"islower", "isprint", "ispunct", "isspace", "isupper",
	"isxdigit", "isblank", "tolower", "toupper",

	// math.h
	"fabs", "fmod", "remainder", "remquo", "fma", "fmax", "fmin",
	"fdim", "nan",
	"exp", "exp2", "expm1", "log", "log2", "log10", "log1p", "logb",
	"pow", "sqrt", "cbrt", "hypot",
	"sin", "cos", "tan", "asin", "acos", "atan", "atan2",
	"sinh", "cosh", "tanh", "asinh", "acosh", "atanh",
	"ceil", "floor", "trunc", "round", "lround", "llround", "rint",
	"nearbyint", "ilogb", "frexp", "ldexp", "modf", "scalbn", "scalbln",
	"copysign", "nextafter", "nexttoward",
	"erf", "erfc", "lgamma", "tgamma",

	// assert.h / setjmp.h / signal.h
	"assert", "setjmp", "longjmp", "signal", "raise",

	// time.h
	"time", "clock", "difftime", "mktime", "gmtime", "localtime",
	"asctime", "ctime", "strftime", "strptime",

	// errno-common codes (appear as bare idents in switch/return)
	"EPERM", "ENOENT", "ESRCH", "EINTR", "EIO", "ENXIO", "E2BIG",
	"ENOEXEC", "EBADF", "ECHILD", "EAGAIN", "ENOMEM", "EACCES",
	"EFAULT", "EBUSY", "EEXIST", "EXDEV", "ENODEV", "ENOTDIR",
	"EISDIR", "EINVAL", "ENFILE", "EMFILE", "ENOTTY", "ETXTBSY",
	"EFBIG", "ENOSPC", "ESPIPE", "EROFS", "EMLINK", "EPIPE",
	"EDOM", "ERANGE", "EDEADLK", "ENAMETOOLONG", "ENOLCK",
	"ENOSYS", "ENOTEMPTY", "ELOOP", "EWOULDBLOCK", "ENOTSOCK",
	"EADDRINUSE", "EADDRNOTAVAIL", "ECONNREFUSED", "ECONNRESET",
	"ETIMEDOUT", "ENETDOWN", "ENETUNREACH", "EHOSTUNREACH",
)
