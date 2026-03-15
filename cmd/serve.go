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
	"time"

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


// sessionStore manages per-caller sessions. Each unique caller PID
// (the agent process, identified by the CLI PPID) gets its own session
// so that delta reads and body dedup are scoped correctly.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[int]*sessionEntry
}

type sessionEntry struct {
	sess     *session.Session
	lastUsed time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[int]*sessionEntry)}
}

// get returns the session for a caller PID, creating one if needed.
// A pid of 0 is used as the fallback for requests without caller_pid.
func (ss *sessionStore) get(pid int) *session.Session {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if e, ok := ss.sessions[pid]; ok {
		e.lastUsed = time.Now()
		return e.sess
	}
	s := session.New()
	ss.sessions[pid] = &sessionEntry{sess: s, lastUsed: time.Now()}
	return s
}

// gc removes sessions whose caller PID is no longer alive.
// Called periodically from the request path.
func (ss *sessionStore) gc() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	for pid := range ss.sessions {
		if pid == 0 {
			continue // fallback session, never GC
		}
		if !processAlive(pid) {
			delete(ss.sessions, pid)
		}
	}
}

// processAlive checks if a PID is still running by sending signal 0.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}


// handleRequest processes a single NDJSON request and returns a response.
func handleRequest(ctx context.Context, db *index.DB, ss *sessionStore, tc *trace.Collector, raw json.RawMessage) serveResponse {
	var envelope struct {
		RequestID string `json:"request_id"`
		Control   string `json:"control,omitempty"`
		CallerPID *int   `json:"caller_pid,omitempty"`
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

	callerPID := 0
	if envelope.CallerPID != nil {
		callerPID = *envelope.CallerPID
	}
	sess := ss.get(callerPID)

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
	} else {
		// No socket but PID file exists — server crashed before creating socket.
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
	// Defer PID cleanup so it runs on panic/signal exit, not just clean return.
	pidPath := filepath.Join(edrDir, "serve.pid")
	defer os.Remove(pidPath)

	db, err := openAndEnsureIndexQuiet(cmd)
	if err != nil {
		return err
	}
	defer db.Close()

	ss := newSessionStore()
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

	// Periodically GC sessions for dead caller PIDs
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ss.gc()
			case <-ctx.Done():
				return
			}
		}
	}()

	var sessMu sync.Mutex
	sockPath := filepath.Join(edrDir, "serve.sock")

	fmt.Fprintf(os.Stderr, "edr: serve started (version %s+%s, socket %s)\n", Version, BuildHash, sockPath)

	// Run socket listener (blocking)
	runSocketListener(ctx, cancel, db, ss, tc, &sessMu, sockPath)

	fmt.Fprintf(os.Stderr, "edr: serve stopped\n")
	return nil
}

// stopServe shuts down a running server via the socket, avoiding PID-based
// signaling which is unsafe (the PID may have been recycled by the OS).
func stopServe(edrDir string) error {
	sockPath := filepath.Join(edrDir, "serve.sock")
	pidPath := filepath.Join(edrDir, "serve.pid")

	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		// Socket unreachable — server is dead. Clean up stale files.
		removed := false
		if os.Remove(sockPath) == nil {
			removed = true
		}
		if os.Remove(pidPath) == nil {
			removed = true
		}
		if removed {
			fmt.Fprintf(os.Stderr, "edr: server was not running (cleaned up stale files)\n")
			return nil
		}
		return fmt.Errorf("no server running (no socket at %s)", sockPath)
	}

	// Send shutdown control message over the socket.
	fmt.Fprintf(conn, "{\"request_id\":\"stop\",\"control\":\"shutdown\"}\n")

	// Read the response to confirm shutdown was acknowledged.
	scanner := bufio.NewScanner(conn)
	scanner.Scan()
	conn.Close()

	// Read PID for the status message (best-effort).
	pidStr := ""
	if data, err := os.ReadFile(pidPath); err == nil {
		pidStr = strings.TrimSpace(string(data))
	}
	// The server defers its own PID file cleanup, but remove here too
	// in case the server exits before its defer runs.
	os.Remove(pidPath)

	if pidStr != "" {
		fmt.Fprintf(os.Stderr, "edr: server stopped (pid %s)\n", pidStr)
	} else {
		fmt.Fprintf(os.Stderr, "edr: server stopped\n")
	}
	return nil
}

// runSocketListener listens on a Unix socket and processes NDJSON requests.
func runSocketListener(ctx context.Context, cancel context.CancelFunc, db *index.DB, ss *sessionStore, tc *trace.Collector, sessMu *sync.Mutex, sockPath string) {
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

		handleSocketConn(ctx, cancel, db, ss, tc, sessMu, conn)
	}
}

// handleSocketConn processes all NDJSON lines from a single socket connection.
func handleSocketConn(ctx context.Context, cancel context.CancelFunc, db *index.DB, ss *sessionStore, tc *trace.Collector, sessMu *sync.Mutex, conn net.Conn) {
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
		resp := handleRequest(ctx, db, ss, tc, raw)
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
	delete(m, "caller_pid")
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
