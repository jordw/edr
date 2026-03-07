package dispatch

import (
	"testing"
)

func TestIndentContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		indent  string
		want    string
	}{
		{
			name:    "unindented content gets indented",
			content: "Age int\nName string",
			indent:  "\t",
			want:    "\tAge int\n\tName string",
		},
		{
			name:    "pre-indented content not double-indented",
			content: "\tAge int\n\tName string",
			indent:  "\t",
			want:    "\tAge int\n\tName string",
		},
		{
			name:    "relative indentation preserved",
			content: "\tif true {\n\t\tfmt.Println()\n\t}",
			indent:  "\t\t",
			want:    "\t\tif true {\n\t\t\tfmt.Println()\n\t\t}",
		},
		{
			name:    "empty lines left as-is",
			content: "a\n\nb",
			indent:  "\t",
			want:    "\ta\n\n\tb",
		},
		{
			name:    "spaces preserved with tab indent",
			content: "    Age int\n    Name string",
			indent:  "\t",
			want:    "\tAge int\n\tName string",
		},
		{
			name:    "mixed relative indentation with spaces",
			content: "    def foo():\n        pass",
			indent:  "    ",
			want:    "    def foo():\n        pass",
		},
		{
			name:    "empty content",
			content: "",
			indent:  "\t",
			want:    "",
		},
		{
			name:    "single line no indent",
			content: "x",
			indent:  "\t",
			want:    "\tx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indentContent(tt.content, tt.indent)
			if got != tt.want {
				t.Errorf("indentContent(%q, %q)\n got: %q\nwant: %q", tt.content, tt.indent, got, tt.want)
			}
		})
	}
}
