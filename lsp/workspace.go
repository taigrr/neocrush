package lsp

// WorkspaceApplyEditRequest is sent from server to client to apply edits.
// Method: workspace/applyEdit
type WorkspaceApplyEditRequest struct {
	Request
	Params ApplyWorkspaceEditParams `json:"params"`
}

// ApplyWorkspaceEditParams contains the workspace edit to apply.
type ApplyWorkspaceEditParams struct {
	Label string        `json:"label,omitempty"`
	Edit  WorkspaceEdit `json:"edit"`
}

// ApplyWorkspaceEditResponse is the client's response to workspace/applyEdit.
type ApplyWorkspaceEditResponse struct {
	Response
	Result ApplyWorkspaceEditResult `json:"result"`
}

// ApplyWorkspaceEditResult indicates whether the edit was applied.
type ApplyWorkspaceEditResult struct {
	Applied       bool   `json:"applied"`
	FailureReason string `json:"failureReason,omitempty"`
}

// ShowDocumentRequest is sent from server to client to show a document.
// Method: window/showDocument
type ShowDocumentRequest struct {
	Request
	Params ShowDocumentParams `json:"params"`
}

// ShowDocumentParams contains the document to show.
type ShowDocumentParams struct {
	URI       string `json:"uri"`
	External  bool   `json:"external,omitempty"`  // Open in external app
	TakeFocus bool   `json:"takeFocus,omitempty"` // Focus the editor
	Selection *Range `json:"selection,omitempty"` // Cursor/selection position
}

// ShowDocumentResponse is the client's response.
type ShowDocumentResponse struct {
	Response
	Result ShowDocumentResult `json:"result"`
}

// ShowDocumentResult indicates whether the document was shown.
type ShowDocumentResult struct {
	Success bool `json:"success"`
}

// DocumentHighlightRequest is sent to get highlights for symbol under cursor.
// Method: textDocument/documentHighlight
type DocumentHighlightRequest struct {
	Request
	Params DocumentHighlightParams `json:"params"`
}

// DocumentHighlightParams contains the position to get highlights for.
type DocumentHighlightParams struct {
	TextDocumentPositionParams
}

// DocumentHighlightResponse contains the highlights.
type DocumentHighlightResponse struct {
	Response
	Result []DocumentHighlight `json:"result"`
}

// DocumentHighlight represents a highlight for a symbol.
type DocumentHighlight struct {
	Range Range                 `json:"range"`
	Kind  DocumentHighlightKind `json:"kind,omitempty"`
}

// DocumentHighlightKind represents the kind of highlight.
type DocumentHighlightKind int

const (
	// DocumentHighlightKindText is a textual occurrence.
	DocumentHighlightKindText DocumentHighlightKind = 1
	// DocumentHighlightKindRead is a read-access of a symbol.
	DocumentHighlightKindRead DocumentHighlightKind = 2
	// DocumentHighlightKindWrite is a write-access of a symbol.
	DocumentHighlightKindWrite DocumentHighlightKind = 3
)

// DidCloseTextDocumentNotification is sent when a document is closed.
// Method: textDocument/didClose
type DidCloseTextDocumentNotification struct {
	Notification
	Params DidCloseTextDocumentParams `json:"params"`
}

// DidCloseTextDocumentParams contains the closed document identifier.
type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// DidSaveTextDocumentNotification is sent when a document is saved.
// Method: textDocument/didSave
type DidSaveTextDocumentNotification struct {
	Notification
	Params DidSaveTextDocumentParams `json:"params"`
}

// DidSaveTextDocumentParams contains the saved document.
type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"` // If includeText is true
}
