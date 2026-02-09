package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// EditorContextInput is the input for the editor_context tool.
type EditorContextInput struct{}

// ShowLocationsInput is the input for the show_locations tool.
type ShowLocationsInput struct {
	Title string         `json:"title"`
	Items []LocationItem `json:"items"`
}

// LocationItem represents a single location with AI-generated context.
type LocationItem struct {
	Filename string `json:"filename"`
	Line     int    `json:"lnum"`
	Col      int    `json:"col,omitempty"`
	Text     string `json:"text"`
	Note     string `json:"note"`
	Type     string `json:"type,omitempty"`
}

// ShowLocationsOutput is the output for the show_locations tool.
type ShowLocationsOutput struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// EditorContextOutput is the output for the editor_context tool.
type EditorContextOutput struct {
	URI           string `json:"uri"`
	Filename      string `json:"filename"`
	CursorLine    int    `json:"cursor_line"`
	CursorColumn  int    `json:"cursor_column"`
	ContextBefore string `json:"context_before"`
	ContextLine   string `json:"context_line"`
	ContextAfter  string `json:"context_after"`
	TotalLines    int    `json:"total_lines"`
	HasSelection  bool   `json:"has_selection"`
	Selection     string `json:"selection,omitempty"`
}

// MCPServer wraps the MCP server with access to daemon state.
type MCPServer struct {
	server     *mcp.Server
	daemonConn net.Conn
}

// NewMCPServer creates a new MCP server connected to the daemon.
func NewMCPServer(daemonConn net.Conn) *MCPServer {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "neocrush",
			Version: version,
		},
		&mcp.ServerOptions{
			Instructions: "Provides editor context from Neovim via neocrush daemon",
		},
	)

	mcpServer := &MCPServer{
		server:     server,
		daemonConn: daemonConn,
	}

	// Add the editor_context tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "editor_context",
		Description: "Get the current editor context including cursor position, surrounding code, and active file from Neovim",
	}, mcpServer.editorContextHandler)

	// Add the show_locations tool
	mcp.AddTool(server, &mcp.Tool{
		Name: "show_locations",
		Description: `Display a list of code locations in Neovim with AI-generated explanations.

Opens a custom Telescope picker with three panes:
- Left: List of locations (filename:line)
- Right: File preview
- Bottom: Your explanation of why this location is relevant

Use this after analyzing code to show the user relevant locations with context.
Press <C-q> in the picker to send all items to the quickfix list.

Each item should include:
- filename: absolute or relative path to the file
- lnum: 1-indexed line number
- text: the relevant code snippet at this location
- note: YOUR explanation of WHY this location matters for the current task (critical - be specific)
- type: N (note), I (info), W (warning), E (error) - defaults to N

The note field is the key differentiator - explain WHY this location is relevant to what the user asked, not just WHAT the code does.`,
	}, mcpServer.showLocationsHandler)

	return mcpServer
}

// editorContextHandler handles the editor_context tool call.
func (m *MCPServer) editorContextHandler(ctx context.Context, req *mcp.CallToolRequest, input EditorContextInput) (*mcp.CallToolResult, EditorContextOutput, error) {
	// Request editor state from daemon
	state, err := m.requestEditorState()
	if err != nil {
		return nil, EditorContextOutput{}, fmt.Errorf("failed to get editor state: %w", err)
	}

	return nil, state, nil
}

// showLocationsHandler handles the show_locations tool call.
func (m *MCPServer) showLocationsHandler(ctx context.Context, req *mcp.CallToolRequest, input ShowLocationsInput) (*mcp.CallToolResult, ShowLocationsOutput, error) {
	if len(input.Items) == 0 {
		return nil, ShowLocationsOutput{Success: false, Error: "no items provided"}, nil
	}

	// Send to daemon which will forward to Neovim
	err := m.sendShowLocations(input.Title, input.Items)
	if err != nil {
		return nil, ShowLocationsOutput{Success: false, Error: err.Error()}, nil
	}

	return nil, ShowLocationsOutput{Success: true}, nil
}

// sendShowLocations sends a crush/showLocations notification to the daemon.
func (m *MCPServer) sendShowLocations(title string, items []LocationItem) error {
	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "crush/showLocations",
		"params": map[string]any{
			"title": title,
			"items": items,
		},
	}

	notifBytes, err := json.Marshal(notification)
	if err != nil {
		return err
	}

	// Format as LSP message with Content-Length header
	msg := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(notifBytes), notifBytes)

	if err := m.daemonConn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	if _, err := m.daemonConn.Write([]byte(msg)); err != nil {
		return err
	}

	return nil
}

// requestEditorState sends a custom request to the daemon to get editor state.
func (m *MCPServer) requestEditorState() (EditorContextOutput, error) {
	// Send a custom JSON-RPC request to the daemon
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "crush/getEditorContext",
		"params":  map[string]any{},
	}

	reqBytes, err := json.Marshal(request)
	if err != nil {
		return EditorContextOutput{}, err
	}

	// Format as LSP message with Content-Length header
	msg := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(reqBytes), reqBytes)

	// Set a timeout for the request
	if err := m.daemonConn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return EditorContextOutput{}, err
	}

	if _, err := m.daemonConn.Write([]byte(msg)); err != nil {
		return EditorContextOutput{}, err
	}

	// Read response
	if err := m.daemonConn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return EditorContextOutput{}, err
	}

	// Read Content-Length header
	buf := make([]byte, 4096)
	n, err := m.daemonConn.Read(buf)
	if err != nil {
		return EditorContextOutput{}, err
	}

	// Parse the response
	response := string(buf[:n])

	// Find the JSON body after headers
	_, jsonBody, found := strings.Cut(response, "\r\n\r\n")
	if !found {
		return EditorContextOutput{}, fmt.Errorf("invalid response format")
	}

	var resp struct {
		Result EditorContextOutput `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(jsonBody), &resp); err != nil {
		return EditorContextOutput{}, fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.Error != nil {
		return EditorContextOutput{}, fmt.Errorf("daemon error: %s", resp.Error.Message)
	}

	return resp.Result, nil
}

// RunWithReader starts the MCP server using a custom reader for stdin.
func (m *MCPServer) RunWithReader(ctx context.Context, reader *bufio.Reader) error {
	// The StdioTransport uses os.Stdin/os.Stdout directly, so we need to
	// replace os.Stdin temporarily. This is a bit hacky but the SDK doesn't
	// expose a way to provide a custom reader.

	// Create a pipe to feed our buffered data
	pipeReader, pipeWriter := io.Pipe()

	// Copy from our buffered reader to the pipe in a goroutine
	go func() {
		defer pipeWriter.Close()
		io.Copy(pipeWriter, reader)
	}()

	// Temporarily replace os.Stdin
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()

	// Feed the pipe reader to os.Stdin replacement
	go func() {
		io.Copy(w, pipeReader)
		w.Close()
	}()

	os.Stdin = r
	defer func() {
		os.Stdin = oldStdin
		r.Close()
	}()

	return m.server.Run(ctx, &mcp.StdioTransport{})
}

// Run starts the MCP server using stdio transport.
func (m *MCPServer) Run(ctx context.Context) error {
	return m.server.Run(ctx, &mcp.StdioTransport{})
}
