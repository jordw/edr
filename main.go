package main

import (
	"os"
	"slices"

	"github.com/jordw/edr/cmd"
)

func main() {
	// Allow batch operations without the "batch" subcommand:
	//   edr -r foo.go -s "pattern" → edr batch -r foo.go -s "pattern"
	if len(os.Args) > 1 && cmd.IsBatchFlag(os.Args[1]) {
		os.Args = slices.Insert(os.Args, 1, "batch")
	}
	cmd.Execute()
}
