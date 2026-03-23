package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/index"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// extractFlags converts Cobra flags into the map[string]any format dispatch expects.
// Flag names are normalized from CLI hyphen convention (dry-run) to internal
// underscore convention (dry_run) so dispatch code only deals with one spelling.
func extractFlags(cmd *cobra.Command) map[string]any {
	flags := make(map[string]any)
	cmd.Flags().Visit(func(f *pflag.Flag) {
		name := cmdspec.CanonicalFlagName(f.Name)
		key := strings.ReplaceAll(name, "-", "_")
		switch f.Value.Type() {
		case "bool":
			// Handle --no-<flag> negation: set the positive flag to false
			if strings.HasPrefix(key, "no_") {
				positiveKey := strings.TrimPrefix(key, "no_")
				flags[positiveKey] = false
				return
			}
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

// resolveAtFiles expands @path values in string flags to file contents.
// This allows shell-safe content passing: --old @/tmp/old.txt avoids shell expansion.
// Paths are validated to be within the repo root to prevent arbitrary file reads.
func resolveAtFiles(root string, flags map[string]any) error {
	for key, val := range flags {
		s, ok := val.(string)
		if !ok || !strings.HasPrefix(s, "@") {
			continue
		}
		path := s[1:]
		if root != "" {
			resolved, err := index.ResolvePath(root, path)
			if err != nil {
				return fmt.Errorf("--%s: %w", key, err)
			}
			path = resolved
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("--%s: reading %s: %w", key, path, err)
		}
		flags[key] = string(data)
	}
	return nil
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
