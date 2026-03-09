BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="urfave-cli"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-urfave-cli}"
SCOPE_DIR="."

API_FILE="command.go"
API_READ_SPEC="command.go:Command"

READ_SYMBOL_FILE="help.go"
READ_SYMBOL_SPEC="help.go:helpCommandAction"

REFS_PATTERN="helpCommandAction"
REFS_GREP_ROOT="."
REFS_ARGS=("help.go" "helpCommandAction")

SEARCH_PATTERN="shellCompletion"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.go")
ORIENT_READ_FILES=(
    "command.go"
    "command_run.go"
    "help.go"
    "flag.go"
)

EDIT_FILE="command_run.go"
EDIT_OLD_TEXT=$'tracef("using post-parse arguments %[1]q (cmd=%[2]q)", args, cmd.Name)'
EDIT_NEW_TEXT=$'tracef("using parsed arguments %[1]q (cmd=%[2]q)", args, cmd.Name)'

WRITE_FILE="command.go"
WRITE_INSIDE="Command"
WRITE_CONTENT=$'func (cmd *Command) HasSubcommands() bool {\n\treturn len(cmd.Commands) > 0\n}\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "command.go"
    "command_run.go"
    "command_parse.go"
)

EXPLORE_PATTERN="helpCommandAction"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("help.go" "helpCommandAction")
EXPLORE_NATIVE_READ_FILES=(
    "help.go"
    "command_run.go"
    "command_setup.go"
)
