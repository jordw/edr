package main

import (
	"os"
	"slices"
	"strings"

	"github.com/jordw/edr/cmd"
)

func main() {
	// Allow batch operations without the "batch" subcommand:
	//   edr -r foo.go -s "pattern"        → edr batch -r foo.go -s "pattern"
	//   edr --root /repo -e f.go --old .. → edr --root /repo batch -e f.go --old ..
	if idx := findBatchFlag(os.Args[1:]); idx >= 0 {
		os.Args = slices.Insert(os.Args, idx+1, "batch")
	}
	cmd.Execute()
}

// findBatchFlag returns the index (in args) of the first batch operation flag,
// skipping over persistent flags like --root <value>. Returns -1 if not found.
func findBatchFlag(args []string) int {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if cmd.IsBatchFlag(a) {
			return i
		}
		// Skip persistent flags that take a value (--flag value or --flag=value).
		if strings.HasPrefix(a, "--") && !strings.Contains(a, "=") {
			i++ // skip the value argument
			continue
		}
		// Any non-flag argument means we hit a subcommand — stop scanning.
		if !strings.HasPrefix(a, "-") {
			break
		}
	}
	return -1
}
