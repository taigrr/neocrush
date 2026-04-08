package state

import (
	"sync"
	"testing"

	"github.com/taigrr/neocrush/lsp"
)

func TestNewState(t *testing.T) {
	s := NewState()
	if s == nil {
		t.Fatal("NewState returned nil")
	}
	if s.GetVersion() != 0 {
		t.Fatalf("expected initial version 0, got %d", s.GetVersion())
	}
	if len(s.ListDocuments()) != 0 {
		t.Fatalf("expected no documents, got %d", len(s.ListDocuments()))
	}
}

func TestOpenAndGetDocument(t *testing.T) {
	s := NewState()
	uri := "file:///test.go"

	diags := s.OpenDocument(uri, "hello world", "go", 1)
	if diags == nil {
		t.Fatal("expected non-nil diagnostics slice")
	}

	doc := s.GetDocument(uri)
	if doc == nil {
		t.Fatal("expected document, got nil")
	}
	if doc.GetContent() != "hello world" {
		t.Fatalf("expected content 'hello world', got %q", doc.GetContent())
	}
	if doc.Version != 1 {
		t.Fatalf("expected version 1, got %d", doc.Version)
	}
	if doc.LanguageID != "go" {
		t.Fatalf("expected languageID 'go', got %q", doc.LanguageID)
	}

	if s.GetVersion() != 1 {
		t.Fatalf("expected version 1, got %d", s.GetVersion())
	}

	uris := s.ListDocuments()
	if len(uris) != 1 || uris[0] != uri {
		t.Fatalf("expected [%s], got %v", uri, uris)
	}
}

func TestUpdateDocument(t *testing.T) {
	s := NewState()
	uri := "file:///test.go"

	s.OpenDocument(uri, "initial", "go", 1)
	s.UpdateDocument(uri, "updated", 2)

	content, ok := s.GetDocumentContent(uri)
	if !ok {
		t.Fatal("expected document to exist")
	}
	if content != "updated" {
		t.Fatalf("expected 'updated', got %q", content)
	}
}

func TestUpdateDocumentCreatesIfMissing(t *testing.T) {
	s := NewState()
	uri := "file:///new.go"

	s.UpdateDocument(uri, "brand new", 1)

	content, ok := s.GetDocumentContent(uri)
	if !ok {
		t.Fatal("expected document to exist after update-create")
	}
	if content != "brand new" {
		t.Fatalf("expected 'brand new', got %q", content)
	}
}

func TestCloseDocument(t *testing.T) {
	s := NewState()
	uri := "file:///test.go"

	s.OpenDocument(uri, "content", "go", 1)
	s.CloseDocument(uri)

	if s.GetDocument(uri) != nil {
		t.Fatal("expected nil after close")
	}
	_, ok := s.GetDocumentContent(uri)
	if ok {
		t.Fatal("expected document not found after close")
	}
}

func TestGetDocumentContentMissing(t *testing.T) {
	s := NewState()
	_, ok := s.GetDocumentContent("file:///nonexistent.go")
	if ok {
		t.Fatal("expected not found for nonexistent URI")
	}
}

func TestCursorOperations(t *testing.T) {
	s := NewState()

	pos := lsp.Position{Line: 10, Character: 5}
	s.UpdateCursor("neovim", "file:///test.go", pos, CursorSourceHover)

	cursor := s.GetCursor("neovim")
	if cursor == nil {
		t.Fatal("expected cursor, got nil")
	}
	if cursor.URI != "file:///test.go" {
		t.Fatalf("expected URI 'file:///test.go', got %q", cursor.URI)
	}
	if cursor.Position.Line != 10 || cursor.Position.Character != 5 {
		t.Fatalf("expected position {10,5}, got {%d,%d}", cursor.Position.Line, cursor.Position.Character)
	}
	if cursor.Source != CursorSourceHover {
		t.Fatalf("expected source %q, got %q", CursorSourceHover, cursor.Source)
	}
	if cursor.Selection != nil {
		t.Fatal("expected nil selection")
	}
}

func TestCursorWithSelection(t *testing.T) {
	s := NewState()

	pos := lsp.Position{Line: 5, Character: 0}
	sel := &lsp.Range{
		Start: lsp.Position{Line: 5, Character: 0},
		End:   lsp.Position{Line: 5, Character: 10},
	}
	s.UpdateCursorWithSelection("neovim", "file:///test.go", pos, sel, CursorSourceCustom)

	cursor := s.GetCursor("neovim")
	if cursor == nil {
		t.Fatal("expected cursor")
	}
	if cursor.Selection == nil {
		t.Fatal("expected selection")
	}
	if cursor.Selection.Start.Character != 0 || cursor.Selection.End.Character != 10 {
		t.Fatalf("expected selection 0-10, got %d-%d", cursor.Selection.Start.Character, cursor.Selection.End.Character)
	}
}

func TestGetCursorMissing(t *testing.T) {
	s := NewState()
	if s.GetCursor("nonexistent") != nil {
		t.Fatal("expected nil for missing cursor")
	}
}

func TestGetAllCursors(t *testing.T) {
	s := NewState()

	s.UpdateCursor("neovim", "file:///a.go", lsp.Position{Line: 1}, CursorSourceHover)
	s.UpdateCursor("crush", "file:///b.go", lsp.Position{Line: 2}, CursorSourceCustom)

	cursors := s.GetAllCursors()
	if len(cursors) != 2 {
		t.Fatalf("expected 2 cursors, got %d", len(cursors))
	}
	if cursors["neovim"].Position.Line != 1 {
		t.Fatal("neovim cursor wrong")
	}
	if cursors["crush"].Position.Line != 2 {
		t.Fatal("crush cursor wrong")
	}
}

func TestDiagnostics(t *testing.T) {
	s := NewState()
	uri := "file:///test.txt"

	s.OpenDocument(uri, "I use VS Code daily", "text", 1)

	diags := s.GetDiagnostics(uri)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic for 'VS Code', got %d", len(diags))
	}
	if diags[0].Severity != 1 {
		t.Fatalf("expected severity 1, got %d", diags[0].Severity)
	}
}

func TestDiagnosticsNeovim(t *testing.T) {
	s := NewState()
	uri := "file:///test.txt"

	s.OpenDocument(uri, "I use Neovim btw", "text", 1)

	diags := s.GetDiagnostics(uri)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic for 'Neovim', got %d", len(diags))
	}
	if diags[0].Severity != 2 {
		t.Fatalf("expected severity 2, got %d", diags[0].Severity)
	}
}

func TestDiagnosticsWithCursor(t *testing.T) {
	s := NewState()
	uri := "file:///test.txt"

	s.OpenDocument(uri, "VS Code is fine", "text", 1)
	s.UpdateCursor("neovim", uri, lsp.Position{Line: 0, Character: 3}, CursorSourceHover)

	diags, cursor := s.GetDiagnosticsWithCursor(uri, "neovim")
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if cursor == nil {
		t.Fatal("expected cursor")
	}
	if cursor.Position.Character != 3 {
		t.Fatalf("expected cursor col 3, got %d", cursor.Position.Character)
	}
}

func TestDiagnosticsMissing(t *testing.T) {
	s := NewState()
	diags := s.GetDiagnostics("file:///nonexistent")
	if diags != nil {
		t.Fatal("expected nil diagnostics for missing URI")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewState()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			uri := "file:///test.go"
			s.OpenDocument(uri, "content", "go", n)
			s.GetDocumentContent(uri)
			s.UpdateCursor("client", uri, lsp.Position{Line: n}, CursorSourceHover)
			s.GetCursor("client")
			s.GetVersion()
			s.ListDocuments()
		}(i)
	}
	wg.Wait()
}
