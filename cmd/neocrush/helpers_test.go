package main

import (
	"testing"
)

func TestComputeLineEdits_NoChange(t *testing.T) {
	edits := computeLineEdits("hello\nworld", "hello\nworld")
	if len(edits) != 0 {
		t.Fatalf("expected no edits for identical text, got %d", len(edits))
	}
}

func TestComputeLineEdits_SingleLineChange(t *testing.T) {
	edits := computeLineEdits("line1\nline2\nline3", "line1\nchanged\nline3")
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	edit := edits[0]
	r := edit["range"].(map[string]any)
	start := r["start"].(map[string]any)
	end := r["end"].(map[string]any)

	if start["line"].(int) != 1 {
		t.Errorf("expected start line 1, got %d", start["line"].(int))
	}
	if end["line"].(int) != 2 {
		t.Errorf("expected end line 2, got %d", end["line"].(int))
	}
	if edit["newText"].(string) != "changed\n" {
		t.Errorf("expected newText 'changed\\n', got %q", edit["newText"].(string))
	}
}

func TestComputeLineEdits_AddLines(t *testing.T) {
	edits := computeLineEdits("line1\nline3", "line1\nline2\nline3")
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	edit := edits[0]
	newText := edit["newText"].(string)
	if newText != "line2\n" {
		t.Errorf("expected 'line2\\n', got %q", newText)
	}
}

func TestComputeLineEdits_DeleteLines(t *testing.T) {
	edits := computeLineEdits("line1\nline2\nline3", "line1\nline3")
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	edit := edits[0]
	r := edit["range"].(map[string]any)
	start := r["start"].(map[string]any)
	end := r["end"].(map[string]any)

	// Should delete the line2 region
	if start["line"].(int) != 1 {
		t.Errorf("expected start line 1, got %d", start["line"].(int))
	}
	if end["line"].(int) != 2 {
		t.Errorf("expected end line 2, got %d", end["line"].(int))
	}
}

func TestComputeLineEdits_MultipleLineChange(t *testing.T) {
	old := "a\nb\nc\nd\ne"
	new := "a\nx\ny\nd\ne"
	edits := computeLineEdits(old, new)
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}

	edit := edits[0]
	r := edit["range"].(map[string]any)
	start := r["start"].(map[string]any)
	end := r["end"].(map[string]any)

	if start["line"].(int) != 1 {
		t.Errorf("expected start line 1, got %d", start["line"].(int))
	}
	if end["line"].(int) != 3 {
		t.Errorf("expected end line 3, got %d", end["line"].(int))
	}
}

func TestComputeLineEdits_EmptyToContent(t *testing.T) {
	edits := computeLineEdits("", "new content")
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
}

func TestComputeLineEdits_ContentToEmpty(t *testing.T) {
	edits := computeLineEdits("old content", "")
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
}

func TestExtractFilename(t *testing.T) {
	tests := []struct {
		uri      string
		expected string
	}{
		{"file:///home/user/test.go", "test.go"},
		{"file:///test.go", "test.go"},
		{"test.go", "test.go"},
		{"file:///home/user/deep/path/file.rs", "file.rs"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			got := extractFilename(tt.uri)
			if got != tt.expected {
				t.Errorf("extractFilename(%q) = %q, want %q", tt.uri, got, tt.expected)
			}
		})
	}
}

func TestURIToPath(t *testing.T) {
	tests := []struct {
		uri      string
		expected string
		hasError bool
	}{
		{"file:///home/user/test.go", "/home/user/test.go", false},
		{"file:///test.go", "/test.go", false},
		{"https://example.com", "", true},
		{"test.go", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			got, err := uriToPath(tt.uri)
			if tt.hasError {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tt.uri)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tt.uri, err)
				return
			}
			if got != tt.expected {
				t.Errorf("uriToPath(%q) = %q, want %q", tt.uri, got, tt.expected)
			}
		})
	}
}
