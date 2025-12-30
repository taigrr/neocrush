# crush-lsp

LSP/MCP server for synchronizing editor state between [Crush](https://github.com/charmbracelet/crush) (Charm's AI coding agent) and Neovim.

## Overview

When Crush edits files, this server enables:

- **Live buffer updates**: Crush edits appear instantly in Neovim with flash highlights
- **Cursor/selection tracking**: AI tools can see your current position and selected text
- **Auto-focus**: Edited files open automatically in Neovim
- **MCP integration**: Provides `editor_context` tool for AI assistants

## Features

- Daemon architecture: single daemon per workspace, multiple clients
- Auto-detection of LSP vs MCP protocol
- Flash highlights on AI edits (like yank highlight)
- No-op edits for unopened files (triggers highlight without doubling content)
- Secure socket placement (`$XDG_RUNTIME_DIR` or `$TMPDIR`)
- Custom `crush/*` methods for real-time cursor/selection tracking

## Installation

```bash
go install github.com/taigrr/crush-lsp/cmd/crush-lsp@latest
```

## Neovim Plugin

Install [crush-lsp.nvim](https://github.com/taigrr/crush-lsp.nvim) for full integration:

```lua
-- lazy.nvim
{
  'taigrr/crush-lsp.nvim',
  event = 'VeryLazy',
  opts = {},
}
```

The plugin provides:

- Auto-start LSP on buffer enter
- Flash highlights on `workspace/applyEdit`
- Cursor position sync (`crush/cursorMoved`)
- Selection sync (`crush/selectionChanged`)
- Crush terminal management (`:CrushToggle`, `<leader>cc`)

## MCP Configuration

Add to your MCP config (e.g., Crush's `mcp.json`):

```json
{
  "mcpServers": {
    "crush-lsp": {
      "command": "crush-lsp"
    }
  }
}
```

This provides the `editor_context` tool for AI assistants to query:

- Current file and cursor position
- Surrounding code context (5 lines before/after)
- Selected text (if any)

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    crush-lsp daemon                         │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  Clients:                                             │  │
│  │  ├─ neovim: LSP over Unix socket                      │  │
│  │  ├─ crush:  LSP over Unix socket                      │  │
│  │  └─ mcp:    MCP over Unix socket                      │  │
│  │                                                       │  │
│  │  State:                                               │  │
│  │  ├─ documentState: map[uri]string (content cache)     │  │
│  │  ├─ neovimOpenDocs: map[uri]bool (open files)         │  │
│  │  ├─ cursorURI/Line/Column (last known position)       │  │
│  │  └─ selectionText (last visual selection)             │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
         ▲                    ▲                    ▲
         │                    │                    │
   ┌─────┴─────┐        ┌─────┴─────┐        ┌─────┴─────┐
   │  Neovim   │        │   Crush   │        │ MCP Client│
   │  (LSP)    │        │   (LSP)   │        │ (AI tool) │
   └───────────┘        └───────────┘        └───────────┘
```

## How It Works

1. **First client connects**: Daemon starts, listens on Unix socket
2. **Neovim attaches**: Sends `initialize`, daemon tracks open files via `didOpen`/`didClose`
3. **Crush edits a file**:
   - If file is open in Neovim: send real diff via `workspace/applyEdit`
   - If file is not open: send no-op edit (triggers open + highlight without doubling)
4. **MCP client calls `editor_context`**: Returns cursor position + surrounding code
5. **All clients disconnect**: Daemon shuts down

## Files

| Path                                   | Purpose                           |
| -------------------------------------- | --------------------------------- |
| `.crush/session`                       | Session metadata (workspace root) |
| `$XDG_RUNTIME_DIR/crush-lsp/<id>.sock` | Unix socket (Linux)               |
| `$TMPDIR/crush-lsp-$UID/<id>.sock`     | Unix socket (macOS)               |

## LSP Methods

| Method                   | Direction     | Purpose                    |
| ------------------------ | ------------- | -------------------------- |
| `textDocument/didOpen`   | Client→Server | Track open files           |
| `textDocument/didChange` | Client→Server | Crush sends edits          |
| `textDocument/didClose`  | Client→Server | Track closed files         |
| `workspace/applyEdit`    | Server→Client | Apply edits to Neovim      |
| `crush/cursorMoved`      | Client→Server | Real-time cursor position  |
| `crush/selectionChanged` | Client→Server | Visual selection with text |
| `crush/getEditorContext` | Client→Server | MCP tool queries state     |

## Development

```bash
# Build
go build ./cmd/crush-lsp

# Test
go test ./...

# Run with logging
crush-lsp --log /tmp/crush-lsp.log
```
