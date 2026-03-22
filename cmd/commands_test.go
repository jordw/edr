package cmd

import "testing"

func TestClassifyErrorMsg(t *testing.T) {
	tests := []struct {
		msg  string
		want string
	}{
		{"symbol not found in file.go", "not_found"},
		{"'Foo' is ambiguous across multiple files", "ambiguous_symbol"},
		{"old_text ambiguous: matched 3 locations", "ambiguous_match"},
		{"no such file or directory", "file_not_found"},
		{"path outside repo root", "outside_repo"},
		{"hash mismatch: expected abc got def", "hash_mismatch"},
		{"--signatures and --skeleton are mutually exclusive", "invalid_mode"},
		{"write: --after and --append are mutually exclusive", "invalid_mode"},
		{"some unknown error", "command_error"},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			got := classifyErrorMsg(tt.msg)
			if got != tt.want {
				t.Errorf("classifyErrorMsg(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}
