package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// extractFlags converts Cobra flags into the map[string]any format dispatch expects.
// Flag names are normalized from CLI hyphen convention (dry-run) to internal
// underscore convention (dry_run) so dispatch code only deals with one spelling.
func extractFlags(cmd *cobra.Command) map[string]any {
	flags := make(map[string]any)
	cmd.Flags().Visit(func(f *pflag.Flag) {
		key := strings.ReplaceAll(f.Name, "-", "_")
		switch f.Value.Type() {
		case "bool":
			v, _ := cmd.Flags().GetBool(f.Name)
			flags[key] = v
		case "int":
			v, _ := cmd.Flags().GetInt(f.Name)
			flags[key] = v
		case "string":
			v, _ := cmd.Flags().GetString(f.Name)
			flags[key] = v
		case "stringSlice":
			v, _ := cmd.Flags().GetStringSlice(f.Name)
			flags[key] = v
		}
	})
	return flags
}

// readStdinToFlags reads stdin and stores the content under the given flag key.
// Used by commands that read replacement/content from stdin.
func readStdinToFlags(flags map[string]any, key string) error {
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return fmt.Errorf("no input on stdin (pipe content)")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	flags[key] = string(data)
	return nil
}
