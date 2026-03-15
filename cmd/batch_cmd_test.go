package cmd

import "testing"

func TestSilentErrorExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  silentError
		want int
	}{
		{"default is 1", silentError{}, 1},
		{"explicit 1", silentError{code: 1}, 1},
		{"verify failure is 2", silentError{code: 2}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSilentErrorMessage(t *testing.T) {
	// silentError should always return empty string — the structured JSON
	// output was already printed.
	e := silentError{code: 2}
	if msg := e.Error(); msg != "" {
		t.Errorf("Error() = %q, want empty", msg)
	}
}
