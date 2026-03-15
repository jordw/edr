package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jordw/edr/internal/index"
)

// runServeSession sends lines to a serve loop and collects responses.
// Each line in input is sent as one NDJSON line. Returns parsed responses.
func runServeSession(t *testing.T, repoDir string, lines []string) []serveResponse {
	t.Helper()

	// Set up pipes for stdin/stdout
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	// Save and restore os.Stdin/os.Stdout
	origStdin := os.Stdin
	origStdout := os.Stdout
	os.Stdin = stdinR
	os.Stdout = stdoutW
	defer func() {
		os.Stdin = origStdin
		os.Stdout = origStdout
	}()

	// Open DB
	db, err := index.OpenDB(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = index.IndexRepo(context.Background(), db)

	// Write input and close stdin
	go func() {
		for _, line := range lines {
			stdinW.WriteString(line + "\n")
		}
		stdinW.Close()
	}()

	// Run serve loop in a goroutine
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- runServeLoop(db)
		stdoutW.Close()
	}()

	// Read responses
	var responses []serveResponse
	scanner := bufio.NewScanner(stdoutR)
	for scanner.Scan() {
		var resp serveResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("bad response JSON: %s", scanner.Text())
		}
		responses = append(responses, resp)
	}

	if err := <-doneCh; err != nil {
		t.Fatalf("serve loop error: %v", err)
	}
	db.Close()

	return responses
}

// runServeLoop is a testable version of the serve main loop.
func runServeLoop(db *index.DB) error {
	ss := newSessionStore()
	ctx := context.Background()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
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

		resp := handleRequest(ctx, db, ss, nil, raw)
		encoder.Encode(resp)

		// Check for shutdown
		var envelope struct {
			Control string `json:"control,omitempty"`
		}
		json.Unmarshal(raw, &envelope)
		if envelope.Control == "shutdown" {
			return nil
		}
	}
	return scanner.Err()
}

func setupServeRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n\nfunc keep() {}\n"), 0644)
	return tmp
}

func TestServe_PingPong(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","control":"ping"}`,
	})
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].RequestID != "1" || responses[0].Control != "pong" || !responses[0].OK {
		t.Errorf("unexpected ping response: %+v", responses[0])
	}
}

func TestServe_Status(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","control":"status"}`,
	})
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if !responses[0].OK || responses[0].Control != "status" {
		t.Errorf("unexpected status response: %+v", responses[0])
	}
	if responses[0].Result == nil {
		t.Error("status should have result")
	}
}

func TestServe_Shutdown(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","control":"ping"}`,
		`{"request_id":"2","control":"shutdown"}`,
		`{"request_id":"3","control":"ping"}`, // should not be processed
	})
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses (ping + shutdown), got %d", len(responses))
	}
	if responses[1].Control != "shutdown" {
		t.Errorf("expected shutdown response, got: %+v", responses[1])
	}
}

func TestServe_MissingRequestID(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"control":"ping"}`,
		`{"request_id":"2","control":"ping"}`,
	})
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
	if responses[0].OK || responses[0].Error == nil || responses[0].Error.Code != "missing_request_id" {
		t.Errorf("first response should be missing_request_id error: %+v", responses[0])
	}
	if !responses[1].OK {
		t.Errorf("second response should succeed: %+v", responses[1])
	}
}

func TestServe_MalformedLineRecovery(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`not valid json at all`,
		`{"request_id":"2","control":"ping"}`,
		`{"broken`,
		`{"request_id":"4","control":"ping"}`,
	})
	if len(responses) != 4 {
		t.Fatalf("expected 4 responses, got %d", len(responses))
	}
	// First: parse error
	if responses[0].OK || responses[0].Error == nil || responses[0].Error.Code != "parse_error" {
		t.Errorf("response[0] should be parse_error: %+v", responses[0])
	}
	// Second: valid ping
	if !responses[1].OK || responses[1].Control != "pong" {
		t.Errorf("response[1] should be pong: %+v", responses[1])
	}
	// Third: parse error
	if responses[2].OK || responses[2].Error == nil || responses[2].Error.Code != "parse_error" {
		t.Errorf("response[2] should be parse_error: %+v", responses[2])
	}
	// Fourth: valid ping — proves recovery worked
	if !responses[3].OK || responses[3].Control != "pong" {
		t.Errorf("response[3] should be pong after recovery: %+v", responses[3])
	}
}

func TestServe_BlankLinesIgnored(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		"",
		`{"request_id":"1","control":"ping"}`,
		"",
		"",
	})
	if len(responses) != 1 {
		t.Fatalf("expected 1 response (blank lines ignored), got %d", len(responses))
	}
	if responses[0].Control != "pong" {
		t.Errorf("expected pong: %+v", responses[0])
	}
}

func TestServe_ReadAndEdit(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","reads":[{"file":"main.go"}]}`,
		`{"request_id":"2","edits":[{"file":"main.go","old_text":"func hello()","new_text":"func world()"}]}`,
		`{"request_id":"3","reads":[{"file":"main.go","full":true}]}`,
	})
	if len(responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(responses))
	}
	if !responses[0].OK {
		t.Errorf("read should succeed: %+v", responses[0])
	}
	if !responses[1].OK {
		t.Errorf("edit should succeed: %+v", responses[1])
	}
	if !responses[2].OK {
		t.Errorf("second read should succeed: %+v", responses[2])
	}
	resultStr := string(responses[2].Result)
	if !strings.Contains(resultStr, "world") {
		t.Errorf("second read should contain edited content, got: %s", resultStr)
	}
}

func TestServe_SessionDeltaReads(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","reads":[{"file":"main.go"}]}`,
		`{"request_id":"2","reads":[{"file":"main.go"}]}`,
	})
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
	if !responses[1].OK {
		t.Errorf("second read should succeed: %+v", responses[1])
	}
	// Second read should detect unchanged content
	resultStr := string(responses[1].Result)
	if !strings.Contains(resultStr, "unchanged") {
		t.Errorf("second read should return unchanged delta, got: %s", resultStr)
	}
}

func TestServe_UnknownControl(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","control":"nonexistent"}`,
	})
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	if responses[0].OK || responses[0].Error == nil || responses[0].Error.Code != "unknown_control" {
		t.Errorf("should be unknown_control error: %+v", responses[0])
	}
}

func TestServe_NoEnvelopeWarnings(t *testing.T) {
	tmp := setupServeRepo(t)
	responses := runServeSession(t, tmp, []string{
		`{"request_id":"1","reads":[{"file":"main.go"}]}`,
	})
	if len(responses) != 1 {
		t.Fatalf("expected 1 response, got %d", len(responses))
	}
	resultStr := string(responses[0].Result)
	if strings.Contains(resultStr, `"request_id"`) && strings.Contains(resultStr, "unknown") {
		t.Errorf("request_id should not trigger unknown field warning: %s", resultStr)
	}
}

func TestStripEnvelopeFields(t *testing.T) {
	raw := json.RawMessage(`{"request_id":"1","control":"","reads":[{"file":"f.go"}],"verify":true}`)
	stripped := stripEnvelopeFields(raw)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["request_id"]; ok {
		t.Error("request_id should be stripped")
	}
	if _, ok := m["control"]; ok {
		t.Error("control should be stripped")
	}
	if _, ok := m["reads"]; !ok {
		t.Error("reads should be preserved")
	}
	if _, ok := m["verify"]; !ok {
		t.Error("verify should be preserved")
	}
}

// --- Socket tests ---

// startSocketServer starts a socket server on a temp socket path and returns
// the path and a cancel function. The server is ready when this returns.
func startSocketServer(t *testing.T, repoDir string) (string, context.CancelFunc) {
	t.Helper()

	db, err := index.OpenDB(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = index.IndexRepo(context.Background(), db)

	// Use /tmp for socket to avoid "bind: invalid argument" from long paths
	// (Unix socket paths have a ~104 char limit on macOS).
	sockDir, err := os.MkdirTemp("", "edr-sock-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "s.sock")

	ss := newSessionStore()
	ctx, cancel := context.WithCancel(context.Background())
	var sessMu sync.Mutex

	// Start socket listener in background
	ready := make(chan struct{})
	go func() {
		// Signal ready after listener starts by polling for socket file
		go func() {
			for i := 0; i < 100; i++ {
				if _, err := os.Stat(sockPath); err == nil {
					close(ready)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		runSocketListener(ctx, cancel, db, ss, nil, &sessMu, sockPath)
		db.Close()
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("socket server did not start in time")
	}

	t.Cleanup(func() {
		cancel()
		// Give the listener time to clean up
		time.Sleep(50 * time.Millisecond)
	})

	return sockPath, cancel
}

// sendSocketRequest sends a single NDJSON request over a Unix socket and returns the response.
func sendSocketRequest(t *testing.T, sockPath string, request string) serveResponse {
	t.Helper()

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()

	// Send request
	fmt.Fprintf(conn, "%s\n", request)
	if uc, ok := conn.(*net.UnixConn); ok {
		uc.CloseWrite()
	}

	// Read response
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	if !scanner.Scan() {
		t.Fatalf("no response from socket: %v", scanner.Err())
	}

	var resp serveResponse
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %s", scanner.Text())
	}
	return resp
}

func TestServe_Socket_PingPong(t *testing.T) {
	tmp := setupServeRepo(t)
	sockPath, _ := startSocketServer(t, tmp)

	resp := sendSocketRequest(t, sockPath, `{"request_id":"1","control":"ping"}`)
	if !resp.OK || resp.Control != "pong" {
		t.Errorf("expected pong, got: %+v", resp)
	}
}

func TestServe_Socket_ReadFile(t *testing.T) {
	tmp := setupServeRepo(t)
	sockPath, _ := startSocketServer(t, tmp)

	resp := sendSocketRequest(t, sockPath, `{"request_id":"1","reads":[{"file":"main.go"}]}`)
	if !resp.OK {
		t.Fatalf("read should succeed: %+v", resp)
	}
	if resp.Result == nil {
		t.Fatal("result should not be nil")
	}
	resultStr := string(resp.Result)
	if !strings.Contains(resultStr, "hello") {
		t.Errorf("result should contain file content, got: %s", resultStr)
	}
}

func TestServe_Socket_SessionPersistence(t *testing.T) {
	tmp := setupServeRepo(t)
	sockPath, _ := startSocketServer(t, tmp)

	// First read — establishes session state
	resp1 := sendSocketRequest(t, sockPath, `{"request_id":"1","reads":[{"file":"main.go"}]}`)
	if !resp1.OK {
		t.Fatalf("first read should succeed: %+v", resp1)
	}

	// Second read — should detect unchanged content (session persists across connections)
	resp2 := sendSocketRequest(t, sockPath, `{"request_id":"2","reads":[{"file":"main.go"}]}`)
	if !resp2.OK {
		t.Fatalf("second read should succeed: %+v", resp2)
	}
	resultStr := string(resp2.Result)
	if !strings.Contains(resultStr, "unchanged") {
		t.Errorf("second read should return unchanged delta, got: %s", resultStr)
	}
}

func TestServe_Socket_StaleSocketCleanup(t *testing.T) {
	tmp := setupServeRepo(t)

	// Create a temporary short socket path
	sockDir, err := os.MkdirTemp("", "edr-stale-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "s.sock")

	// Create a stale socket file (just a regular file, not a real socket)
	os.WriteFile(sockPath, []byte("stale"), 0644)

	// Start server — should clean up the stale file and create a real socket
	db, dbErr := index.OpenDB(tmp)
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	_, _, _ = index.IndexRepo(context.Background(), db)

	ss := newSessionStore()
	ctx, cancel := context.WithCancel(context.Background())
	var sessMu sync.Mutex
	defer cancel()

	ready := make(chan struct{})
	go func() {
		go func() {
			for i := 0; i < 100; i++ {
				if info, err := os.Stat(sockPath); err == nil && info.Mode()&os.ModeSocket != 0 {
					close(ready)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		runSocketListener(ctx, cancel, db, ss, nil, &sessMu, sockPath)
		db.Close()
	}()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("socket server did not start in time")
	}

	resp := sendSocketRequest(t, sockPath, `{"request_id":"1","control":"ping"}`)
	if !resp.OK || resp.Control != "pong" {
		t.Errorf("expected pong after stale cleanup, got: %+v", resp)
	}
}

func TestServe_Socket_Shutdown(t *testing.T) {
	tmp := setupServeRepo(t)
	sockPath, _ := startSocketServer(t, tmp)

	// Send shutdown
	resp := sendSocketRequest(t, sockPath, `{"request_id":"1","control":"shutdown"}`)
	if !resp.OK || resp.Control != "shutdown" {
		t.Errorf("expected shutdown response, got: %+v", resp)
	}

	// Wait a bit for cleanup
	time.Sleep(200 * time.Millisecond)

	// Socket should be cleaned up — new connection should fail
	conn, err := net.Dial("unix", sockPath)
	if err == nil {
		conn.Close()
		t.Error("socket should be cleaned up after shutdown")
	}
}
