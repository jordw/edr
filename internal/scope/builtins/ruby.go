package builtins

// Ruby Kernel methods, core classes, and common constants. Conservative
// v1 list — biased toward Kernel calls (puts/require/raise) and stdlib
// types that dominate real code. Without this, Ruby's
// BindUnresolved/"method_call" fallback misclassifies most lines that
// use the standard library.
var Ruby = from(
	// Kernel methods (called as bare idents on implicit self)
	"puts", "print", "p", "pp", "printf", "sprintf", "format",
	"gets", "getc", "readline", "readlines",
	"require", "require_relative", "load", "autoload",
	"raise", "fail", "catch", "throw", "rescue",
	"block_given?", "proc", "lambda", "yield",
	"attr_accessor", "attr_reader", "attr_writer",
	"private", "public", "protected", "module_function",
	"alias", "alias_method", "define_method", "method", "methods",
	"send", "__send__", "public_send", "respond_to?", "method_missing",
	"extend", "include", "prepend", "using",
	"Array", "Integer", "Float", "String", "Hash", "Rational", "Complex",
	"at_exit", "exit", "exit!", "abort", "sleep", "system", "spawn",
	"caller", "caller_locations", "binding", "eval", "instance_eval",
	"class_eval", "module_eval",
	"freeze", "frozen?", "dup", "clone", "tap", "then", "yield_self",
	"loop", "__method__", "__callee__", "__dir__",

	// Core classes
	"Object", "BasicObject", "Module", "Class", "Kernel",
	"Numeric", "Integer", "Float", "Rational", "Complex",
	"String", "Symbol", "Array", "Hash", "Range", "Regexp",
	"Proc", "Method", "UnboundMethod", "Enumerator", "Struct",
	"NilClass", "TrueClass", "FalseClass",
	"Comparable", "Enumerable", "Math", "ObjectSpace",
	"IO", "File", "Dir", "Pathname", "StringIO",
	"Time", "Date", "DateTime",
	"Thread", "Mutex", "ConditionVariable", "Queue", "Fiber",
	"Set", "BasicSocket", "Socket",

	// Exception hierarchy
	"Exception", "StandardError", "RuntimeError", "TypeError",
	"ArgumentError", "NameError", "NoMethodError", "LocalJumpError",
	"ZeroDivisionError", "FloatDomainError", "RangeError",
	"IndexError", "KeyError", "StopIteration", "FiberError",
	"IOError", "EOFError", "SystemCallError", "Errno",
	"NotImplementedError", "SecurityError", "EncodingError",
	"ScriptError", "SyntaxError", "LoadError",
	"SystemExit", "Interrupt", "SignalException", "SystemStackError",
	"ThreadError", "FrozenError", "RegexpError",

	// Constants / pseudo-keywords that appear as value idents
	"nil", "true", "false", "self",
	"STDIN", "STDOUT", "STDERR", "ARGV", "ARGF", "ENV",
	"RUBY_VERSION", "RUBY_PLATFORM", "RUBY_ENGINE", "RUBY_RELEASE_DATE",
	"__FILE__", "__LINE__", "__dir__", "__method__",
)
