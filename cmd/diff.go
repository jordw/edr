package cmd

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(diffPreviewCmd)
	rootCmd.AddCommand(diffPreviewSpanCmd)
	diffPreviewCmd.Flags().String("replacement", "", "replacement content (if omitted, read from stdin)")
	diffPreviewSpanCmd.Flags().String("replacement", "", "replacement content (if omitted, read from stdin)")
}

// getReplacement returns replacement from --replacement flag, or stdin if flag is empty.
func getReplacement(cmd *cobra.Command) (string, error) {
	if s, _ := cmd.Flags().GetString("replacement"); s != "" {
		return s, nil
	}
	return readStdin()
}

// --- diff-preview (symbol) ---

var diffPreviewCmd = &cobra.Command{
	Use:   "diff-preview [file] <symbol>",
	Short: "Preview what replace-symbol would do as a unified diff",
	Long:  "Replacement from --replacement flag or stdin. Shows the diff WITHOUT applying it.",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		sym, err := resolveSymbol(ctx, db, args)
		if err != nil {
			return err
		}

		replacement, err := getReplacement(cmd)
		if err != nil {
			return err
		}

		diff, err := edit.DiffPreview(sym.File, sym.StartByte, sym.EndByte, replacement)
		if err != nil {
			return err
		}

		oldSize := int(sym.EndByte - sym.StartByte)
		newSize := len(replacement)

		output.Print(map[string]any{
			"file":     output.Rel(sym.File),
			"symbol":   sym.Name,
			"diff":     diff,
			"old_size": oldSize,
			"new_size": newSize,
		})
		return nil
	},
}

// --- diff-preview-span ---

var diffPreviewSpanCmd = &cobra.Command{
	Use:   "diff-preview-span <file> <start-byte> <end-byte>",
	Short: "Preview what replace-span would do as a unified diff",
	Long:  "Replacement from --replacement flag or stdin. Shows the diff WITHOUT applying it.",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := index.NormalizeRoot(getRoot(cmd))
		if err != nil {
			return err
		}
		file, err := index.ResolvePath(root, args[0])
		if err != nil {
			return err
		}

		var startByte, endByte uint32
		fmt.Sscanf(args[1], "%d", &startByte)
		fmt.Sscanf(args[2], "%d", &endByte)

		replacement, err := getReplacement(cmd)
		if err != nil {
			return err
		}

		diff, err := edit.DiffPreview(file, startByte, endByte, replacement)
		if err != nil {
			return err
		}

		oldSize := int(endByte - startByte)
		newSize := len(replacement)

		output.Print(map[string]any{
			"file":     output.Rel(file),
			"diff":     diff,
			"old_size": oldSize,
			"new_size": newSize,
		})
		return nil
	},
}
