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
)
