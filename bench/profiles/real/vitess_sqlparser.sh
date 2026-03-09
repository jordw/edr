BASE_DIR="${BASE_DIR:-/tmp}"
BENCH_NAME="vitess-sqlparser"
BENCH_ROOT="${BENCH_ROOT:-$BASE_DIR/edr-bench-vitess/go/vt/sqlparser}"
SCOPE_DIR="."

API_FILE="parser.go"
API_READ_SPEC="parser.go:Parser"

READ_SYMBOL_FILE="analyzer.go"
READ_SYMBOL_SPEC="analyzer.go:Preview"

REFS_PATTERN="literalToBindvar"
REFS_GREP_ROOT="."
REFS_ARGS=("normalizer.go" "literalToBindvar")

SEARCH_PATTERN="partialDDL"
SEARCH_ROOT="."
SEARCH_BUDGET=500

ORIENT_DIR="."
ORIENT_BUDGET=500
ORIENT_GLOBS=("*.go")
ORIENT_READ_FILES=(
    "parser.go"
    "ast.go"
    "analyzer.go"
    "ast_funcs.go"
)

EDIT_FILE="parser.go"
EDIT_OLD_TEXT=$'err := checkParseTreesError(tokenizer)\n\tif err != nil {\n\t\treturn nil, nil, err\n\t}'
EDIT_NEW_TEXT=$'parseTreeErr := checkParseTreesError(tokenizer)\n\tif parseTreeErr != nil {\n\t\treturn nil, nil, parseTreeErr\n\t}'

WRITE_FILE="parser.go"
WRITE_INSIDE="Parser"
WRITE_CONTENT=$'func (p *Parser) Version() string {\n\treturn p.version\n}\n'

MULTI_READ_BUDGET=500
MULTI_READ_FILES=(
    "parser.go"
    "ast.go"
    "analyzer.go"
)

EXPLORE_PATTERN="literalToBindvar"
EXPLORE_GREP_ROOT="."
EXPLORE_ARGS=("normalizer.go" "literalToBindvar")
EXPLORE_NATIVE_READ_FILES=(
    "normalizer.go"
    "normalizer_test.go"
)
