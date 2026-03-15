package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
	"github.com/spf13/cobra"
)

func init() {
	serveCmd.Flags().Bool("stdio", true, "use stdin/stdout for NDJSON transport (default)")
	serveCmd.Flags().String("socket", "", "Unix socket path (enables socket listener)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start a persistent stdio server",
	Long: `Starts a persistent server that reads NDJSON requests from stdin and
writes NDJSON responses to stdout. Sessions are connection-scoped (process
lifetime), eliminating the need for file-based persistence.

With --socket, also listens on a Unix socket so CLI commands can proxy
through and benefit from session optimizations (delta reads, body dedup,
slim edits).

Each request is a JSON object with a required "request_id" field.
Requests can include an optional "control" field for protocol messages
(ping, status, shutdown), or batch operation fields (reads, queries,
edits, writes, renames, verify, init).

Example:
  echo '{"request_id":"1","control":"ping"}' | edr serve --stdio
  edr serve --socket .edr/serve.sock`,
	RunE: runServe,
}

// serveRequest extends doParams with protocol envelope fields.
type serveRequest struct {
	RequestID string `json:"request_id"`
	Control   string `json:"control,omitempty"`
	doParams
}

// serveResponse is the protocol envelope for all responses.
type serveResponse struct {
	RequestID string          `json:"request_id"`
	OK        bool            `json:"ok"`
	Error     *serveError     `json:"error,omitempty"`
	Control   string          `json:"control,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

type serveError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// handleRequest processes a single NDJSON request and returns a response.
// This is the shared logic used by both stdio and socket transports.
func handleRequest(ctx context.Context, db *index.DB, sess *session.Session, tc *trace.Collector, raw json.RawMessage) serveResponse {
	// Extract request_id and control before full parse
	var envelope struct {
		RequestID string `json:"request_id"`
		Control   string `json:"control,omitempty"`
	}
	json.Unmarshal(raw, &envelope)

	if envelope.RequestID == "" {
		return serveResponse{
			OK:    false,
			Error: &serveError{Code: "missing_request_id", Message: "request_id is required"},
		}
	}

	// Handle control messages
	if envelope.Control != "" {
		return handleControl(envelope.RequestID, envelope.Control, db)
	}

	// Strip envelope fields before passing to handleDo
	batchRaw := stripEnvelopeFields(raw)

	// Route to handleDo for batch operations
	text, err := handleDo(ctx, db, sess, tc, batchRaw)
	if err != nil {
		return serveResponse{
			RequestID: envelope.RequestID,
			OK:        false,
			Error:     &serveError{Code: "internal_error", Message: err.Error()},
		}
	}

	return serveResponse{
		RequestID: envelope.RequestID,
		OK:        true,
		Result:    json.RawMessage(text),
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	db, err := openAndEnsureIndexQuiet(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	sess := session.New()
	edrDir := filepath.Join(db.Root(), ".edr")
	tc := trace.NewCollector(edrDir, Version+"+"+BuildHash)
	if tc != nil {
		defer tc.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGTERM/SIGINT for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		cancel()
	}()

	// Session mutex: socket and stdio share the same session, which isn't concurrent-safe
	var sessMu sync.Mutex

	// Start socket listener if --socket is set
	sockPath, _ := cmd.Flags().GetString("socket")
	if sockPath != "" {
		if !filepath.IsAbs(sockPath) {
			sockPath = filepath.Join(db.Root(), sockPath)
		}
		go runSocketListener(ctx, cancel, db, sess, tc, &sessMu, sockPath)
	}

	fmt.Fprintf(os.Stderr, "edr: serve started (version %s+%s)\n", Version, BuildHash)
	if sockPath != "" {
		fmt.Fprintf(os.Stderr, "edr: socket listener at %s\n", sockPath)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024) // up to 10MB per line
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		// Check for context cancellation (shutdown via signal or socket)
		if ctx.Err() != nil {
			break
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue // skip blank lines
		}

		var raw json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			resp := serveResponse{
				RequestID: "",
				OK:        false,
				Error:     &serveError{Code: "parse_error", Message: err.Error()},
			}
			encoder.Encode(resp)
			continue
		}

		// Check for shutdown control before locking
		var envelope struct {
			Control string `json:"control,omitempty"`
		}
		json.Unmarshal(raw, &envelope)

		sessMu.Lock()
		resp := handleRequest(ctx, db, sess, tc, raw)
		sessMu.Unlock()

		encoder.Encode(resp)

		if envelope.Control == "shutdown" {
			fmt.Fprintf(os.Stderr, "edr: serve stopped (shutdown requested)\n")
			cancel()
			return nil
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "edr: serve stopped (read error: %v)\n", err)
		return err
	}
	fmt.Fprintf(os.Stderr, "edr: serve stopped (stdin closed)\n")
	return nil
}

// runSocketListener listens on a Unix socket and processes NDJSON requests.
// Each connection is handled sequentially (one at a time). The session
// accumulates across all connections for the lifetime of the server.
func runSocketListener(ctx context.Context, cancel context.CancelFunc, db *index.DB, sess *session.Session, tc *trace.Collector, sessMu *sync.Mutex, sockPath string) {
	// Clean up stale socket if it exists
	if _, err := os.Stat(sockPath); err == nil {
		// Try connecting to see if it's live
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			fmt.Fprintf(os.Stderr, "edr: socket %s already in use by another server\n", sockPath)
			return
		}
		// Stale socket — remove it
		os.Remove(sockPath)
	}

	// Ensure parent directory exists
	os.MkdirAll(filepath.Dir(sockPath), 0755)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "edr: socket listen error: %v\n", err)
		return
	}
	defer func() {
		listener.Close()
		os.Remove(sockPath)
	}()

	// Close listener on context cancellation
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return // clean shutdown
			}
			fmt.Fprintf(os.Stderr, "edr: socket accept error: %v\n", err)
			continue
		}

		handleSocketConn(ctx, cancel, db, sess, tc, sessMu, conn)
	}
}

// handleSocketConn processes all NDJSON lines from a single socket connection.
func handleSocketConn(ctx context.Context, cancel context.CancelFunc, db *index.DB, sess *session.Session, tc *trace.Collector, sessMu *sync.Mutex, conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	encoder := json.NewEncoder(conn)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			resp := serveResponse{
				OK:    false,
				Error: &serveError{Code: "parse_error", Message: err.Error()},
			}
			encoder.Encode(resp)
			continue
		}

		// Check for shutdown control
		var envelope struct {
			Control string `json:"control,omitempty"`
		}
		json.Unmarshal(raw, &envelope)

		sessMu.Lock()
		resp := handleRequest(ctx, db, sess, tc, raw)
		sessMu.Unlock()

		encoder.Encode(resp)

		if envelope.Control == "shutdown" {
			fmt.Fprintf(os.Stderr, "edr: serve stopped (shutdown via socket)\n")
			cancel()
			return
		}
	}
}

// stripEnvelopeFields removes request_id and control from a raw JSON message
// so that handleDo's unknown-field detection doesn't warn about them.
func stripEnvelopeFields(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	delete(m, "request_id")
	delete(m, "control")
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return json.RawMessage(out)
}

func handleControl(requestID, control string, db interface{ Root() string }) serveResponse {
	switch control {
	case "ping":
		return serveResponse{
			RequestID: requestID,
			OK:        true,
			Control:   "pong",
		}
	case "status":
		result, _ := json.Marshal(map[string]any{
			"version": Version + "+" + BuildHash,
			"root":    db.Root(),
		})
		return serveResponse{
			RequestID: requestID,
			OK:        true,
			Control:   "status",
			Result:    json.RawMessage(result),
		}
	case "shutdown":
		return serveResponse{
			RequestID: requestID,
			OK:        true,
			Control:   "shutdown",
		}
	default:
		return serveResponse{
			RequestID: requestID,
			OK:        false,
			Error:     &serveError{Code: "unknown_control", Message: fmt.Sprintf("unknown control message: %q", control)},
		}
	}
}
