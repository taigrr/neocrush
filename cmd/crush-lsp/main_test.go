package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"educationalsp/internal/session"
	"educationalsp/rpc"
)

func TestIdentifyClient(t *testing.T) {
	d := &Daemon{
		logger:  log.New(io.Discard, "", 0),
		clients: make(map[string]net.Conn),
	}

	tests := []struct {
		name     string
		clientID string
		expected string
	}{
		{"Neovim full name", "Neovim", "neovim"},
		{"Neovim with version", "Neovim 0.10.0", "neovim"},
		{"nvim lowercase", "nvim", "neovim"},
		{"vim lowercase", "vim", "neovim"},
		{"Vim uppercase", "Vim", "neovim"},
		{"Crush full name", "Crush", "crush"},
		{"crush lowercase", "crush", "crush"},
		{"Crush with version", "Crush 1.0.0", "crush"},
		{"Unknown client", "vscode", "vscode"},
		{"Empty client", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := createInitializeMessage(tt.clientID)
			result := d.identifyClient([]byte(msg))
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestIdentifyClient_NonInitialize(t *testing.T) {
	d := &Daemon{
		logger:  log.New(io.Discard, "", 0),
		clients: make(map[string]net.Conn),
	}

	// Test with non-initialize message
	msg := rpc.EncodeMessage(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "textDocument/didOpen",
		"params":  map[string]interface{}{},
	})

	result := d.identifyClient([]byte(msg))
	if result != "" {
		t.Errorf("Expected empty string for non-initialize message, got %q", result)
	}
}

func createInitializeMessage(clientName string) string {
	params := map[string]interface{}{
		"capabilities": map[string]interface{}{},
	}
	if clientName != "" {
		params["clientInfo"] = map[string]interface{}{
			"name": clientName,
		}
	}

	return rpc.EncodeMessage(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  params,
	})
}

func TestDaemonClientRouting(t *testing.T) {
	// Create a test workspace
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	// Create session
	sess, err := mgr.CreateSession(tmpDir, os.Getpid())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Ensure socket directory exists
	socketDir := filepath.Dir(sess.SocketPath)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("Failed to create socket directory: %v", err)
	}

	// Start daemon listener
	listener, err := net.Listen("unix", sess.SocketPath)
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()
	defer os.Remove(sess.SocketPath)

	daemon := &Daemon{
		logger:   log.New(io.Discard, "", 0),
		listener: listener,
		clients:  make(map[string]net.Conn),
	}

	// Start daemon in background
	go daemon.run()

	// Connect Neovim client
	nvimConn, err := net.DialTimeout("unix", sess.SocketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect neovim client: %v", err)
	}
	defer nvimConn.Close()

	// Send initialize from Neovim
	nvimInit := createInitializeMessage("Neovim 0.10")
	if _, err := nvimConn.Write([]byte(nvimInit)); err != nil {
		t.Fatalf("Failed to send neovim init: %v", err)
	}

	// Read initialize response from neovim connection
	nvimConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	nvimScanner := bufio.NewScanner(nvimConn)
	nvimScanner.Split(rpc.Split)
	nvimScanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	if !nvimScanner.Scan() {
		t.Fatal("Failed to read init response for neovim")
	}

	// Wait for daemon to process
	time.Sleep(100 * time.Millisecond)

	// Verify neovim client is registered
	daemon.mu.RLock()
	_, hasNeovim := daemon.clients["neovim"]
	daemon.mu.RUnlock()

	if !hasNeovim {
		t.Fatal("Neovim client should be registered")
	}

	// Connect Crush client
	crushConn, err := net.DialTimeout("unix", sess.SocketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect crush client: %v", err)
	}
	defer crushConn.Close()

	// Send initialize from Crush
	crushInit := createInitializeMessage("Crush")
	if _, err := crushConn.Write([]byte(crushInit)); err != nil {
		t.Fatalf("Failed to send crush init: %v", err)
	}

	// Read initialize response from crush connection
	crushConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	crushScanner := bufio.NewScanner(crushConn)
	crushScanner.Split(rpc.Split)
	crushScanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	if !crushScanner.Scan() {
		t.Fatal("Failed to read init response for crush")
	}

	// Wait for daemon to process
	time.Sleep(100 * time.Millisecond)

	// Verify crush client is registered
	daemon.mu.RLock()
	_, hasCrush := daemon.clients["crush"]
	daemon.mu.RUnlock()

	if !hasCrush {
		t.Fatal("Crush client should be registered")
	}

	// Test message forwarding: send from Neovim, should arrive at Crush
	testMsg := rpc.EncodeMessage(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "textDocument/didOpen",
		"params": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"uri": "file:///test.go",
			},
		},
	})

	if _, err := nvimConn.Write([]byte(testMsg)); err != nil {
		t.Fatalf("Failed to send test message: %v", err)
	}

	// Read from Crush connection (reuse the scanner)
	crushConn.SetReadDeadline(time.Now().Add(2 * time.Second))

	if !crushScanner.Scan() {
		if crushScanner.Err() != nil {
			t.Fatalf("Failed to read from crush: %v", crushScanner.Err())
		}
		t.Fatal("No message received at crush client")
	}

	received := crushScanner.Bytes()
	if !strings.Contains(string(received), "textDocument/didOpen") {
		t.Errorf("Expected didOpen message, got: %s", string(received))
	}
}

func TestDaemonClientDisconnect(t *testing.T) {
	// Create a test workspace
	tmpDir := t.TempDir()
	mgr := session.NewManager()

	// Create session
	sess, err := mgr.CreateSession(tmpDir, os.Getpid())
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Ensure socket directory exists
	socketDir := filepath.Dir(sess.SocketPath)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("Failed to create socket directory: %v", err)
	}

	// Start daemon listener
	listener, err := net.Listen("unix", sess.SocketPath)
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer os.Remove(sess.SocketPath)

	daemon := &Daemon{
		logger:   log.New(io.Discard, "", 0),
		listener: listener,
		clients:  make(map[string]net.Conn),
	}

	// Start daemon in background
	go daemon.run()

	// Connect client
	conn, err := net.DialTimeout("unix", sess.SocketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Send initialize
	initMsg := createInitializeMessage("Neovim")
	if _, err := conn.Write([]byte(initMsg)); err != nil {
		t.Fatalf("Failed to send init: %v", err)
	}

	// Wait for registration
	time.Sleep(100 * time.Millisecond)

	daemon.mu.RLock()
	clientCount := len(daemon.clients)
	daemon.mu.RUnlock()

	if clientCount != 1 {
		t.Fatalf("Expected 1 client, got %d", clientCount)
	}

	// Disconnect
	conn.Close()

	// Wait for cleanup
	time.Sleep(200 * time.Millisecond)

	daemon.mu.RLock()
	clientCount = len(daemon.clients)
	daemon.mu.RUnlock()

	if clientCount != 0 {
		t.Fatalf("Expected 0 clients after disconnect, got %d", clientCount)
	}
}

func TestContainsLower(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"Neovim", "vim", true},
		{"NEOVIM", "vim", true},
		{"neovim", "VIM", true},
		{"nvim", "vim", true},
		{"Crush", "crush", true},
		{"CRUSH", "crush", true},
		{"vscode", "vim", false},
		{"", "vim", false},
		{"vim", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.s+"_"+tt.substr, func(t *testing.T) {
			got := contains(tt.s, tt.substr)
			if got != tt.want {
				t.Errorf("contains(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
			}
		})
	}
}

func TestBridgeConnections(t *testing.T) {
	// Create pipes for testing
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()

	// Create a mock socket connection using pipes
	clientConn, serverConn := net.Pipe()

	logger := log.New(io.Discard, "", 0)

	// Start bridge in background
	done := make(chan struct{})
	go func() {
		bridgeConnections(stdinReader, stdoutWriter, clientConn, logger)
		close(done)
	}()

	// Test stdin -> socket
	testMsg := rpc.EncodeMessage(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "test",
	})

	go func() {
		stdinWriter.Write([]byte(testMsg))
	}()

	// Read from server side of socket
	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len(testMsg))
	n, err := io.ReadFull(serverConn, buf)
	if err != nil {
		t.Errorf("Failed to read from server: %v", err)
	}
	if n != len(testMsg) {
		t.Errorf("Expected %d bytes, got %d", len(testMsg), n)
	}

	// Test socket -> stdout
	responseMsg := rpc.EncodeMessage(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  "ok",
	})

	go func() {
		serverConn.Write([]byte(responseMsg))
	}()

	// Read from stdout
	buf = make([]byte, len(responseMsg))
	n, err = io.ReadFull(stdoutReader, buf)
	if err != nil {
		t.Errorf("Failed to read from stdout: %v", err)
	}
	if n != len(responseMsg) {
		t.Errorf("Expected %d bytes, got %d", len(responseMsg), n)
	}

	// Cleanup
	stdinWriter.Close()
	serverConn.Close()
	<-done
}

func TestDecodeInitializeParams(t *testing.T) {
	// Test that we can properly decode the clientInfo from initialize params
	msg := createInitializeMessage("Neovim 0.10")

	method, content, err := rpc.DecodeMessage([]byte(msg))
	if err != nil {
		t.Fatalf("Failed to decode message: %v", err)
	}

	if method != "initialize" {
		t.Fatalf("Expected method 'initialize', got %q", method)
	}

	var req struct {
		Params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		} `json:"params"`
	}

	if err := json.Unmarshal(content, &req); err != nil {
		t.Fatalf("Failed to unmarshal params: %v", err)
	}

	if req.Params.ClientInfo.Name != "Neovim 0.10" {
		t.Fatalf("Expected client name 'Neovim 0.10', got %q", req.Params.ClientInfo.Name)
	}
}
