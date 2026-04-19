package builtins

// Swift standard library globals: primitive/wrapper types, collections,
// core protocols, and top-level functions. Foundation types (URL, Data,
// Date) are NOT included — they require `import Foundation` and show up
// as cross-file refs in that pattern rather than as globals.
var Swift = from(
	// Value / object fundamentals
	"Int", "UInt", "Int8", "Int16", "Int32", "Int64",
	"UInt8", "UInt16", "UInt32", "UInt64",
	"Float", "Float32", "Float64", "Float80", "Double",
	"Bool", "Character", "String", "Substring", "StaticString",
	"Void", "Never", "Any", "AnyObject", "AnyHashable", "AnyClass",

	// Collections
	"Array", "ArraySlice", "ContiguousArray",
	"Dictionary", "Set",
	"Optional", "Range", "ClosedRange", "PartialRangeFrom",
	"PartialRangeUpTo", "PartialRangeThrough",
	"IndexingIterator", "AnyIterator", "AnySequence", "AnyCollection",
	"Sequence", "Collection", "BidirectionalCollection",
	"RandomAccessCollection", "MutableCollection", "RangeReplaceableCollection",
	"LazySequence", "LazyCollection", "LazyMapSequence", "LazyFilterSequence",
	"EnumeratedSequence", "EmptyCollection", "CollectionOfOne",

	// Error handling
	"Error", "Result",

	// Common protocols
	"Equatable", "Hashable", "Comparable", "Identifiable",
	"Codable", "Encodable", "Decodable",
	"Encoder", "Decoder", "CodingKey", "KeyedEncodingContainer", "KeyedDecodingContainer",
	"CustomStringConvertible", "CustomDebugStringConvertible",
	"LosslessStringConvertible",
	"ExpressibleByIntegerLiteral", "ExpressibleByFloatLiteral",
	"ExpressibleByStringLiteral", "ExpressibleByBooleanLiteral",
	"ExpressibleByArrayLiteral", "ExpressibleByDictionaryLiteral",
	"ExpressibleByNilLiteral",
	"Strideable", "Numeric", "SignedNumeric", "AdditiveArithmetic",
	"BinaryInteger", "SignedInteger", "UnsignedInteger",
	"BinaryFloatingPoint", "FloatingPoint",
	"IteratorProtocol", "Sequence",
	"Sendable", "Actor", "MainActor", "GlobalActor",

	// Concurrency
	"Task", "TaskGroup", "ThrowingTaskGroup", "AsyncSequence",
	"AsyncIterator", "AsyncStream", "AsyncThrowingStream",
	"CheckedContinuation", "UnsafeContinuation",
	"ContinuousClock", "SuspendingClock", "Clock", "Duration", "Instant",

	// Top-level functions
	"print", "debugPrint", "dump",
	"assert", "assertionFailure", "precondition", "preconditionFailure",
	"fatalError", "abs", "min", "max",
	"swap", "stride", "zip", "sequence",
	"type", "readLine",
	"withUnsafePointer", "withUnsafeMutablePointer",
	"withUnsafeBytes", "withUnsafeMutableBytes",

	// Constants / value keywords
	"nil", "true", "false", "self", "Self", "super",

	// Unsafe pointer family
	"UnsafePointer", "UnsafeMutablePointer",
	"UnsafeRawPointer", "UnsafeMutableRawPointer",
	"UnsafeBufferPointer", "UnsafeMutableBufferPointer",
	"UnsafeRawBufferPointer", "UnsafeMutableRawBufferPointer",
	"OpaquePointer", "AutoreleasingUnsafeMutablePointer",
)
