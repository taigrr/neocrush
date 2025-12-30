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
			Name:    "crush-lsp",
			Version: version,
		},
		&mcp.ServerOptions{
			Instructions: "Provides editor context from Neovim via crush-lsp daemon",
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

// readerStdio wraps a reader with stdout for MCP transport.
type readerStdio struct {
	reader io.Reader
}

func (r *readerStdio) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (r *readerStdio) Write(p []byte) (n int, err error) {
	return os.Stdout.Write(p)
}

func (r *readerStdio) Close() error {
	return nil
}

// RunWithReader starts the MCP server using a custom reader for stdin.
func (m *MCPServer) RunWithReader(ctx context.Context, reader *bufio.Reader) error {
	// Create a transport that uses our buffered reader instead of os.Stdin
	transport := &mcp.StdioTransport{}

	// The StdioTransport uses os.Stdin/os.Stdout directly, so we need to
	// replace os.Stdin temporarily. This is a bit hacky but the SDK doesn't
	// expose a way to provide a custom reader.
	//
	// Actually, let's just use the regular Run since we've already peeked.
	// The buffered reader should work fine as long as we don't double-read.

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

	return m.server.Run(ctx, transport)
}

// Run starts the MCP server using stdio transport.
func (m *MCPServer) Run(ctx context.Context) error {
	return m.server.Run(ctx, &mcp.StdioTransport{})
}
