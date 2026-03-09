BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="redux-toolkit"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-redux-toolkit/packages/toolkit/src}"
SCOPE_DIR="."

API_FILE="utils.ts"
API_READ_SPEC="utils.ts:Tuple"

READ_SYMBOL_FILE="createReducer.ts"
READ_SYMBOL_SPEC="createReducer.ts:createReducer"

REFS_PATTERN="executeReducerBuilderCallback"
REFS_GREP_ROOT="."
REFS_ARGS=("mapBuilders.ts" "executeReducerBuilderCallback")

SEARCH_PATTERN="TaskAbortError"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.ts" "*.tsx")
ORIENT_READ_FILES=(
    "createReducer.ts"
    "mapBuilders.ts"
    "utils.ts"
    "listenerMiddleware/task.ts"
)

EDIT_FILE="createReducer.ts"
EDIT_OLD_TEXT=$'  // Ensure the initial state gets frozen either way (if draftable)'
EDIT_NEW_TEXT=$'  // Freeze the initial state either way (if draftable)'

WRITE_FILE="utils.ts"
WRITE_INSIDE="Tuple"
WRITE_CONTENT=$'  isEmpty() {\n    return this.length === 0\n  }\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "createReducer.ts"
    "mapBuilders.ts"
    "listenerMiddleware/task.ts"
)

EXPLORE_PATTERN="executeReducerBuilderCallback"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("mapBuilders.ts" "executeReducerBuilderCallback")
EXPLORE_NATIVE_READ_FILES=(
    "mapBuilders.ts"
    "createReducer.ts"
    "createSlice.ts"
)
