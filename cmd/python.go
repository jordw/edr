package cmd

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

var installPythonCmd = &cobra.Command{
	Use:   "install-python",
	Short: "Install edr into Python's site-packages so 'import edr' works without boilerplate",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := ensurePythonModule()
		if err != nil {
			return err
		}
		// Find site-packages via python3 -c 'import site; ...'
		out, err := exec.Command("python3", "-c",
			"import site; print(site.getusersitepackages())").Output()
		if err != nil {
			return fmt.Errorf("could not find Python user site-packages: %w", err)
		}
		siteDir := strings.TrimSpace(string(out))
		if siteDir == "" {
			return fmt.Errorf("python3 returned empty site-packages path")
		}
		if err := os.MkdirAll(siteDir, 0o755); err != nil {
			return fmt.Errorf("create site-packages dir: %w", err)
		}
		pthPath := filepath.Join(siteDir, "edr.pth")
		if err := os.WriteFile(pthPath, []byte(dir+"\n"), 0o644); err != nil {
			return fmt.Errorf("write .pth file: %w", err)
		}
		fmt.Printf("Installed: %s\n", pthPath)
		fmt.Printf("Module at: %s/edr.py\n", dir)
		fmt.Println("You can now use: import edr")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(pythonPathCmd)
	rootCmd.AddCommand(installPythonCmd)
}
