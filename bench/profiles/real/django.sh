BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="django"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-django/django}"
SCOPE_DIR="."

API_FILE="db/models/query.py"
API_READ_SPEC="db/models/query.py:QuerySet"

READ_SYMBOL_FILE="db/models/sql/compiler.py"
READ_SYMBOL_SPEC="db/models/sql/compiler.py:as_sql"

REFS_PATTERN="get_fields"
REFS_GREP_ROOT="db"
REFS_ARGS=("db/models/options.py" "get_fields")

SEARCH_PATTERN="queryset"
SEARCH_ROOT="db"
SEARCH_BUDGET=500

ORIENT_DIR="db/models"
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.py")
ORIENT_READ_FILES=(
    "db/models/base.py"
    "db/models/query.py"
    "db/models/fields/__init__.py"
    "db/models/manager.py"
)

EDIT_FILE="db/models/query.py"
EDIT_OLD_TEXT='Return a new QuerySet instance with the args ANDed to the existing'
EDIT_NEW_TEXT='Return a new QuerySet instance with the args ANDed to the current'

WRITE_FILE="db/models/query.py"
WRITE_INSIDE="QuerySet"
WRITE_CONTENT=$'    def is_empty(self) -> bool:\n        """Return True if the queryset has no results."""\n        return not self.exists()\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "db/models/base.py"
    "db/models/query.py"
    "db/models/sql/compiler.py"
)

EXPLORE_PATTERN="get_fields"
EXPLORE_GREP_ROOT="db"
EXPLORE_ARGS=("db/models/options.py" "get_fields")
EXPLORE_NATIVE_READ_FILES=(
    "db/models/options.py"
    "db/models/base.py"
    "db/models/sql/compiler.py"
)
