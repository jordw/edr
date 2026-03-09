BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="vitess-vtgate"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-vitess/go/vt/vtgate}"
SCOPE_DIR="."

API_FILE="executor.go"
API_READ_SPEC="executor.go:Executor"

READ_SYMBOL_FILE="executor.go"
READ_SYMBOL_SPEC="executor.go:fetchOrCreatePlan"

REFS_PATTERN="fetchOrCreatePlan"
REFS_GREP_ROOT="."
REFS_ARGS=("executor.go" "fetchOrCreatePlan")

SEARCH_PATTERN="rollback"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.go")
ORIENT_READ_FILES=(
    "executor.go"
    "plan_execute.go"
    "planbuilder/builder.go"
    "planbuilder/planner.go"
)

EDIT_FILE="plan_execute.go"
EDIT_OLD_TEXT=$'// 5: Log and add statistics'
EDIT_NEW_TEXT=$'// 5: Record and add statistics'

WRITE_FILE="executor.go"
WRITE_INSIDE="Executor"
WRITE_CONTENT=$'func (e *Executor) HasPlanCache() bool {\n\treturn e.plans != nil\n}\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "executor.go"
    "plan_execute.go"
    "planbuilder/builder.go"
)

EXPLORE_PATTERN="fetchOrCreatePlan"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("executor.go" "fetchOrCreatePlan")
EXPLORE_NATIVE_READ_FILES=(
    "executor.go"
    "plan_execute.go"
)
