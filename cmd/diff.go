package cmd

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(diffPreviewCmd)
	rootCmd.AddCommand(diffPreviewSpanCmd)
}

// --- diff-preview (symbol) ---

var diffPreviewCmd = &cobra.Command{
	Use:   "diff-preview <file> <symbol>",
	Short: "Preview what replace-symbol would do as a unified diff",
	Long:  "Reads replacement code from stdin and shows the diff WITHOUT applying it.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)

		db, err := openAndEnsureIndex(cmd)
		if err != nil {
			return err
		}
		defer db.Close()

		ctx := context.Background()
		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		sym, err := db.GetSymbol(ctx, file, args[1])
		if err != nil {
			return err
		}

		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		diff, err := edit.DiffPreview(sym.File, sym.StartByte, sym.EndByte, replacement)
		if err != nil {
			return err
		}

		oldSize := int(sym.EndByte - sym.StartByte)
		newSize := len(replacement)

		output.Print(map[string]any{
			"file":     sym.File,
			"symbol":   args[1],
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
	Long:  "Reads replacement code from stdin and shows the diff WITHOUT applying it.",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		root := getRoot(cmd)

		file := args[0]
		if file[0] != '/' {
			file = root + "/" + file
		}

		var startByte, endByte uint32
		fmt.Sscanf(args[1], "%d", &startByte)
		fmt.Sscanf(args[2], "%d", &endByte)

		replacement, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading replacement from stdin: %w", err)
		}

		diff, err := edit.DiffPreview(file, startByte, endByte, replacement)
		if err != nil {
			return err
		}

		oldSize := int(endByte - startByte)
		newSize := len(replacement)

		output.Print(map[string]any{
			"file":     file,
			"diff":     diff,
			"old_size": oldSize,
			"new_size": newSize,
		})
		return nil
	},
}
