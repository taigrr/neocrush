package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"educationalsp/internal/session"
	"educationalsp/rpc"
)

var version = "0.1.4"

func main() {
	logPath := flag.String("log", "", "Log file path")
	showVersion := flag.Bool("version", false, "Show version")
	showHelp := flag.Bool("help", false, "Show help")
	daemonMode := flag.Bool("daemon", false, "Run as daemon (internal use)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("crush-lsp version %s\n", version)
		return
	}

	if *showHelp {
		printUsage()
		return
	}

	logger := getLogger(*logPath)

	if *daemonMode {
		runDaemon(logger)
		return
	}

	// Normal mode: ensure daemon is running, then connect
	runClient(logger)
}

func runClient(logger *log.Logger) {
	cwd, _ := os.Getwd()
	mgr := session.NewManager()

	// Try to load existing session (don't check socket - we'll verify by connecting)
	sess, err := mgr.LoadSessionMetadata(cwd)
	if err == nil {
		// Session file exists, try to connect to existing daemon
		conn, err := net.DialTimeout("unix", sess.SocketPath, 2*time.Second)
		if err == nil {
			// Connected to existing daemon
			defer conn.Close()
			logger.Printf("Connected to existing session %s", sess.ID)
			bridgeConnections(os.Stdin, os.Stdout, conn, logger)
			return
		}
		// Socket exists in session but can't connect - daemon probably dead
		logger.Printf("Session exists but daemon unreachable, creating new session")
	}

	// No session or daemon dead - start new daemon
	sess, err = startDaemonAndCreateSession(logger, cwd, mgr)
	if err != nil {
		logger.Fatalf("Failed to start daemon: %v", err)
	}

	conn, err := net.DialTimeout("unix", sess.SocketPath, 5*time.Second)
	if err != nil {
		logger.Fatalf("Failed to connect to daemon: %v", err)
	}
	defer conn.Close()

	logger.Printf("Connected to session %s", sess.ID)
	bridgeConnections(os.Stdin, os.Stdout, conn, logger)
}

func startDaemonAndCreateSession(logger *log.Logger, cwd string, mgr *session.Manager) (*session.Session, error) {
	// Create session first to get socket path
	sess, err := mgr.CreateSession(cwd, os.Getppid())
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	// Start daemon in background
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}

	cmd := exec.Command(exe, "--daemon",
		"--log", filepath.Join(filepath.Dir(sess.SocketPath), "daemon.log"))
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CRUSH_SESSION_ID="+sess.ID)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start daemon: %w", err)
	}

	// Detach from parent
	if err := cmd.Process.Release(); err != nil {
		logger.Printf("Warning: failed to release daemon process: %v", err)
	}

	// Wait for socket to be ready
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := os.Stat(sess.SocketPath); err == nil {
			return sess, nil
		}
	}

	return nil, fmt.Errorf("daemon did not create socket within timeout")
}

func runDaemon(logger *log.Logger) {
	sessionID := os.Getenv("CRUSH_SESSION_ID")
	if sessionID == "" {
		logger.Fatal("CRUSH_SESSION_ID not set")
	}

	cwd, _ := os.Getwd()
	mgr := session.NewManager()

	sess, err := mgr.LoadSessionMetadata(cwd)
	if err != nil {
		logger.Fatalf("Failed to load session: %v", err)
	}

	if sess.ID != sessionID {
		logger.Fatalf("Session ID mismatch: expected %s, got %s", sessionID, sess.ID)
	}

	// Ensure socket directory exists
	socketDir := filepath.Dir(sess.SocketPath)
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		logger.Fatalf("Failed to create socket directory: %v", err)
	}

	// Remove stale socket if exists
	os.Remove(sess.SocketPath)

	listener, err := net.Listen("unix", sess.SocketPath)
	if err != nil {
		logger.Fatalf("Failed to listen on socket: %v", err)
	}
	defer listener.Close()
	defer os.Remove(sess.SocketPath)

	// Set socket permissions
	if err := os.Chmod(sess.SocketPath, 0o600); err != nil {
		logger.Printf("Warning: failed to set socket permissions: %v", err)
	}

	logger.Printf("Daemon listening on %s", sess.SocketPath)

	daemon := &Daemon{
		logger:          logger,
		listener:        listener,
		clients:         make(map[string]net.Conn),
		pendingRequests: make(map[int]bool),
	}

	daemon.run()
}

// Daemon manages connected clients and routes messages between them
type Daemon struct {
	logger   *log.Logger
	listener net.Listener

	mu              sync.RWMutex
	clients         map[string]net.Conn // "neovim" or "crush" -> connection
	requestID       int                 // Counter for generating unique request IDs
	pendingRequests map[int]bool        // Request IDs we've sent (to filter responses)
}

func (d *Daemon) run() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			d.logger.Printf("Accept error: %v", err)
			return
		}

		go d.handleClient(conn)
	}
}

func (d *Daemon) handleClient(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	scanner.Split(rpc.Split)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

	var clientName string

	for scanner.Scan() {
		msg := scanner.Bytes()

		// Parse to identify client from initialize request
		if clientName == "" {
			clientName, _ = d.handleInitialize(msg, conn)
			if clientName != "" {
				d.logger.Printf("Client identified: %s", clientName)
				d.mu.Lock()
				d.clients[clientName] = conn
				d.mu.Unlock()

				defer func() {
					d.mu.Lock()
					delete(d.clients, clientName)
					d.mu.Unlock()
					d.logger.Printf("Client disconnected: %s", clientName)

					// Exit daemon if no clients remain
					if len(d.clients) == 0 {
						d.logger.Println("No clients remaining, shutting down")
						d.listener.Close()
					}
				}()
			}
			continue // Don't forward initialize, we responded to it
		}

		// Handle initialized notification (don't forward, just acknowledge)
		method, content, _ := rpc.DecodeMessage(msg)
		if method == "initialized" {
			continue
		}

		// Filter out responses to our own requests (from Neovim responding to workspace/applyEdit)
		if method == "" && clientName == "neovim" {
			// No method means this is a response, check if it's to one of our requests
			var resp struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(content, &resp) == nil && resp.ID > 0 {
				d.mu.Lock()
				if d.pendingRequests[resp.ID] {
					delete(d.pendingRequests, resp.ID)
					d.mu.Unlock()
					d.logger.Printf("Consumed response to our request #%d", resp.ID)
					continue
				}
				d.mu.Unlock()
			}
		}

		// Forward to peer
		d.forwardToPeer(clientName, msg)
	}

	if err := scanner.Err(); err != nil {
		d.logger.Printf("Client %s read error: %v", clientName, err)
	}
}

// handleInitialize processes the initialize request and sends a response.
// Returns the identified client name and any error.
func (d *Daemon) handleInitialize(msg []byte, conn net.Conn) (string, error) {
	method, content, err := rpc.DecodeMessage(msg)
	if err != nil {
		return "", err
	}

	if method != "initialize" {
		return "", nil
	}

	// Extract request ID and client info
	var req struct {
		ID     interface{} `json:"id"`
		Params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		} `json:"params"`
	}

	if err := json.Unmarshal(content, &req); err != nil {
		return "", err
	}

	// Send initialize response
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]interface{}{
			"capabilities": map[string]interface{}{
				"textDocumentSync": map[string]interface{}{
					"openClose": true,
					"change":    2, // Incremental
				},
			},
			"serverInfo": map[string]interface{}{
				"name":    "crush-lsp",
				"version": version,
			},
		},
	}

	responseMsg := rpc.EncodeMessage(response)
	if _, err := conn.Write([]byte(responseMsg)); err != nil {
		return "", err
	}

	// Identify client
	name := req.Params.ClientInfo.Name
	switch {
	case contains(name, "vim") || contains(name, "nvim") || contains(name, "Neovim"):
		return "neovim", nil
	case contains(name, "crush") || contains(name, "Crush") || contains(name, "powernap"):
		return "crush", nil
	default:
		if name == "" {
			return "unknown", nil
		}
		return name, nil
	}
}

func (d *Daemon) identifyClient(msg []byte) string {
	method, content, err := rpc.DecodeMessage(msg)
	if err != nil {
		return ""
	}

	if method != "initialize" {
		return ""
	}

	var req struct {
		Params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		} `json:"params"`
	}

	if err := json.Unmarshal(content, &req); err != nil {
		return ""
	}

	name := req.Params.ClientInfo.Name
	// Normalize: anything with "vim" -> "neovim", anything with "crush/powernap" -> "crush"
	switch {
	case contains(name, "vim") || contains(name, "nvim") || contains(name, "Neovim"):
		return "neovim"
	case contains(name, "crush") || contains(name, "Crush") || contains(name, "powernap"):
		return "crush"
	default:
		// Unknown client, use the name as-is
		return name
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsLower(s, substr))
}

func containsLower(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if toLower(s[i:i+len(substr)]) == toLower(substr) {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func (d *Daemon) forwardToPeer(fromClient string, msg []byte) {
	var peerName string
	switch fromClient {
	case "neovim":
		peerName = "crush"
	case "crush":
		peerName = "neovim"
	default:
		return // Unknown client, don't forward
	}

	d.mu.RLock()
	peer, ok := d.clients[peerName]
	d.mu.RUnlock()

	if !ok {
		d.logger.Printf("Peer %s not connected, cannot forward", peerName)
		return // Peer not connected
	}

	// Transform messages from Crush to Neovim
	if fromClient == "crush" && peerName == "neovim" {
		transformed := d.transformCrushToNeovim(msg)
		if transformed != nil {
			msg = transformed
		} else {
			return // Message was handled or should not be forwarded
		}
	}

	if _, err := peer.Write(msg); err != nil {
		d.logger.Printf("Failed to forward to %s: %v", peerName, err)
	}
}

// transformCrushToNeovim transforms LSP messages from Crush into messages Neovim understands.
// Returns the transformed message, or nil if the message should not be forwarded.
func (d *Daemon) transformCrushToNeovim(msg []byte) []byte {
	method, content, err := rpc.DecodeMessage(msg)
	if err != nil {
		return msg // Pass through if we can't decode
	}

	switch method {
	case "textDocument/didChange":
		// Transform didChange into workspace/applyEdit
		return d.didChangeToApplyEdit(content)
	case "textDocument/didOpen":
		// Could send window/showDocument to open in Neovim
		d.logger.Printf("Crush opened file, consider notifying Neovim")
		return nil // Don't forward raw didOpen
	case "textDocument/didClose":
		return nil // Don't forward
	default:
		return msg // Forward other messages as-is
	}
}

// didChangeToApplyEdit converts a textDocument/didChange notification into a workspace/applyEdit request.
func (d *Daemon) didChangeToApplyEdit(content []byte) []byte {
	var didChange struct {
		Params struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version int    `json:"version"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		} `json:"params"`
	}

	if err := json.Unmarshal(content, &didChange); err != nil {
		d.logger.Printf("Failed to parse didChange: %v", err)
		return nil
	}

	if len(didChange.Params.ContentChanges) == 0 {
		return nil
	}

	// Get the new content (Crush sends full document)
	newText := didChange.Params.ContentChanges[0].Text
	uri := didChange.Params.TextDocument.URI

	d.logger.Printf("Crush changed file: %s (%d bytes)", uri, len(newText))

	// Create workspace/applyEdit request
	// This replaces the entire document content
	d.mu.Lock()
	d.requestID++
	requestID := d.requestID
	d.pendingRequests[requestID] = true
	d.mu.Unlock()

	applyEdit := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "workspace/applyEdit",
		"params": map[string]interface{}{
			"label": "Crush edit",
			"edit": map[string]interface{}{
				"changes": map[string]interface{}{
					uri: []map[string]interface{}{
						{
							"range": map[string]interface{}{
								"start": map[string]interface{}{"line": 0, "character": 0},
								"end":   map[string]interface{}{"line": 999999, "character": 0},
							},
							"newText": newText,
						},
					},
				},
			},
		},
	}

	return []byte(rpc.EncodeMessage(applyEdit))
}

func bridgeConnections(stdin io.Reader, stdout io.Writer, conn net.Conn, logger *log.Logger) {
	errChan := make(chan error, 2)

	// stdin -> socket
	go func() {
		scanner := bufio.NewScanner(stdin)
		scanner.Split(rpc.Split)
		scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

		for scanner.Scan() {
			if _, err := conn.Write(scanner.Bytes()); err != nil {
				errChan <- err
				return
			}
		}
		errChan <- scanner.Err()
	}()

	// socket -> stdout
	go func() {
		scanner := bufio.NewScanner(conn)
		scanner.Split(rpc.Split)
		scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)

		for scanner.Scan() {
			if _, err := stdout.Write(scanner.Bytes()); err != nil {
				errChan <- err
				return
			}
		}
		errChan <- scanner.Err()
	}()

	<-errChan
}

func printUsage() {
	fmt.Println(`crush-lsp - LSP state synchronization for Crush and Neovim

USAGE:
    crush-lsp [OPTIONS]

OPTIONS:
    --log FILE    Log file path
    --version     Show version
    --help        Show this help

DESCRIPTION:
    Runs as an LSP server that synchronizes state between Neovim and Crush.
    
    On first run, starts a background daemon and connects to it.
    Subsequent clients connect to the same daemon.
    Daemon exits when all clients disconnect.
    
    Client identification is automatic via the LSP initialize request.
    Messages from Neovim are forwarded to Crush and vice versa.

CONFIGURATION:
    Neovim: Add to LSP config with cmd = { "crush-lsp" }
    Crush:  Add to crush.json lsp section with command = "crush-lsp"

FILES:
    .crush/session              Session info (in workspace root)
    $XDG_RUNTIME_DIR/crush-lsp/ Sockets (Linux)
    $TMPDIR/crush-lsp-$UID/     Sockets (macOS)`)
}

func getLogger(path string) *log.Logger {
	if path == "" {
		path = os.Getenv("CRUSH_LOG")
	}
	if path == "" {
		// Default to stderr for client, let daemon set its own
		return log.New(os.Stderr, "[crush-lsp] ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return log.New(os.Stderr, "[crush-lsp] ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	logfile, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return log.New(os.Stderr, "[crush-lsp] ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	return log.New(logfile, "[crush-lsp] ", log.Ldate|log.Ltime|log.Lshortfile)
}
