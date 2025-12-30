# crush-lsp

LSP-based state synchronization between [Crush](https://github.com/charmbracelet/crush) (Charm's AI coding agent) and Neovim.

## Overview

When Crush runs inside Neovim's terminal (`:term`), edits it makes are invisible to Neovim until disk reload.
This LSP server enables bidirectional state synchronization:

- **Neovim → Crush**: Cursor position, current buffer, diagnostics
- **Crush → Neovim**: Live buffer updates, focus changes

## Features

- Thread-safe shared state between Neovim and Crush
- Auto-discovery of sessions via `.crush/session` file
- Secure socket placement (`$XDG_RUNTIME_DIR` or `$TMPDIR`)
- Standard LSP methods (no plugin required for basic features)
- Custom `crush/*` methods for real-time cursor tracking
- `workspace/applyEdit` for Crush→Neovim live edits
- `window/showDocument` for Crush→Neovim focus changes

## Installation

```bash
go install ./cmd/crush-lsp
```

## Usage

### Standalone Mode (Simple)

Run as a standard LSP server—no sessions, basic features only:

```lua
-- In your Neovim config
vim.lsp.start({
  name = "crush-lsp",
  cmd = { "crush-lsp" },
})
```

### Session Mode (Full Features)

For Crush integration with automatic session discovery:

```lua
-- In your Neovim config
vim.lsp.start({
  name = "crush-lsp",
  cmd = { "crush-lsp", "lsp" },
})
```

This creates a `.crush/session` file in your workspace.
Crush automatically discovers the session when run from the same directory.

```bash
# In Neovim's :term or another terminal in the same workspace
crush  # Auto-discovers session via .crush/session
```

### Neovim Plugin (Optional)

For real-time cursor position tracking, add the plugin:

```lua
-- ~/.config/nvim/lua/crush-lsp.lua (copy from plugin/crush-lsp.lua)
require('crush-lsp').setup({
  debounce_ms = 50,  -- cursor notification debounce
  debug = false,     -- enable debug logging
})
```

This adds `crush/cursorMoved` notifications on every cursor movement.
Without the plugin, cursor position is only tracked when you trigger hover, completion, etc.

## Architecture

```
┌───────────────────────────────────────────────────────────┐
│                     crush-lsp process                      │
│  ┌─────────────────────────────────────────────────────┐  │
│  │  Session (discovered via .crush/session)            │  │
│  │  ├─ neovimClient: LSP over stdio                    │  │
│  │  ├─ crushClient: LSP over Unix socket               │  │
│  │  └─ state: *SharedState (mutex-protected)           │  │
│  │      ├─ documents: map[uri]*Document                │  │
│  │      ├─ cursors: map[clientID]*CursorState          │  │
│  │      └─ diagnostics: map[uri][]Diagnostic           │  │
│  └─────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────┘
         ▲                                    ▲
         │ LSP (stdio)                        │ LSP (Unix socket)
         │                                    │
   ┌─────┴─────┐                        ┌─────┴─────┐
   │  Neovim   │                        │   Crush   │
   │  (nvim)   │◄── runs inside ───────►│ (in term) │
   └───────────┘                        └───────────┘
```

## Files

| Path | Purpose |
|------|---------|
| `.crush/session` | Session metadata (in workspace) |
| `$XDG_RUNTIME_DIR/crush-lsp/<id>.sock` | Unix socket (Linux) |
| `$TMPDIR/crush-lsp-$UID/<id>.sock` | Unix socket (macOS) |
| `~/.crush/logs/crush-lsp.log` | Log file |

## LSP Methods

### Standard Methods (Work Automatically)

| Method | Direction | Purpose |
|--------|-----------|---------|
| `textDocument/didOpen` | Client→Server | Open document |
| `textDocument/didChange` | Client→Server | Document edits |
| `textDocument/didClose` | Client→Server | Close document |
| `textDocument/hover` | Client→Server | Hover info (+ cursor update) |
| `textDocument/completion` | Client→Server | Completions (+ cursor update) |
| `textDocument/definition` | Client→Server | Go to definition (+ cursor update) |
| `workspace/applyEdit` | Server→Client | Apply edits from Crush |
| `window/showDocument` | Server→Client | Focus file from Crush |

### Custom Methods (Optional Plugin)

| Method | Direction | Purpose |
|--------|-----------|---------|
| `crush/cursorMoved` | Client→Server | Real-time cursor position |
| `crush/selectionChanged` | Client→Server | Selection changes |
| `crush/documentChanged` | Server→Client | Broadcast document changes |
| `crush/focusChanged` | Server→Client | Broadcast focus changes |
| `crush/getState` | Client→Server | Query current editor state |
| `crush/editFile` | Client→Server | Crush applies edits |
| `crush/focusFile` | Client→Server | Crush changes focus |
| `crush/subscribe` | Client→Server | Subscribe to events |

## Security

- Sockets are created in secure directories with `0700` permissions
- On Linux: `$XDG_RUNTIME_DIR` (user-only tmpfs, cleared on logout)
- On macOS: `$TMPDIR/crush-lsp-$UID/` (user-isolated)
- Session files in `.crush/` are workspace-local

## Development

Based on [educationalsp](https://github.com/tjdevries/educationalsp) by TJ DeVries.

```bash
# Build
go build -o crush-lsp ./cmd/crush-lsp

# Test
go test ./...

# Run standalone
./crush-lsp

# Run with logging
CRUSH_LOG=/tmp/crush-lsp.log ./crush-lsp lsp
```

## License

MIT
