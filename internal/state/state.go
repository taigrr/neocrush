// Package state provides thread-safe document state management for LSP sync
package state

import (
	"sync"
	"time"

	"github.com/taigrr/neocrush/lsp"
)

// CursorSource indicates where cursor information came from.
// This is used to track the origin of cursor position updates
// for debugging and to prioritize certain sources over others.
type CursorSource string

const (
	CursorSourceCustom     CursorSource = "crush/cursorMoved"
	CursorSourceHover      CursorSource = "textDocument/hover"
	CursorSourceCompletion CursorSource = "textDocument/completion"
	CursorSourceDefinition CursorSource = "textDocument/definition"
	CursorSourceHighlight  CursorSource = "textDocument/documentHighlight"
	CursorSourceCodeAction CursorSource = "textDocument/codeAction"
)

// CursorState tracks the current cursor position for a client.
type CursorState struct {
	URI       string
	Position  lsp.Position
	Selection *lsp.Range // nil if no selection
	Source    CursorSource
	Timestamp time.Time
}

// Document represents an open text document with thread-safe access.
// It stores the current content, version, and provides synchronized
// read/write operations for safe concurrent access from multiple goroutines.
type Document struct {
	URI        string
	Content    string
	Version    int
	LanguageID string
	mu         sync.RWMutex
}

// NewDocument creates a new document.
func NewDocument(uri, content, languageID string, version int) *Document {
	return &Document{
		URI:        uri,
		Content:    content,
		Version:    version,
		LanguageID: languageID,
	}
}

// GetContent returns the document content safely.
func (d *Document) GetContent() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.Content
}

// SetContent updates the document content safely.
func (d *Document) SetContent(content string, version int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Content = content
	d.Version = version
}

// State manages shared state for a session with thread-safe access.
type State struct {
	mu          sync.RWMutex
	documents   map[string]*Document
	cursors     map[string]*CursorState // clientID -> cursor
	diagnostics map[string][]lsp.Diagnostic
	version     int64 // monotonic state version for change detection
}

// NewState creates a new thread-safe state manager.
func NewState() *State {
	return &State{
		documents:   make(map[string]*Document),
		cursors:     make(map[string]*CursorState),
		diagnostics: make(map[string][]lsp.Diagnostic),
	}
}

// OpenDocument opens a document and returns initial diagnostics.
func (s *State) OpenDocument(uri, text, languageID string, version int) []lsp.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.documents[uri] = NewDocument(uri, text, languageID, version)
	s.version++

	diags := getDiagnosticsForFile(text)
	s.diagnostics[uri] = diags
	return diags
}

// UpdateDocument updates a document and returns new diagnostics.
func (s *State) UpdateDocument(uri, text string, version int) []lsp.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()

	if doc, ok := s.documents[uri]; ok {
		doc.SetContent(text, version)
	} else {
		s.documents[uri] = NewDocument(uri, text, "", version)
	}
	s.version++

	diags := getDiagnosticsForFile(text)
	s.diagnostics[uri] = diags
	return diags
}

// CloseDocument removes a document from state.
func (s *State) CloseDocument(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.documents, uri)
	delete(s.diagnostics, uri)
	s.version++
}

// GetDocument returns a document by URI, or nil if not found.
func (s *State) GetDocument(uri string) *Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.documents[uri]
}

// GetDocumentContent returns the content of a document.
func (s *State) GetDocumentContent(uri string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if doc, ok := s.documents[uri]; ok {
		return doc.GetContent(), true
	}
	return "", false
}

// UpdateCursor updates the cursor state for a client.
func (s *State) UpdateCursor(clientID, uri string, position lsp.Position, source CursorSource) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cursors[clientID] = &CursorState{
		URI:       uri,
		Position:  position,
		Source:    source,
		Timestamp: time.Now(),
	}
	s.version++
}

// UpdateCursorWithSelection updates cursor state including selection.
func (s *State) UpdateCursorWithSelection(clientID, uri string, position lsp.Position, selection *lsp.Range, source CursorSource) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cursors[clientID] = &CursorState{
		URI:       uri,
		Position:  position,
		Selection: selection,
		Source:    source,
		Timestamp: time.Now(),
	}
	s.version++
}

// GetCursor returns the current cursor state for a client.
func (s *State) GetCursor(clientID string) *CursorState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if cursor, ok := s.cursors[clientID]; ok {
		// Return a copy to avoid race conditions
		c := *cursor
		return &c
	}
	return nil
}

// GetAllCursors returns all cursor states.
func (s *State) GetAllCursors() map[string]*CursorState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*CursorState, len(s.cursors))
	for k, v := range s.cursors {
		c := *v
		result[k] = &c
	}
	return result
}

// GetDiagnostics returns diagnostics for a URI.
func (s *State) GetDiagnostics(uri string) []lsp.Diagnostic {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if diags, ok := s.diagnostics[uri]; ok {
		result := make([]lsp.Diagnostic, len(diags))
		copy(result, diags)
		return result
	}
	return nil
}

// GetDiagnosticsWithCursor returns diagnostics enriched with cursor context.
func (s *State) GetDiagnosticsWithCursor(uri, clientID string) ([]lsp.Diagnostic, *CursorState) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var diags []lsp.Diagnostic
	if d, ok := s.diagnostics[uri]; ok {
		diags = make([]lsp.Diagnostic, len(d))
		copy(diags, d)
	}

	var cursor *CursorState
	if c, ok := s.cursors[clientID]; ok {
		cc := *c
		cursor = &cc
	}

	return diags, cursor
}

// GetVersion returns the current state version.
func (s *State) GetVersion() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.version
}

// ListDocuments returns all open document URIs.
func (s *State) ListDocuments() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	uris := make([]string, 0, len(s.documents))
	for uri := range s.documents {
		uris = append(uris, uri)
	}
	return uris
}
