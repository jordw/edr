BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="rails-thor"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-thor/lib/thor}"
SCOPE_DIR="."

API_FILE="command.rb"
API_READ_SPEC="command.rb:Command"

READ_SYMBOL_FILE="runner.rb"
READ_SYMBOL_SPEC="runner.rb:install"

REFS_PATTERN="formatted_usage"
REFS_GREP_ROOT="."
REFS_ARGS=("command.rb" "formatted_usage")

SEARCH_PATTERN="class_option"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.rb")
ORIENT_READ_FILES=(
    "base.rb"
    "command.rb"
    "parser/options.rb"
    "parser/option.rb"
)

EDIT_FILE="parser/options.rb"
EDIT_OLD_TEXT=$'      @extra = []\n      @pile = args.dup'
EDIT_NEW_TEXT=$'      @extra = []\n      @pile = Array(args).dup'

WRITE_FILE="parser/options.rb"
WRITE_INSIDE="Options"
WRITE_CONTENT=$'    def empty?\n      @pile.empty?\n    end\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "base.rb"
    "runner.rb"
    "parser/options.rb"
)

EXPLORE_PATTERN="formatted_usage"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("command.rb" "formatted_usage")
EXPLORE_NATIVE_READ_FILES=(
    "command.rb"
    "group.rb"
    "runner.rb"
)
