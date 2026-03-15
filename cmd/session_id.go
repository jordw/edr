package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(sessionIDCmd)
}

var sessionIDCmd = &cobra.Command{
	Use:   "session-id",
	Short: "Print a short random session ID",
	Long:  `Prints a short random ID suitable for EDR_SESSION. Usage: export EDR_SESSION=$(edr session-id)`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		fmt.Println(hex.EncodeToString(b))
		return nil
	},
}
