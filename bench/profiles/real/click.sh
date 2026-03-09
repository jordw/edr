BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="pallets-click"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-click/src/click}"
SCOPE_DIR="."

API_FILE="formatting.py"
API_READ_SPEC="formatting.py:HelpFormatter"

READ_SYMBOL_FILE="_compat.py"
READ_SYMBOL_SPEC="_compat.py:open_stream"

REFS_PATTERN="open_stream"
REFS_GREP_ROOT="."
REFS_ARGS=("_compat.py" "open_stream")

SEARCH_PATTERN="allow_extra_args"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.py")
ORIENT_READ_FILES=(
    "formatting.py"
    "parser.py"
    "decorators.py"
    "types.py"
)

EDIT_FILE="formatting.py"
EDIT_OLD_TEXT=$'    # The arguments will fit to the right of the prefix.'
EDIT_NEW_TEXT=$'    # Arguments fit to the right of the prefix.'

WRITE_FILE="formatting.py"
WRITE_INSIDE="HelpFormatter"
WRITE_CONTENT=$'    def has_content(self) -> bool:\n        return bool(self.buffer)\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "formatting.py"
    "parser.py"
    "types.py"
)

EXPLORE_PATTERN="open_stream"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("_compat.py" "open_stream")
EXPLORE_NATIVE_READ_FILES=(
    "_compat.py"
    "types.py"
    "utils.py"
)
