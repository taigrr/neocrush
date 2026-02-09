package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
	"github.com/taigrr/neocrush/internal/session"
	"github.com/taigrr/neocrush/rpc"
)

var version = "0.2.7"

func main() {
	var logPath string
	var daemonMode bool

	rootCmd := &cobra.Command{
		Use:   "neocrush",
		Short: "LSP/MCP multiplexed server for Crush and Neovim",
		Long: `Runs as an LSP server that synchronizes state between Neovim and Crush,
and as an MCP server providing editor context to AI tools.

Protocol is auto-detected from the first message:
  - LSP: Content-Length header (from Neovim/Crush LSP clients)
  - MCP: Newline-delimited JSON (from AI tools like Claude)

On first run, starts a background daemon and connects to it.
Subsequent clients connect to the same daemon.
Daemon exits when all clients disconnect.

Client identification is automatic via the LSP initialize request.
Messages from Neovim are forwarded to Crush and vice versa.

MCP Tools:
  editor_context   Get cursor position, surrounding code, and active file
  show_locations   Display code locations with AI explanations in Telescope

Configuration:
  Neovim: cmd = { "neocrush" }
  Crush:  { "lsp": { "command": "neocrush" } }
  MCP:    { "command": "neocrush" }

Files:
  .crush/session               Session info (workspace root)
  $XDG_RUNTIME_DIR/neocrush/   Sockets (Linux)
  $TMPDIR/neocrush-$UID/       Sockets (macOS)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(logPath)

			if daemonMode {
				runDaemon(logger)
				return nil
			}

			runClient(logger)
			return nil
		},
	}

	rootCmd.Flags().StringVar(&logPath, "log", "", "Log file path")
	rootCmd.Flags().BoolVar(&daemonMode, "daemon", false, "Run as daemon (internal use)")
	_ = rootCmd.Flags().MarkHidden("daemon")

	if err := fang.Execute(context.Background(), rootCmd, fang.WithVersion(version)); err != nil {
		os.Exit(1)
	}
}

func runClient(logger *log.Logger) {
	cwd, _ := os.Getwd()
	mgr := session.NewManager()

	// Peek at stdin to detect protocol (MCP vs LSP)
	// MCP: newline-delimited JSON, starts with '{'
	// LSP: Content-Length header, starts with 'C'
	stdinReader := bufio.NewReader(os.Stdin)

	// Set a reasonable timeout for protocol detection
	// If we don't receive data within 5 seconds, assume MCP (which may send data later)
	done := make(chan struct{})
	var firstByte []byte
	var peekErr error

	go func() {
		firstByte, peekErr = stdinReader.Peek(1)
		close(done)
	}()

	select {
	case <-done:
		if peekErr != nil {
			// EOF or error - could be MCP client that hasn't sent yet, or closed pipe
			// Try running as MCP server anyway - it will handle the error gracefully
			logger.Printf("Peek returned error (%v), attempting MCP mode", peekErr)
			runMCPClient(logger, cwd, mgr, stdinReader)
			return
		}
	case <-time.After(5 * time.Second):
		// Timeout waiting for first byte - assume MCP
		logger.Printf("Timeout waiting for first byte, assuming MCP protocol")
		runMCPClient(logger, cwd, mgr, stdinReader)
		return
	}

	isMCP := firstByte[0] == '{'
	if isMCP {
		logger.Printf("Detected MCP protocol")
		runMCPClient(logger, cwd, mgr, stdinReader)
		return
	}

	logger.Printf("Detected LSP protocol")
	runLSPClient(logger, cwd, mgr, stdinReader)
}

func runMCPClient(logger *log.Logger, cwd string, mgr *session.Manager, stdinReader *bufio.Reader) {
	// Connect to daemon (or start one)
	conn, err := connectToDaemon(logger, cwd, mgr)
	if err != nil {
		logger.Fatalf("Failed to connect to daemon: %v", err)
	}
	defer conn.Close()

	// Run MCP server with daemon connection
	mcpServer := NewMCPServer(conn)

	// Create a custom stdin that uses our buffered reader
	ctx := context.Background()
	if err := mcpServer.RunWithReader(ctx, stdinReader); err != nil {
		logger.Printf("MCP server error: %v", err)
	}
}

func runLSPClient(logger *log.Logger, cwd string, mgr *session.Manager, stdinReader *bufio.Reader) {
	conn, err := connectToDaemon(logger, cwd, mgr)
	if err != nil {
		logger.Fatalf("Failed to connect to daemon: %v", err)
	}
	defer conn.Close()

	logger.Printf("LSP client connected to daemon")
	bridgeConnections(stdinReader, os.Stdout, conn, logger)
}

func connectToDaemon(logger *log.Logger, cwd string, mgr *session.Manager) (net.Conn, error) {
	// Try to load existing session (don't check socket - we'll verify by connecting)
	sess, err := mgr.LoadSessionMetadata(cwd)
	if err == nil {
		// Session file exists, try to connect to existing daemon
		conn, err := net.DialTimeout("unix", sess.SocketPath, 2*time.Second)
		if err == nil {
			logger.Printf("Connected to existing session %s", sess.ID)
			return conn, nil
		}
		// Socket exists in session but can't connect - daemon probably dead
		logger.Printf("Session exists but daemon unreachable, creating new session")
	}

	// No session or daemon dead - start new daemon
	sess, err = startDaemonAndCreateSession(logger, cwd, mgr)
	if err != nil {
		return nil, fmt.Errorf("failed to start daemon: %w", err)
	}

	conn, err := net.DialTimeout("unix", sess.SocketPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to daemon: %w", err)
	}

	logger.Printf("Connected to session %s", sess.ID)
	return conn, nil
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
		documentState:   make(map[string]string),
		neovimOpenDocs:  make(map[string]bool),
	}

	daemon.run()
}

// Daemon manages connected clients and routes messages between them
type Daemon struct {
	logger   *log.Logger
	listener net.Listener

	mu              sync.RWMutex
	clients         map[string]net.Conn // "neovim", "crush", or "mcp" -> connection
	requestID       int                 // Counter for generating unique request IDs
	pendingRequests map[int]bool        // Request IDs we've sent (to filter responses)
	documentState   map[string]string   // URI -> last known content (for diffing)
	neovimOpenDocs  map[string]bool     // URIs of documents open in Neovim

	// Cursor tracking for MCP tool
	cursorURI    string // Current file URI
	cursorLine   int    // 0-indexed line
	cursorColumn int    // 0-indexed column

	// Selection tracking (from crush/selectionChanged)
	selectionText string // Currently selected text (empty if no selection)
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

		// Check for MCP-specific requests first (these don't require identification)
		method, content, _ := rpc.DecodeMessage(msg)

		// Handle MCP-specific methods (these don't require prior identification)
		if method == "crush/getEditorContext" || method == "crush/showLocations" {
			if clientName == "" {
				clientName = "mcp"
				d.logger.Printf("Client identified: %s (from %s)", clientName, method)
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

			if method == "crush/getEditorContext" {
				d.handleGetEditorContext(content, conn)
			} else if method == "crush/showLocations" {
				d.forwardToNeovim(msg)
			}
			continue
		}

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
		if method == "initialized" {
			continue
		}

		// Handle crush/cursorMoved from Neovim
		if method == "crush/cursorMoved" {
			d.handleCursorMoved(content)
			continue
		}

		// Handle crush/selectionChanged from Neovim
		if method == "crush/selectionChanged" {
			d.handleSelectionChanged(content)
			continue
		}

		// Track cursor position from Neovim requests
		if clientName == "neovim" {
			d.trackCursorFromRequest(method, content)
			d.trackNeovimDocuments(method, content)
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
		ID     any `json:"id"`
		Params struct {
			ClientInfo struct {
				Name string `json:"name"`
			} `json:"clientInfo"`
		} `json:"params"`
	}

	if err := json.Unmarshal(content, &req); err != nil {
		return "", err
	}

	// Identify client first to determine capabilities
	clientName := identifyClientName(req.Params.ClientInfo.Name)

	// Different capabilities for different clients
	var changeSync int
	if clientName == "neovim" {
		changeSync = 0 // Don't send us changes - we'll send workspace/applyEdit
	} else {
		changeSync = 2 // Incremental - Crush sends us changes to forward to Neovim
	}

	// Send initialize response
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync": map[string]any{
					"openClose": true,
					"change":    changeSync,
				},
				"experimental": map[string]any{
					"cursorSync":    true,
					"selectionSync": true,
					"editorContext": true,
				},
			},
			"serverInfo": map[string]any{
				"name":    "neocrush",
				"version": version,
			},
		},
	}

	responseMsg := rpc.EncodeMessage(response)
	if _, err := conn.Write([]byte(responseMsg)); err != nil {
		return "", err
	}

	return clientName, nil
}

// identifyClientName normalizes client names from LSP initialize requests.
func identifyClientName(name string) string {
	nameLower := strings.ToLower(name)
	switch {
	case strings.Contains(nameLower, "vim") || strings.Contains(nameLower, "nvim") || strings.Contains(nameLower, "neovim"):
		return "neovim"
	case strings.Contains(nameLower, "crush") || strings.Contains(nameLower, "powernap"):
		return "crush"
	default:
		if name == "" {
			return "unknown"
		}
		return name
	}
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

// forwardToNeovim sends a message directly to Neovim (used for MCP->Neovim forwarding).
func (d *Daemon) forwardToNeovim(msg []byte) {
	d.mu.RLock()
	neovim, ok := d.clients["neovim"]
	d.mu.RUnlock()

	if !ok {
		d.logger.Printf("Neovim not connected, cannot forward")
		return
	}

	if _, err := neovim.Write(msg); err != nil {
		d.logger.Printf("Failed to forward to neovim: %v", err)
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
// Uses line-based diffing to only send changed regions, preserving unsaved changes in other parts of the buffer.
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

	// Get previous state for diffing
	d.mu.Lock()
	oldText, hasOld := d.documentState[uri]
	d.documentState[uri] = newText
	neovimHasFile := d.neovimOpenDocs[uri]
	d.mu.Unlock()

	var edits []map[string]any

	if !neovimHasFile {
		// Neovim doesn't have this file open. Crush already saved to disk.
		// Send a no-op edit (replace changed lines with themselves) to trigger
		// file open and highlight without doubling the content.
		d.logger.Printf("Neovim doesn't have %s open, sending no-op edit for highlight", uri)

		// Compute diff to find which lines changed
		if !hasOld {
			if path, err := uriToPath(uri); err == nil {
				if data, err := os.ReadFile(path); err == nil {
					// Disk has new content, we need oldText from before
					// But we don't have it - use newText to find the region
					// and send a no-op that replaces it with itself
					oldText = string(data)
					hasOld = true
				}
			}
		}

		// Find the changed region by diffing old vs new
		realEdits := computeLineEdits(oldText, newText)
		if len(realEdits) == 0 {
			d.logger.Printf("No changes detected for %s", uri)
			return nil
		}

		// Create no-op edits: replace each region with the NEW content (same as disk)
		for _, edit := range realEdits {
			rangeData := edit["range"].(map[string]any)
			startLine := rangeData["start"].(map[string]any)["line"].(int)
			endLine := rangeData["end"].(map[string]any)["line"].(int)

			// Get the lines from newText that correspond to this range
			newLines := strings.Split(newText, "\n")
			var replacementLines []string
			for i := startLine; i < endLine && i < len(newLines); i++ {
				replacementLines = append(replacementLines, newLines[i])
			}
			replacementText := strings.Join(replacementLines, "\n")
			if len(replacementLines) > 0 && endLine <= len(newLines) {
				replacementText += "\n"
			}

			// No-op: replace the range with what's already there (from disk/newText)
			edits = append(edits, map[string]any{
				"range":   rangeData,
				"newText": replacementText,
			})
		}
	} else {
		// Neovim has the file open - send the real diff
		if !hasOld {
			// First time seeing this file - read from disk as baseline
			if path, err := uriToPath(uri); err == nil {
				if data, err := os.ReadFile(path); err == nil {
					oldText = string(data)
					hasOld = true
				}
			}
		}

		// Compute line-based diff
		edits = computeLineEdits(oldText, newText)
		if len(edits) == 0 {
			d.logger.Printf("No changes detected for %s", uri)
			return nil
		}
	}

	d.logger.Printf("Crush changed file: %s (%d edits, neovim_open=%v)", uri, len(edits), neovimHasFile)

	// Create workspace/applyEdit request with incremental edits
	d.mu.Lock()
	d.requestID++
	requestID := d.requestID
	d.pendingRequests[requestID] = true
	d.mu.Unlock()

	applyEdit := map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "workspace/applyEdit",
		"params": map[string]any{
			"label": "Crush edit",
			"edit": map[string]any{
				"changes": map[string]any{
					uri: edits,
				},
			},
		},
	}

	return []byte(rpc.EncodeMessage(applyEdit))
}

// uriToPath converts a file:// URI to a local path
func uriToPath(uri string) (string, error) {
	if !strings.HasPrefix(uri, "file://") {
		return "", fmt.Errorf("not a file URI: %s", uri)
	}
	return strings.TrimPrefix(uri, "file://"), nil
}

// computeLineEdits computes minimal line-based edits to transform oldText into newText.
// Returns a slice of LSP TextEdit objects.
func computeLineEdits(oldText, newText string) []map[string]any {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	// Find common prefix
	prefixLen := 0
	for prefixLen < len(oldLines) && prefixLen < len(newLines) && oldLines[prefixLen] == newLines[prefixLen] {
		prefixLen++
	}

	// Find common suffix (but don't overlap with prefix)
	suffixLen := 0
	for suffixLen < len(oldLines)-prefixLen && suffixLen < len(newLines)-prefixLen &&
		oldLines[len(oldLines)-1-suffixLen] == newLines[len(newLines)-1-suffixLen] {
		suffixLen++
	}

	// The changed region
	oldStart := prefixLen
	oldEnd := len(oldLines) - suffixLen
	newStart := prefixLen
	newEnd := len(newLines) - suffixLen

	// Clamp to valid ranges to prevent highlight going past buffer length
	if oldEnd > len(oldLines) {
		oldEnd = len(oldLines)
	}
	if newEnd > len(newLines) {
		newEnd = len(newLines)
	}

	if oldStart >= oldEnd && newStart >= newEnd {
		// No changes
		return nil
	}

	// Build the replacement text
	var replacementLines []string
	for i := newStart; i < newEnd; i++ {
		replacementLines = append(replacementLines, newLines[i])
	}
	replacementText := strings.Join(replacementLines, "\n")

	// Add trailing newline if we're not at the end and original had content after
	if newEnd < len(newLines) && len(replacementLines) > 0 {
		replacementText += "\n"
	} else if oldEnd < len(oldLines) && len(replacementLines) > 0 {
		replacementText += "\n"
	}

	// Handle edge case: if we're replacing lines but keeping suffix, we need the newline
	if oldEnd < len(oldLines) && newEnd < len(newLines) && len(replacementLines) == 0 {
		// Deleting lines - no trailing newline needed
	}

	edit := map[string]any{
		"range": map[string]any{
			"start": map[string]any{"line": oldStart, "character": 0},
			"end":   map[string]any{"line": oldEnd, "character": 0},
		},
		"newText": replacementText,
	}

	return []map[string]any{edit}
}

// trackCursorFromRequest extracts cursor position from LSP requests that include position info.
func (d *Daemon) trackCursorFromRequest(method string, content []byte) {
	// Methods that include textDocument + position
	switch method {
	case "textDocument/hover",
		"textDocument/completion",
		"textDocument/definition",
		"textDocument/references",
		"textDocument/documentHighlight",
		"textDocument/codeAction",
		"textDocument/signatureHelp":
		// Extract position
		var req struct {
			Params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
				Position struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"position"`
			} `json:"params"`
		}
		if err := json.Unmarshal(content, &req); err == nil && req.Params.TextDocument.URI != "" {
			d.mu.Lock()
			d.cursorURI = req.Params.TextDocument.URI
			d.cursorLine = req.Params.Position.Line
			d.cursorColumn = req.Params.Position.Character
			d.mu.Unlock()
			d.logger.Printf("Cursor updated: %s:%d:%d (from %s)", d.cursorURI, d.cursorLine, d.cursorColumn, method)
		}
	}
}

// trackNeovimDocuments tracks which documents Neovim has open.
func (d *Daemon) trackNeovimDocuments(method string, content []byte) {
	switch method {
	case "textDocument/didOpen":
		var req struct {
			Params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			} `json:"params"`
		}
		if err := json.Unmarshal(content, &req); err == nil && req.Params.TextDocument.URI != "" {
			d.mu.Lock()
			d.neovimOpenDocs[req.Params.TextDocument.URI] = true
			d.mu.Unlock()
			d.logger.Printf("Neovim opened: %s", req.Params.TextDocument.URI)
		}
	case "textDocument/didClose":
		var req struct {
			Params struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			} `json:"params"`
		}
		if err := json.Unmarshal(content, &req); err == nil && req.Params.TextDocument.URI != "" {
			d.mu.Lock()
			delete(d.neovimOpenDocs, req.Params.TextDocument.URI)
			d.mu.Unlock()
			d.logger.Printf("Neovim closed: %s", req.Params.TextDocument.URI)
		}
	}
}

// handleSelectionChanged processes crush/selectionChanged from Neovim.
func (d *Daemon) handleSelectionChanged(content []byte) {
	var notif struct {
		Params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Text string `json:"text"`
		} `json:"params"`
	}
	if err := json.Unmarshal(content, &notif); err != nil {
		d.logger.Printf("Failed to parse selectionChanged: %v", err)
		return
	}

	d.mu.Lock()
	d.selectionText = notif.Params.Text
	if notif.Params.TextDocument.URI != "" {
		d.cursorURI = notif.Params.TextDocument.URI
	}
	d.mu.Unlock()

	d.logger.Printf("Selection updated: %d chars in %s", len(d.selectionText), d.cursorURI)
}

// handleCursorMoved processes crush/cursorMoved from Neovim.
func (d *Daemon) handleCursorMoved(content []byte) {
	var notif struct {
		Params struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Position struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"position"`
		} `json:"params"`
	}
	if err := json.Unmarshal(content, &notif); err != nil {
		d.logger.Printf("Failed to parse cursorMoved: %v", err)
		return
	}

	d.mu.Lock()
	d.cursorURI = notif.Params.TextDocument.URI
	d.cursorLine = notif.Params.Position.Line
	d.cursorColumn = notif.Params.Position.Character
	d.mu.Unlock()

	d.logger.Printf("Cursor moved: %s:%d:%d", d.cursorURI, d.cursorLine, d.cursorColumn)
}

// handleGetEditorContext responds to crush/getEditorContext requests from MCP clients.
func (d *Daemon) handleGetEditorContext(content []byte, conn net.Conn) {
	var req struct {
		ID any `json:"id"`
	}
	if err := json.Unmarshal(content, &req); err != nil {
		d.logger.Printf("Failed to parse getEditorContext request: %v", err)
		return
	}

	d.mu.RLock()
	uri := d.cursorURI
	line := d.cursorLine
	col := d.cursorColumn
	selectionText := d.selectionText
	docContent, hasDoc := d.documentState[uri]
	d.mu.RUnlock()

	// Build response
	hasSelection := selectionText != ""
	result := map[string]any{
		"uri":           uri,
		"filename":      extractFilename(uri),
		"cursor_line":   line,
		"cursor_column": col,
		"has_selection": hasSelection,
	}
	if hasSelection {
		result["selection"] = selectionText
	}

	if hasDoc {
		lines := strings.Split(docContent, "\n")
		result["total_lines"] = len(lines)

		// Get context lines (5 before, current, 5 after)
		startLine := line - 5
		if startLine < 0 {
			startLine = 0
		}
		endLine := line + 6 // exclusive
		if endLine > len(lines) {
			endLine = len(lines)
		}

		var beforeLines, afterLines []string
		for i := startLine; i < line && i < len(lines); i++ {
			beforeLines = append(beforeLines, lines[i])
		}
		result["context_before"] = strings.Join(beforeLines, "\n")

		if line < len(lines) {
			result["context_line"] = lines[line]
		} else {
			result["context_line"] = ""
		}

		for i := line + 1; i < endLine && i < len(lines); i++ {
			afterLines = append(afterLines, lines[i])
		}
		result["context_after"] = strings.Join(afterLines, "\n")
	} else {
		result["total_lines"] = 0
		result["context_before"] = ""
		result["context_line"] = ""
		result["context_after"] = ""
	}

	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	}

	responseMsg := rpc.EncodeMessage(response)
	if _, err := conn.Write([]byte(responseMsg)); err != nil {
		d.logger.Printf("Failed to send getEditorContext response: %v", err)
	}
}

// extractFilename extracts the filename from a file:// URI.
func extractFilename(uri string) string {
	path := strings.TrimPrefix(uri, "file://")
	idx := strings.LastIndex(path, "/")
	if idx >= 0 {
		return path[idx+1:]
	}
	return path
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

func getLogger(path string) *log.Logger {
	if path == "" {
		path = os.Getenv("CRUSH_LSP_LOG")
	}
	if path == "" {
		// Default to stderr for client, let daemon set its own
		return log.New(os.Stderr, "[neocrush] ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return log.New(os.Stderr, "[neocrush] ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	logfile, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return log.New(os.Stderr, "[neocrush] ", log.Ldate|log.Ltime|log.Lshortfile)
	}

	return log.New(logfile, "[neocrush] ", log.Ldate|log.Ltime|log.Lshortfile)
}
