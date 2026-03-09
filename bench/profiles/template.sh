# Copy this file, update the values, then run:
#   bash bench/native_comparison.sh /path/to/profile.sh

BENCH_NAME="my-benchmark"
BENCH_ROOT="/absolute/path/to/repo"
SCOPE_DIR="relative/subdir/to/benchmark"

# Full-file native read vs edr read --signatures.
API_FILE="relative/path/to/file.go"
API_READ_SPEC="relative/path/to/file.go:TypeName"

# Full-file native read vs edr read symbol.
READ_SYMBOL_FILE="relative/path/to/file.go"
READ_SYMBOL_SPEC="relative/path/to/file.go:FunctionName"

# grep + follow-up file reads vs edr refs.
REFS_PATTERN="FunctionName"
REFS_GREP_ROOT="relative/subdir/to/search"
# Use either ("SymbolName") or ("relative/path/to/file.go" "SymbolName")
REFS_ARGS=("SymbolName")

# grep -C3 vs edr search --text --context 3.
SEARCH_PATTERN="retry"
SEARCH_ROOT="relative/subdir/to/search"
SEARCH_BUDGET=500

# glob + a few full-file reads vs edr map.
ORIENT_DIR="relative/subdir/to/map"
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.go")
ORIENT_READ_FILES=(
    "relative/path/to/file1.go"
    "relative/path/to/file2.go"
    "relative/path/to/file3.go"
)

# read + edit + verify/read vs edr edit --dry-run.
EDIT_FILE="relative/path/to/file.go"
EDIT_OLD_TEXT="old text"
EDIT_NEW_TEXT="new text"

# Optional: skip this scenario by leaving these unset.
WRITE_FILE="relative/path/to/file.go"
WRITE_INSIDE="TypeName"
WRITE_CONTENT=$'func (t *TypeName) NewMethod() error {\n    return nil\n}\n'

# Separate full-file reads vs one batched edr read.
MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "relative/path/to/file1.go"
    "relative/path/to/file2.go"
    "relative/path/to/file3.go"
)

# grep + a couple of reads vs edr explore --body --callers --deps.
EXPLORE_PATTERN="FunctionName"
EXPLORE_GREP_ROOT="relative/subdir/to/search"
EXPLORE_ARGS=("SymbolName")
EXPLORE_NATIVE_READ_FILES=(
    "relative/path/to/file1.go"
    "relative/path/to/file2.go"
)
