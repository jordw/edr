package builtins

// C# language builtins: primitive keywords, their System.* aliases,
// and the subset of BCL types (System, System.Collections.Generic,
// System.Threading.Tasks, System.Linq) that pervade real code.
// Not exhaustive — extend as dogfood surfaces real gaps.
var CSharp = from(
	// Primitive keywords (used as type names in declarations)
	"bool", "byte", "sbyte", "short", "ushort", "int", "uint",
	"long", "ulong", "float", "double", "decimal",
	"char", "string", "object", "void", "dynamic",
	"nint", "nuint",

	// Contextual / value keywords that show up as idents
	"null", "true", "false", "this", "base", "var", "value",
	"nameof", "typeof", "sizeof", "default", "await", "async",

	// System aliases for primitives
	"Boolean", "Byte", "SByte", "Int16", "UInt16", "Int32", "UInt32",
	"Int64", "UInt64", "Single", "Double", "Decimal",
	"Char", "String", "Object", "Void", "IntPtr", "UIntPtr",

	// Core System types
	"Array", "ArraySegment", "Attribute", "Buffer", "Console",
	"Convert", "DateTime", "DateTimeOffset", "DateOnly", "TimeOnly",
	"TimeSpan", "TimeZoneInfo", "Guid", "Enum", "ValueType",
	"Environment", "Exception", "GC", "Index", "Lazy", "Math",
	"MathF", "Memory", "ReadOnlyMemory", "Span", "ReadOnlySpan",
	"Nullable", "Random", "Range", "Type", "Tuple", "ValueTuple",
	"Uri", "UriBuilder", "Version", "WeakReference",
	"Progress", "Activator",

	// Exceptions
	"ApplicationException", "ArgumentException", "ArgumentNullException",
	"ArgumentOutOfRangeException", "ArithmeticException",
	"DivideByZeroException", "FormatException", "IndexOutOfRangeException",
	"InvalidCastException", "InvalidOperationException",
	"KeyNotFoundException", "NotImplementedException", "NotSupportedException",
	"NullReferenceException", "ObjectDisposedException",
	"OperationCanceledException", "OutOfMemoryException",
	"OverflowException", "StackOverflowException", "SystemException",
	"TimeoutException", "UnauthorizedAccessException",
	"IOException", "FileNotFoundException", "DirectoryNotFoundException",
	"PathTooLongException", "EndOfStreamException",
	"AggregateException", "TaskCanceledException",

	// Interfaces / delegates (common)
	"IDisposable", "IAsyncDisposable", "ICloneable", "IComparable",
	"IEquatable", "IFormattable", "IConvertible",
	"IComparer", "IEqualityComparer",
	"IEnumerable", "IEnumerator", "ICollection", "IList", "IDictionary",
	"IReadOnlyList", "IReadOnlyCollection", "IReadOnlyDictionary",
	"ISet", "IReadOnlySet",
	"Action", "Func", "Predicate", "Comparison", "Converter",
	"EventHandler", "EventArgs",
	"IQueryable", "IQueryProvider", "IOrderedQueryable",
	"IObservable", "IObserver",

	// System.Collections.Generic
	"List", "Dictionary", "HashSet", "SortedSet", "SortedDictionary",
	"SortedList", "Queue", "Stack", "LinkedList", "LinkedListNode",
	"KeyValuePair",
	"Enumerable", "EnumerableExtensions",

	// System.Threading + Tasks
	"Task", "ValueTask", "TaskCompletionSource", "TaskFactory",
	"TaskScheduler", "CancellationToken", "CancellationTokenSource",
	"Thread", "ThreadStart", "ThreadPool", "Monitor", "Mutex",
	"Semaphore", "SemaphoreSlim", "Interlocked", "Volatile",
	"ManualResetEvent", "AutoResetEvent", "ManualResetEventSlim",
	"CountdownEvent", "Barrier", "SpinLock", "SpinWait",
	"ReaderWriterLockSlim", "Timer",

	// Diagnostics / logging names that show up in tests
	"Debug", "Trace", "Stopwatch", "Process",

	// Common attributes (bare class name inside [...])
	"Obsolete", "Flags", "Serializable", "AttributeUsage",
	"CallerMemberName", "CallerFilePath", "CallerLineNumber",

	// Namespace-root identifiers that appear bare. Roslyn dogfood surfaces
	// these at high volume; they never resolve to a scope decl so should
	// bind as builtins rather than missing_import.
	"System", "Microsoft", "Linq",

	// System.Collections.Immutable (heavy use in Roslyn, compilers, ASP.NET
	// Core). ImmutableArray and friends alone show up 4.7K+ times in Roslyn.
	"ImmutableArray", "ImmutableList", "ImmutableDictionary",
	"ImmutableHashSet", "ImmutableSortedSet", "ImmutableSortedDictionary",
	"ImmutableQueue", "ImmutableStack", "ImmutableInterlocked",
	"IImmutableList", "IImmutableDictionary", "IImmutableSet",
	"IImmutableQueue", "IImmutableStack",

	// Concurrent collections (System.Collections.Concurrent)
	"ConcurrentBag", "ConcurrentDictionary", "ConcurrentQueue",
	"ConcurrentStack", "BlockingCollection",

	// Common System.IO
	"File", "Directory", "Path", "Stream", "FileStream", "MemoryStream",
	"TextReader", "TextWriter", "StreamReader", "StreamWriter",
	"BinaryReader", "BinaryWriter", "FileInfo", "DirectoryInfo",
	"FileMode", "FileAccess", "FileShare", "FileOptions",

	// Common System.Text
	"StringBuilder", "Encoding", "Decoder", "Encoder",
	"UTF8Encoding", "UnicodeEncoding", "ASCIIEncoding",

	// System.Text.RegularExpressions
	"Regex", "Match", "MatchCollection", "Group", "GroupCollection",
	"Capture", "RegexOptions",

	// Reflection
	"Assembly", "MethodInfo", "FieldInfo", "PropertyInfo",
	"ConstructorInfo", "MemberInfo", "ParameterInfo", "EventInfo",
	"BindingFlags", "CustomAttributeData",

	// Numerics
	"BigInteger", "Complex", "Vector", "Vector2", "Vector3", "Vector4",
	"Matrix4x4", "Quaternion",

	// Span / buffer ecosystem
	"Slice", "ArrayPool", "MemoryPool", "SequencePosition",
	"ReadOnlySequence",

	// Nullable reference-type contextual keywords that appear as idents in
	// pragmas and annotations (the `#nullable disable` directive is now
	// scanned away by the preprocessor handler, but `nullable` and
	// `disable` can also appear in `#pragma warning disable` etc.)
	"nullable", "disable", "enable", "restore", "warnings", "annotations",

	// Pattern-matching contextual keywords (`is T or U`, `is not null`).
	// These are never refs; binding them as builtins avoids missing_import
	// noise.
	"or", "and", "not",

	// Property-accessor contextual idents. `int value;` as a field name is
	// legit, but `value` is also the implicit setter param. Treating these
	// as builtins means: real field decls still work (declContext wins over
	// ref resolution), but `get`/`set`/`init`/`value` in accessor bodies —
	// which fall through as refs — resolve as "builtin" rather than
	// missing_import. Covers ~15K cases in Roslyn.
	"get", "set", "init",

	// LINQ query-expression contextual keywords (`from`, `where`, `select`,
	// `orderby`, `group`, `into`, `let`, `join`, `on`, `equals`, `by`,
	// `ascending`, `descending`).
	"from", "where", "select", "orderby", "group", "into", "let",
	"join", "on", "equals", "by", "ascending", "descending",
)
