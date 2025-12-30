package state

import (
	"strings"

	"github.com/taigrr/crush-lsp/lsp"
)

// getDiagnosticsForFile generates diagnostics for file content.
// This is a placeholder implementation - in production this would
// delegate to real language analysis or external LSP servers.
func getDiagnosticsForFile(text string) []lsp.Diagnostic {
	diagnostics := []lsp.Diagnostic{}

	for row, line := range strings.Split(text, "\n") {
		if idx := strings.Index(line, "VS Code"); idx >= 0 {
			diagnostics = append(diagnostics, lsp.Diagnostic{
				Range:    lineRange(row, idx, idx+len("VS Code")),
				Severity: 1,
				Source:   "Common Sense",
				Message:  "Please make sure we use good language in this video",
			})
		}

		if idx := strings.Index(line, "Neovim"); idx >= 0 {
			diagnostics = append(diagnostics, lsp.Diagnostic{
				Range:    lineRange(row, idx, idx+len("Neovim")),
				Severity: 2,
				Source:   "Common Sense",
				Message:  "Great choice :)",
			})
		}
	}

	return diagnostics
}

func lineRange(line, start, end int) lsp.Range {
	return lsp.Range{
		Start: lsp.Position{
			Line:      line,
			Character: start,
		},
		End: lsp.Position{
			Line:      line,
			Character: end,
		},
	}
}
