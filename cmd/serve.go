package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
	"github.com/spf13/cobra"
)

func init() {
	serveCmd.Flags().Bool("stop", false, "stop a running server")
	serveCmd.Flags().Bool("foreground", false, "run in foreground (used internally by daemonize)")
	serveCmd.Flags().MarkHidden("foreground")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start a persistent background server",
	Long: `Starts a background server that listens on a Unix socket (.edr/serve.sock).
CLI commands automatically proxy through the server for session benefits
(delta reads, body dedup, slim edits).

  edr serve          # daemonize, create socket, print PID
  edr serve --stop   # send shutdown to running server

The server accumulates session state across all CLI commands for its lifetime.
If no server is running, CLI commands fall back to ephemeral dispatch.`,
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
func handleRequest(ctx context.Context, db *index.DB, sess *session.Session, tc *trace.Collector, raw json.RawMessage) serveResponse {
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

	if envelope.Control != "" {
		return handleControl(envelope.RequestID, envelope.Control, db)
	}

	batchRaw := stripEnvelopeFields(raw)

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
	root := getRoot(cmd)
	edrDir := filepath.Join(root, ".edr")

	// --stop: shut down running server
	if stop, _ := cmd.Flags().GetBool("stop"); stop {
		return stopServe(edrDir)
	}

	// --foreground: run the server directly (used by daemonize)
	if fg, _ := cmd.Flags().GetBool("foreground"); fg {
		return runServeForeground(cmd, edrDir)
	}

	// Default: daemonize
	return daemonize(root, edrDir)
}

// daemonize re-execs the binary with --foreground and detaches from the terminal.
func daemonize(root, edrDir string) error {
	sockPath := filepath.Join(edrDir, "serve.sock")
	pidPath := filepath.Join(edrDir, "serve.pid")

	// Check if a server is already running
	if info, err := os.Stat(sockPath); err == nil && info.Mode()&os.ModeSocket != 0 {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			fmt.Fprintf(os.Stderr, "edr: server already running (socket %s)\n", sockPath)
			return nil
		}
		// Stale socket — clean up
		os.Remove(sockPath)
		os.Remove(pidPath)
	}

	os.MkdirAll(edrDir, 0755)

	// Open log file for the child's stderr
	logPath := filepath.Join(edrDir, "serve.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Build child command
	exe, err := os.Executable()
	if err != nil {
		logFile.Close()
		return fmt.Errorf("find executable: %w", err)
	}

	childArgs := []string{"serve", "--foreground"}
	if root != "." {
		childArgs = append(childArgs, "--root", root)
	}

	child := exec.Command(exe, childArgs...)
	child.Stdout = nil // no stdout needed
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := child.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start server: %w", err)
	}

	// Write PID file
	pid := child.Process.Pid
	os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644)

	// Detach: don't wait for the child
	child.Process.Release()
	logFile.Close()

	fmt.Fprintf(os.Stderr, "edr: server started (pid %d)\n", pid)
	return nil
}

// runServeForeground runs the socket server in the current process.
func runServeForeground(cmd *cobra.Command, edrDir string) error {
	db, err := openAndEnsureIndexQuiet(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	sess := session.New()
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

	var sessMu sync.Mutex
	sockPath := filepath.Join(edrDir, "serve.sock")

	fmt.Fprintf(os.Stderr, "edr: serve started (version %s+%s, socket %s)\n", Version, BuildHash, sockPath)

	// Run socket listener (blocking)
	runSocketListener(ctx, cancel, db, sess, tc, &sessMu, sockPath)

	// Clean up PID file on exit
	pidPath := filepath.Join(edrDir, "serve.pid")
	os.Remove(pidPath)

	fmt.Fprintf(os.Stderr, "edr: serve stopped\n")
	return nil
}

// stopServe sends SIGTERM to a running server.
func stopServe(edrDir string) error {
	pidPath := filepath.Join(edrDir, "serve.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return fmt.Errorf("no server running (no PID file at %s)", pidPath)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.Remove(pidPath)
		return fmt.Errorf("invalid PID file")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		os.Remove(pidPath)
		return fmt.Errorf("process %d not found", pid)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		os.Remove(pidPath)
		// Process is already dead — clean up socket too
		os.Remove(filepath.Join(edrDir, "serve.sock"))
		fmt.Fprintf(os.Stderr, "edr: server was not running (cleaned up stale files)\n")
		return nil
	}

	os.Remove(pidPath)
	fmt.Fprintf(os.Stderr, "edr: server stopped (pid %d)\n", pid)
	return nil
}

// runSocketListener listens on a Unix socket and processes NDJSON requests.
func runSocketListener(ctx context.Context, cancel context.CancelFunc, db *index.DB, sess *session.Session, tc *trace.Collector, sessMu *sync.Mutex, sockPath string) {
	// Clean up stale socket if it exists
	if _, err := os.Stat(sockPath); err == nil {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			fmt.Fprintf(os.Stderr, "edr: socket %s already in use by another server\n", sockPath)
			return
		}
		os.Remove(sockPath)
	}

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
