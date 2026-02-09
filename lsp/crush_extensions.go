package lsp

// CursorMovedNotification is sent by the client when cursor position changes.
// Method: crush/cursorMoved
type CursorMovedNotification struct {
	Notification
	Params CursorMovedParams `json:"params"`
}

// CursorMovedParams contains cursor position information.
type CursorMovedParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Selection    *Range                 `json:"selection,omitempty"`
}

// SelectionChangedNotification is sent when selection changes.
// Method: crush/selectionChanged
type SelectionChangedNotification struct {
	Notification
	Params SelectionChangedParams `json:"params"`
}

// SelectionChangedParams contains selection information.
type SelectionChangedParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Selections   []Range                `json:"selections"`
	Text         string                 `json:"text,omitempty"` // The selected text content
}

// DocumentChangedNotification is broadcast when document content changes.
// Method: crush/documentChanged
// This is sent from daemon to all connected clients (Crush) when
// either Neovim or Crush modifies a document.
type DocumentChangedNotification struct {
	Notification
	Params DocumentChangedParams `json:"params"`
}

// DocumentChangedParams contains the changed document state.
type DocumentChangedParams struct {
	TextDocument VersionTextDocumentIdentifier `json:"textDocument"`
	Content      string                        `json:"content"`
	ChangeSource string                        `json:"changeSource"` // "neovim" or "crush"
}

// FocusChangedNotification is broadcast when focused document changes.
// Method: crush/focusChanged
type FocusChangedNotification struct {
	Notification
	Params FocusChangedParams `json:"params"`
}

// FocusChangedParams contains focus change information.
type FocusChangedParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Source       string                 `json:"source"` // "neovim" or "crush"
}

// GetStateRequest is used by Crush to get current editor state.
// Method: crush/getState
type GetStateRequest struct {
	Request
	Params GetStateParams `json:"params"`
}

// GetStateParams specifies what state to retrieve.
type GetStateParams struct {
	IncludeContent     bool `json:"includeContent,omitempty"`
	IncludeDiagnostics bool `json:"includeDiagnostics,omitempty"`
	IncludeCursor      bool `json:"includeCursor,omitempty"`
}

// GetStateResponse returns current editor state.
type GetStateResponse struct {
	Response
	Result GetStateResult `json:"result"`
}

// GetStateResult contains the requested state.
type GetStateResult struct {
	FocusedDocument *TextDocumentIdentifier `json:"focusedDocument,omitempty"`
	Cursor          *CursorInfo             `json:"cursor,omitempty"`
	OpenDocuments   []DocumentInfo          `json:"openDocuments,omitempty"`
}

// CursorInfo contains current cursor position and context.
type CursorInfo struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Selection    *Range                 `json:"selection,omitempty"`
	LineContent  string                 `json:"lineContent,omitempty"`
}

// DocumentInfo contains document metadata and optionally content.
type DocumentInfo struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	LanguageID   string                 `json:"languageId,omitempty"`
	Version      int                    `json:"version"`
	Content      *string                `json:"content,omitempty"`
	Diagnostics  []Diagnostic           `json:"diagnostics,omitempty"`
}

// EditFileRequest is used by Crush to apply edits to a file.
// Method: crush/editFile
// The daemon will forward this to Neovim via workspace/applyEdit.
type EditFileRequest struct {
	Request
	Params EditFileParams `json:"params"`
}

// EditFileParams specifies the edits to apply.
type EditFileParams struct {
	TextDocument VersionTextDocumentIdentifier `json:"textDocument"`
	Edits        []TextEdit                    `json:"edits"`
}

// EditFileResponse indicates success/failure of the edit.
type EditFileResponse struct {
	Response
	Result EditFileResult `json:"result"`
}

// EditFileResult contains the edit result.
type EditFileResult struct {
	Applied bool   `json:"applied"`
	Error   string `json:"error,omitempty"`
}

// FocusFileRequest is used by Crush to focus a file in Neovim.
// Method: crush/focusFile
// The daemon will forward this to Neovim via window/showDocument.
type FocusFileRequest struct {
	Request
	Params FocusFileParams `json:"params"`
}

// FocusFileParams specifies the file to focus.
type FocusFileParams struct {
	URI       string `json:"uri"`
	Selection *Range `json:"selection,omitempty"` // Optional: jump to location
	TakeFocus bool   `json:"takeFocus,omitempty"` // Default true
}

// FocusFileResponse indicates success/failure.
type FocusFileResponse struct {
	Response
	Result FocusFileResult `json:"result"`
}

// FocusFileResult contains the focus result.
type FocusFileResult struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// SubscribeRequest is used by Crush to subscribe to state changes.
// Method: crush/subscribe
type SubscribeRequest struct {
	Request
	Params SubscribeParams `json:"params"`
}

// SubscribeParams specifies what events to subscribe to.
type SubscribeParams struct {
	DocumentChanges bool `json:"documentChanges,omitempty"`
	CursorChanges   bool `json:"cursorChanges,omitempty"`
	FocusChanges    bool `json:"focusChanges,omitempty"`
	Diagnostics     bool `json:"diagnostics,omitempty"`
}

// SubscribeResponse confirms subscription.
type SubscribeResponse struct {
	Response
	Result SubscribeResult `json:"result"`
}

// SubscribeResult contains subscription confirmation.
type SubscribeResult struct {
	Subscribed bool `json:"subscribed"`
}

// ShowLocationsNotification is sent to Neovim to display locations in Telescope.
// Method: crush/showLocations
type ShowLocationsNotification struct {
	Notification
	Params ShowLocationsParams `json:"params"`
}

// ShowLocationsParams contains the locations to display.
type ShowLocationsParams struct {
	Title string         `json:"title"`
	Items []LocationItem `json:"items"`
}

// LocationItem represents a single location with AI-generated context.
type LocationItem struct {
	Filename string `json:"filename"`          // Absolute or relative path
	Line     int    `json:"lnum"`              // 1-indexed line number
	Col      int    `json:"col,omitempty"`     // 1-indexed column (optional)
	Text     string `json:"text"`              // The code snippet at this location
	Note     string `json:"note"`              // AI explanation of why this location matters
	Type     string `json:"type,omitempty"`    // E/W/I/N (error/warn/info/note), default N
}
