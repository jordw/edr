BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="urfave-cli"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-urfave-cli}"
SCOPE_DIR="."

API_FILE="command.go"
API_READ_SPEC="command.go:Command"

READ_SYMBOL_FILE="help.go"
READ_SYMBOL_SPEC="help.go:ShowCommandHelp"

REFS_PATTERN="ShowCommandHelp"
REFS_GREP_ROOT="."
REFS_ARGS=("help.go" "ShowCommandHelp")

SEARCH_PATTERN="shellCompletion"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.go")
ORIENT_READ_FILES=(
    "command.go"
    "app.go"
    "help.go"
    "flag.go"
)

EDIT_FILE="command.go"
EDIT_OLD_TEXT='// HasName returns true if Command.Name matches given name'
EDIT_NEW_TEXT='// HasName reports whether Command.Name matches the given name'

WRITE_FILE="command.go"
WRITE_INSIDE="Command"
WRITE_CONTENT=$'func (cmd *Command) HasSubcommands() bool {\n\treturn len(cmd.Subcommands) > 0\n}\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "command.go"
    "app.go"
    "context.go"
)

EXPLORE_PATTERN="ShowCommandHelp"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("help.go" "ShowCommandHelp")
EXPLORE_NATIVE_READ_FILES=(
    "help.go"
    "app.go"
    "command.go"
)
