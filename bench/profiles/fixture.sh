# Default profile using the bundled bench/testdata multi-language fixtures.
BENCH_NAME="fixture"
BENCH_ROOT="."
SCOPE_DIR="bench/testdata"

API_FILE="bench/testdata/lib/scheduler.py"
API_READ_SPEC="bench/testdata/lib/scheduler.py:Scheduler"

READ_SYMBOL_FILE="bench/testdata/lib/scheduler.py"
READ_SYMBOL_SPEC="bench/testdata/lib/scheduler.py:_execute_task"

REFS_PATTERN="_execute_task"
REFS_GREP_ROOT="bench/testdata"
REFS_ARGS=("_execute_task")

SEARCH_PATTERN="retry"
SEARCH_ROOT="bench/testdata"
SEARCH_BUDGET=500

ORIENT_DIR="bench/testdata"
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.py" "*.go" "*.java" "*.rb" "*.js" "*.tsx" "*.rs" "*.c")
ORIENT_READ_FILES=(
    "bench/testdata/lib/scheduler.py"
    "bench/testdata/lib/TaskProcessor.java"
    "bench/testdata/internal/worker.go"
    "bench/testdata/main.go"
)

EDIT_FILE="bench/testdata/lib/scheduler.py"
EDIT_OLD_TEXT="self._running = True"
EDIT_NEW_TEXT="self._running = False"

WRITE_FILE="bench/testdata/lib/scheduler.py"
WRITE_INSIDE="Scheduler"
WRITE_CONTENT=$'def drain(self, timeout: float = 5.0) -> int:\n    """Drain remaining tasks."""\n    return 0\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "bench/testdata/lib/scheduler.py"
    "bench/testdata/lib/TaskProcessor.java"
    "bench/testdata/internal/worker.go"
)

EXPLORE_PATTERN="_execute_task"
EXPLORE_GREP_ROOT="bench/testdata"
EXPLORE_ARGS=("_execute_task")
EXPLORE_NATIVE_READ_FILES=(
    "bench/testdata/lib/scheduler.py"
    "bench/testdata/main.go"
)
