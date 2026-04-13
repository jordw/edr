package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/index"
)

func TestReceiverPopulated(t *testing.T) {
	tmp := t.TempDir()

	os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main

type Server struct{}

func (s *Server) Start() {}
func (s *Server) Stop() {}
func freeFunction() {}

type Client struct{}

func (c *Client) Start() {}
`), 0644)

	os.WriteFile(filepath.Join(tmp, "main.py"), []byte(`class Server:
    def start(self):
        pass
    def stop(self):
        pass

def free_function():
    pass

class Client:
    def start(self):
        pass
`), 0644)

	os.WriteFile(filepath.Join(tmp, "main.rs"), []byte(`struct Server;

impl Server {
    fn start(&self) {}
    fn stop(&self) {}
}

fn free_function() {}

struct Client;

impl Client {
    fn start(&self) {}
}
`), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	ctx := context.Background()

	// Check all symbols per file, verifying receiver on each.
	// Use GetSymbolsByFile to avoid ambiguity when multiple symbols share a name.
	for _, tc := range []struct {
		file   string
		checks map[string]string // name → expected receiver ("" for non-methods)
	}{
		{"main.go", map[string]string{
			"Server": "", "Client": "", "freeFunction": "",
			"Stop": "Server",
		}},
		{"main.py", map[string]string{
			"Server": "", "Client": "", "free_function": "",
			"stop": "Server",
		}},
		{"main.rs", map[string]string{
			"Server": "", "Client": "", "free_function": "",
			"stop": "Server",
		}},
	} {
		syms, err := db.GetSymbolsByFile(ctx, tc.file)
		if err != nil {
			t.Fatalf("GetSymbolsByFile(%s): %v", tc.file, err)
		}
		for _, sym := range syms {
			want, ok := tc.checks[sym.Name]
			if !ok {
				continue
			}
			t.Run(tc.file+":"+sym.Name, func(t *testing.T) {
				if sym.Receiver != want {
					t.Errorf("Receiver = %q, want %q (type=%s)", sym.Receiver, want, sym.Type)
				}
			})
			delete(tc.checks, sym.Name)
		}
		for name := range tc.checks {
			t.Errorf("%s: symbol %q not found", tc.file, name)
		}
	}

	// Go-specific: verify both Start methods have different receivers.
	goSyms, _ := db.GetSymbolsByFile(ctx, "main.go")
	var goStarts []index.SymbolInfo
	for _, s := range goSyms {
		if s.Name == "Start" {
			goStarts = append(goStarts, s)
		}
	}
	if len(goStarts) != 2 {
		t.Fatalf("expected 2 Go Start methods, got %d", len(goStarts))
	}
	receivers := map[string]bool{goStarts[0].Receiver: true, goStarts[1].Receiver: true}
	if !receivers["Server"] || !receivers["Client"] {
		t.Errorf("Go Start receivers = %v, want Server and Client", receivers)
	}
}
