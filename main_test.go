package main

import "testing"

func TestFindBatchFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		// Basic batch flags
		{"simple -r", []string{"-r", "foo.go"}, 0},
		{"simple -s", []string{"-s", "pattern"}, 0},
		{"simple -e", []string{"-e", "foo.go"}, 0},

		// With persistent value flags
		{"--root before -r", []string{"--root", "/repo", "-r", "foo.go"}, 2},
		{"--root=val before -r", []string{"--root=/repo", "-r", "foo.go"}, 1},

		// With persistent boolean flags
		{"--verbose before -r", []string{"--verbose", "-r", "foo.go"}, 1},
		{"--verbose --root before -r", []string{"--verbose", "--root", "/repo", "-r", "foo.go"}, 3},

		// Subcommand before batch flag — stop scanning
		{"subcommand first", []string{"read", "-r", "foo.go"}, -1},

		// -- terminates flag parsing
		{"-- terminates", []string{"--", "-r", "foo.go"}, -1},

		// Unknown flags treated as value-bearing (safe default)
		{"unknown flag before -r", []string{"--unknown", "val", "-r", "foo.go"}, 2},

		// No batch flags
		{"no batch flags", []string{"read", "foo.go"}, -1},
		{"empty args", []string{}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findBatchFlag(tt.args)
			if got != tt.want {
				t.Errorf("findBatchFlag(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}
