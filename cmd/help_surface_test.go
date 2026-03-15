package cmd

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestHelpSurface(t *testing.T) {
	// Verify that edr --help lists exactly the expected public commands.
	expected := []string{"edit", "map", "read", "refs", "reindex", "rename", "search", "verify", "write"}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"--help"})
	rootCmd.Execute()

	output := buf.String()

	// Extract command names from the help output (lines like "  command   description")
	cmdRe := regexp.MustCompile(`(?m)^\s{2}(\w+)\s`)
	matches := cmdRe.FindAllStringSubmatch(output, -1)

	var found []string
	skip := map[string]bool{"edr": true, "version": true}
	for _, m := range matches {
		name := m[1]
		if skip[name] {
			continue
		}
		found = append(found, name)
	}
	sort.Strings(found)

	if strings.Join(found, " ") != strings.Join(expected, " ") {
		t.Errorf("help surface mismatch\n  got:  %v\n  want: %v", found, expected)
	}
}
