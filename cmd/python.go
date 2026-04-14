package cmd

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed python_edr.py
var pythonModule string

// pythonInstallDir returns ~/.edr/python — where edr.py is extracted and kept.
func pythonInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".edr", "python"), nil
}

// ensurePythonModule writes the embedded edr.py to disk if missing or stale.
func ensurePythonModule() (string, error) {
	dir, err := pythonInstallDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "edr.py")
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == pythonModule {
		return dir, nil
	}
	if err := os.WriteFile(path, []byte(pythonModule), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

var pythonPathCmd = &cobra.Command{
	Use:   "python-path",
	Short: "Print the directory containing the edr.py Python module",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := ensurePythonModule()
		if err != nil {
			return err
		}
		fmt.Println(dir)
		return nil
	},
}

func init() { rootCmd.AddCommand(pythonPathCmd) }
