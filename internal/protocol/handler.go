package protocol

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/taigrr/neocrush/internal/state"
	"github.com/taigrr/neocrush/internal/transport"
	"github.com/taigrr/neocrush/lsp"
)

// ClientType identifies the type of connected client.
type ClientType string

const (
	ClientTypeNeovim ClientType = "neovim"
	ClientTypeCrush  ClientType = "crush"
)

// Client represents a connected LSP client.
type Client struct {
	ID        string
	Type      ClientType
	Transport transport.Transport

	// Subscriptions for Crush clients
	subscriptions Subscriptions

	mu     sync.RWMutex
	closed bool
}

// Subscriptions tracks what events a client is subscribed to.
type Subscriptions struct {
	DocumentChanges bool
	CursorChanges   bool
	FocusChanges    bool
	Diagnostics     bool
}

// Handler processes LSP messages for a session.
type Handler struct {
	state   *state.State
	clients map[string]*Client
	mu      sync.RWMutex
	logger  *log.Logger

	// For generating request IDs
	requestID atomic.Int64

	// Track focused document
	focusedURI string
	focusedMu  sync.RWMutex

	// Neovim client (for sending requests to editor)
	neovimClient *Client
}

// NewHandler creates a new protocol handler.
func NewHandler(state *state.State, logger *log.Logger) *Handler {
	return &Handler{
		state:   state,
		clients: make(map[string]*Client),
		logger:  logger,
	}
}

// AddClient registers a new client.
func (h *Handler) AddClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client.ID] = client

	if client.Type == ClientTypeNeovim {
		h.neovimClient = client
	}
}

// RemoveClient unregisters a client.
func (h *Handler) RemoveClient(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if client, ok := h.clients[clientID]; ok {
		if client.Type == ClientTypeNeovim && h.neovimClient == client {
			h.neovimClient = nil
		}
		delete(h.clients, clientID)
	}
}

// HandleMessage processes an incoming LSP message.
func (h *Handler) HandleMessage(client *Client, method string, content []byte) error {
	h.logger.Printf("[%s:%s] Received: %s", client.Type, client.ID, method)

	switch method {
	// Standard LSP - Initialize
	case "initialize":
		return h.handleInitialize(client, content)
	case "initialized":
		return nil // No-op, just acknowledgment
	case "shutdown":
		return h.handleShutdown(client)
	case "exit":
		return h.handleExit(client)

	// Standard LSP - Document Sync
	case "textDocument/didOpen":
		return h.handleDidOpen(client, content)
	case "textDocument/didChange":
		return h.handleDidChange(client, content)
	case "textDocument/didClose":
		return h.handleDidClose(client, content)
	case "textDocument/didSave":
		return h.handleDidSave(client, content)

	// Standard LSP - Language Features (update cursor as side effect)
	case "textDocument/hover":
		return h.handleHover(client, content)
	case "textDocument/completion":
		return h.handleCompletion(client, content)
	case "textDocument/definition":
		return h.handleDefinition(client, content)
	case "textDocument/documentHighlight":
		return h.handleDocumentHighlight(client, content)
	case "textDocument/codeAction":
		return h.handleCodeAction(client, content)

	// Custom Crush extensions
	case "crush/cursorMoved":
		return h.handleCursorMoved(client, content)
	case "crush/selectionChanged":
		return h.handleSelectionChanged(client, content)
	case "crush/getState":
		return h.handleGetState(client, content)
	case "crush/editFile":
		return h.handleEditFile(client, content)
	case "crush/focusFile":
		return h.handleFocusFile(client, content)
	case "crush/subscribe":
		return h.handleSubscribe(client, content)
	case "crush/showLocations":
		return h.handleShowLocations(client, content)

	default:
		h.logger.Printf("Unknown method: %s", method)
		return nil
	}
}

// handleInitialize processes the initialize request.
func (h *Handler) handleInitialize(client *Client, content []byte) error {
	var request lsp.InitializeRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return fmt.Errorf("failed to parse initialize request: %w", err)
	}

	h.logger.Printf("Client initialized: %s %s",
		request.Params.ClientInfo.Name,
		request.Params.ClientInfo.Version)

	response := lsp.InitializeResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: lsp.InitializeResult{
			Capabilities: lsp.ServerCapabilities{
				TextDocumentSync:   1, // Full sync
				HoverProvider:      true,
				DefinitionProvider: true,
				CodeActionProvider: true,
				CompletionProvider: map[string]any{},
			},
			ServerInfo: lsp.ServerInfo{
				Name:    "neocrush",
				Version: "0.1.0",
			},
		},
	}

	return client.Transport.Write(response)
}

// handleShutdown processes the shutdown request.
func (h *Handler) handleShutdown(client *Client) error {
	h.logger.Printf("Client %s requested shutdown", client.ID)
	return nil
}

// handleExit processes the exit notification.
func (h *Handler) handleExit(client *Client) error {
	h.logger.Printf("Client %s exiting", client.ID)
	client.mu.Lock()
	client.closed = true
	client.mu.Unlock()
	return client.Transport.Close()
}

// handleDidOpen processes textDocument/didOpen.
func (h *Handler) handleDidOpen(client *Client, content []byte) error {
	var notification lsp.DidOpenTextDocumentNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}

	doc := notification.Params.TextDocument
	diagnostics := h.state.OpenDocument(doc.URI, doc.Text, doc.LanguageID, doc.Version)

	// Update focused document
	h.focusedMu.Lock()
	h.focusedURI = doc.URI
	h.focusedMu.Unlock()

	// Send diagnostics back to the client that opened the file
	h.sendDiagnostics(client, doc.URI, diagnostics)

	// Broadcast to subscribers
	h.broadcastDocumentChanged(doc.URI, doc.Text, doc.Version, string(client.Type))
	h.broadcastFocusChanged(doc.URI, string(client.Type))

	return nil
}

// handleDidChange processes textDocument/didChange.
func (h *Handler) handleDidChange(client *Client, content []byte) error {
	var notification lsp.TextDocumentDidChangeNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}

	uri := notification.Params.TextDocument.URI
	version := notification.Params.TextDocument.Version

	for _, change := range notification.Params.ContentChanges {
		diagnostics := h.state.UpdateDocument(uri, change.Text, version)
		h.sendDiagnostics(client, uri, diagnostics)
		h.broadcastDocumentChanged(uri, change.Text, version, string(client.Type))
	}

	return nil
}

// handleDidClose processes textDocument/didClose.
func (h *Handler) handleDidClose(_ *Client, content []byte) error {
	var notification lsp.DidCloseTextDocumentNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}

	h.state.CloseDocument(notification.Params.TextDocument.URI)
	return nil
}

// handleDidSave processes textDocument/didSave.
func (h *Handler) handleDidSave(_ *Client, content []byte) error {
	// Just log for now, state is already up to date from didChange
	var notification lsp.DidSaveTextDocumentNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}
	h.logger.Printf("Document saved: %s", notification.Params.TextDocument.URI)
	return nil
}

// handleHover processes textDocument/hover and updates cursor.
func (h *Handler) handleHover(client *Client, content []byte) error {
	var request lsp.HoverRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	uri := request.Params.TextDocument.URI
	pos := request.Params.Position

	// Update cursor state from this request
	h.state.UpdateCursor(client.ID, uri, pos, state.CursorSourceHover)
	h.broadcastCursorChanged(client.ID, uri, pos)

	// Generate response
	docContent, _ := h.state.GetDocumentContent(uri)
	response := lsp.HoverResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: lsp.HoverResult{
			Contents: fmt.Sprintf("File: %s, Characters: %d", uri, len(docContent)),
		},
	}

	return client.Transport.Write(response)
}

// handleCompletion processes textDocument/completion and updates cursor.
func (h *Handler) handleCompletion(client *Client, content []byte) error {
	var request lsp.CompletionRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	uri := request.Params.TextDocument.URI
	pos := request.Params.Position

	// Update cursor state
	h.state.UpdateCursor(client.ID, uri, pos, state.CursorSourceCompletion)
	h.broadcastCursorChanged(client.ID, uri, pos)

	// Generate response
	response := lsp.CompletionResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: []lsp.CompletionItem{
			{
				Label:         "Neovim (BTW)",
				Detail:        "Very cool editor",
				Documentation: "Fun to watch in videos",
			},
		},
	}

	return client.Transport.Write(response)
}

// handleDefinition processes textDocument/definition and updates cursor.
func (h *Handler) handleDefinition(client *Client, content []byte) error {
	var request lsp.DefinitionRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	uri := request.Params.TextDocument.URI
	pos := request.Params.Position

	// Update cursor state
	h.state.UpdateCursor(client.ID, uri, pos, state.CursorSourceDefinition)
	h.broadcastCursorChanged(client.ID, uri, pos)

	// Generate response (stub - just go to previous line)
	response := lsp.DefinitionResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: lsp.Location{
			URI: uri,
			Range: lsp.Range{
				Start: lsp.Position{Line: max(0, pos.Line-1), Character: 0},
				End:   lsp.Position{Line: max(0, pos.Line-1), Character: 0},
			},
		},
	}

	return client.Transport.Write(response)
}

// handleDocumentHighlight processes textDocument/documentHighlight.
func (h *Handler) handleDocumentHighlight(client *Client, content []byte) error {
	var request lsp.DocumentHighlightRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	uri := request.Params.TextDocument.URI
	pos := request.Params.Position

	// Update cursor state
	h.state.UpdateCursor(client.ID, uri, pos, state.CursorSourceHighlight)
	h.broadcastCursorChanged(client.ID, uri, pos)

	// Return empty highlights (stub)
	response := lsp.DocumentHighlightResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: []lsp.DocumentHighlight{},
	}

	return client.Transport.Write(response)
}

// handleCodeAction processes textDocument/codeAction.
func (h *Handler) handleCodeAction(client *Client, content []byte) error {
	var request lsp.CodeActionRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	uri := request.Params.TextDocument.URI

	// Update cursor from range
	h.state.UpdateCursor(client.ID, uri, request.Params.Range.Start, state.CursorSourceCodeAction)

	// Generate code actions
	docContent, _ := h.state.GetDocumentContent(uri)
	actions := generateCodeActions(uri, docContent)

	response := lsp.TextDocumentCodeActionResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: actions,
	}

	return client.Transport.Write(response)
}

// handleCursorMoved processes crush/cursorMoved.
func (h *Handler) handleCursorMoved(client *Client, content []byte) error {
	var notification lsp.CursorMovedNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}

	uri := notification.Params.TextDocument.URI
	pos := notification.Params.Position

	h.state.UpdateCursorWithSelection(
		client.ID,
		uri,
		pos,
		notification.Params.Selection,
		state.CursorSourceCustom,
	)

	// Update focused document
	h.focusedMu.Lock()
	h.focusedURI = uri
	h.focusedMu.Unlock()

	h.broadcastCursorChanged(client.ID, uri, pos)
	return nil
}

// handleSelectionChanged processes crush/selectionChanged.
func (h *Handler) handleSelectionChanged(client *Client, content []byte) error {
	var notification lsp.SelectionChangedNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}

	uri := notification.Params.TextDocument.URI
	if len(notification.Params.Selections) > 0 {
		sel := notification.Params.Selections[0]
		h.state.UpdateCursorWithSelection(client.ID, uri, sel.Start, &sel, state.CursorSourceCustom)
	}

	return nil
}

// handleGetState processes crush/getState.
func (h *Handler) handleGetState(client *Client, content []byte) error {
	var request lsp.GetStateRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	result := lsp.GetStateResult{}

	// Focused document
	h.focusedMu.RLock()
	if h.focusedURI != "" {
		result.FocusedDocument = &lsp.TextDocumentIdentifier{URI: h.focusedURI}
	}
	h.focusedMu.RUnlock()

	// Cursor info
	if request.Params.IncludeCursor {
		if cursor := h.state.GetCursor(client.ID); cursor != nil {
			result.Cursor = &lsp.CursorInfo{
				TextDocument: lsp.TextDocumentIdentifier{URI: cursor.URI},
				Position:     cursor.Position,
				Selection:    cursor.Selection,
			}
		}
	}

	// Open documents
	for _, uri := range h.state.ListDocuments() {
		doc := h.state.GetDocument(uri)
		if doc == nil {
			continue
		}

		info := lsp.DocumentInfo{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			LanguageID:   doc.LanguageID,
			Version:      doc.Version,
		}

		if request.Params.IncludeContent {
			content := doc.GetContent()
			info.Content = &content
		}

		if request.Params.IncludeDiagnostics {
			info.Diagnostics = h.state.GetDiagnostics(uri)
		}

		result.OpenDocuments = append(result.OpenDocuments, info)
	}

	response := lsp.GetStateResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: result,
	}

	return client.Transport.Write(response)
}

// handleEditFile processes crush/editFile from Crush.
func (h *Handler) handleEditFile(client *Client, content []byte) error {
	var request lsp.EditFileRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	uri := request.Params.TextDocument.URI

	// Apply edits to state
	doc := h.state.GetDocument(uri)
	if doc == nil {
		return h.sendEditFileResponse(client, request.ID, false, "document not open")
	}

	// Forward to Neovim via workspace/applyEdit
	if h.neovimClient != nil {
		err := h.sendApplyEdit(h.neovimClient, uri, request.Params.Edits)
		if err != nil {
			return h.sendEditFileResponse(client, request.ID, false, err.Error())
		}
	}

	return h.sendEditFileResponse(client, request.ID, true, "")
}

// handleFocusFile processes crush/focusFile from Crush.
func (h *Handler) handleFocusFile(client *Client, content []byte) error {
	var request lsp.FocusFileRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	// Forward to Neovim via window/showDocument
	if h.neovimClient != nil {
		err := h.sendShowDocument(h.neovimClient, request.Params.URI, request.Params.Selection)
		if err != nil {
			return h.sendFocusFileResponse(client, request.ID, false, err.Error())
		}
	}

	// Update focused URI
	h.focusedMu.Lock()
	h.focusedURI = request.Params.URI
	h.focusedMu.Unlock()

	h.broadcastFocusChanged(request.Params.URI, "crush")

	return h.sendFocusFileResponse(client, request.ID, true, "")
}

// handleSubscribe processes crush/subscribe.
func (h *Handler) handleSubscribe(client *Client, content []byte) error {
	var request lsp.SubscribeRequest
	if err := json.Unmarshal(content, &request); err != nil {
		return err
	}

	client.mu.Lock()
	client.subscriptions = Subscriptions{
		DocumentChanges: request.Params.DocumentChanges,
		CursorChanges:   request.Params.CursorChanges,
		FocusChanges:    request.Params.FocusChanges,
		Diagnostics:     request.Params.Diagnostics,
	}
	client.mu.Unlock()

	response := lsp.SubscribeResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &request.ID,
		},
		Result: lsp.SubscribeResult{Subscribed: true},
	}

	return client.Transport.Write(response)
}

// handleShowLocations forwards crush/showLocations to Neovim for display.
func (h *Handler) handleShowLocations(client *Client, content []byte) error {
	var notification lsp.ShowLocationsNotification
	if err := json.Unmarshal(content, &notification); err != nil {
		return err
	}

	// Forward to Neovim
	if h.neovimClient != nil {
		return h.sendShowLocations(h.neovimClient, notification.Params)
	}

	h.logger.Printf("No Neovim client connected, cannot show locations")
	return nil
}

// sendShowLocations sends crush/showLocations notification to Neovim.
func (h *Handler) sendShowLocations(client *Client, params lsp.ShowLocationsParams) error {
	notification := lsp.ShowLocationsNotification{
		Notification: lsp.Notification{
			RPC:    "2.0",
			Method: "crush/showLocations",
		},
		Params: params,
	}

	return client.Transport.Write(notification)
}

// sendDiagnostics sends diagnostics to a client.
func (h *Handler) sendDiagnostics(client *Client, uri string, diagnostics []lsp.Diagnostic) {
	notification := lsp.PublishDiagnosticsNotification{
		Notification: lsp.Notification{
			RPC:    "2.0",
			Method: "textDocument/publishDiagnostics",
		},
		Params: lsp.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: diagnostics,
		},
	}

	if err := client.Transport.Write(notification); err != nil {
		h.logger.Printf("Failed to send diagnostics: %v", err)
	}
}

// sendApplyEdit sends workspace/applyEdit to Neovim.
func (h *Handler) sendApplyEdit(client *Client, uri string, edits []lsp.TextEdit) error {
	id := int(h.requestID.Add(1))

	request := lsp.WorkspaceApplyEditRequest{
		Request: lsp.Request{
			RPC:    "2.0",
			ID:     id,
			Method: "workspace/applyEdit",
		},
		Params: lsp.ApplyWorkspaceEditParams{
			Label: "Crush edit",
			Edit: lsp.WorkspaceEdit{
				Changes: map[string][]lsp.TextEdit{uri: edits},
			},
		},
	}

	return client.Transport.Write(request)
}

// sendShowDocument sends window/showDocument to Neovim.
func (h *Handler) sendShowDocument(client *Client, uri string, selection *lsp.Range) error {
	id := int(h.requestID.Add(1))

	request := lsp.ShowDocumentRequest{
		Request: lsp.Request{
			RPC:    "2.0",
			ID:     id,
			Method: "window/showDocument",
		},
		Params: lsp.ShowDocumentParams{
			URI:       uri,
			TakeFocus: true,
			Selection: selection,
		},
	}

	return client.Transport.Write(request)
}

// sendEditFileResponse sends crush/editFile response.
func (h *Handler) sendEditFileResponse(client *Client, id int, applied bool, errMsg string) error {
	response := lsp.EditFileResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &id,
		},
		Result: lsp.EditFileResult{
			Applied: applied,
			Error:   errMsg,
		},
	}
	return client.Transport.Write(response)
}

// sendFocusFileResponse sends crush/focusFile response.
func (h *Handler) sendFocusFileResponse(client *Client, id int, success bool, errMsg string) error {
	response := lsp.FocusFileResponse{
		Response: lsp.Response{
			RPC: "2.0",
			ID:  &id,
		},
		Result: lsp.FocusFileResult{
			Success: success,
			Error:   errMsg,
		},
	}
	return client.Transport.Write(response)
}

// broadcastDocumentChanged notifies subscribed clients of document changes.
func (h *Handler) broadcastDocumentChanged(uri, content string, version int, source string) {
	notification := lsp.DocumentChangedNotification{
		Notification: lsp.Notification{
			RPC:    "2.0",
			Method: "crush/documentChanged",
		},
		Params: lsp.DocumentChangedParams{
			TextDocument: lsp.VersionTextDocumentIdentifier{
				TextDocumentIdentifier: lsp.TextDocumentIdentifier{URI: uri},
				Version:                version,
			},
			Content:      content,
			ChangeSource: source,
		},
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		client.mu.RLock()
		shouldSend := client.subscriptions.DocumentChanges
		client.mu.RUnlock()

		if shouldSend {
			if err := client.Transport.Write(notification); err != nil {
				h.logger.Printf("Failed to broadcast to %s: %v", client.ID, err)
			}
		}
	}
}

// broadcastCursorChanged notifies subscribed clients of cursor changes.
func (h *Handler) broadcastCursorChanged(sourceClientID, uri string, pos lsp.Position) {
	notification := lsp.CursorMovedNotification{
		Notification: lsp.Notification{
			RPC:    "2.0",
			Method: "crush/cursorMoved",
		},
		Params: lsp.CursorMovedParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Position:     pos,
		},
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		if client.ID == sourceClientID {
			continue // Don't echo back to sender
		}

		client.mu.RLock()
		shouldSend := client.subscriptions.CursorChanges
		client.mu.RUnlock()

		if shouldSend {
			if err := client.Transport.Write(notification); err != nil {
				h.logger.Printf("Failed to broadcast cursor to %s: %v", client.ID, err)
			}
		}
	}
}

// broadcastFocusChanged notifies subscribed clients of focus changes.
func (h *Handler) broadcastFocusChanged(uri, source string) {
	notification := lsp.FocusChangedNotification{
		Notification: lsp.Notification{
			RPC:    "2.0",
			Method: "crush/focusChanged",
		},
		Params: lsp.FocusChangedParams{
			TextDocument: lsp.TextDocumentIdentifier{URI: uri},
			Source:       source,
		},
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		client.mu.RLock()
		shouldSend := client.subscriptions.FocusChanges
		client.mu.RUnlock()

		if shouldSend {
			if err := client.Transport.Write(notification); err != nil {
				h.logger.Printf("Failed to broadcast focus to %s: %v", client.ID, err)
			}
		}
	}
}

// generateCodeActions creates code actions for a document.
func generateCodeActions(uri, text string) []lsp.CodeAction {
	var actions []lsp.CodeAction

	// Reuse the VS Code replacement logic from original
	for row, line := range splitLines(text) {
		if idx := indexOf(line, "VS Code"); idx >= 0 {
			actions = append(actions, lsp.CodeAction{
				Title: "Replace VS C*de with a superior editor",
				Edit: &lsp.WorkspaceEdit{
					Changes: map[string][]lsp.TextEdit{
						uri: {{
							Range:   lineRange(row, idx, idx+len("VS Code")),
							NewText: "Neovim",
						}},
					},
				},
			})
		}
	}

	return actions
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func lineRange(line, start, end int) lsp.Range {
	return lsp.Range{
		Start: lsp.Position{Line: line, Character: start},
		End:   lsp.Position{Line: line, Character: end},
	}
}
