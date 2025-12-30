-- crush-lsp.nvim
-- Neovim plugin for crush-lsp cursor synchronization
--
-- Installation:
--   1. Copy this file to ~/.config/nvim/lua/crush-lsp.lua
--   2. Add to your init.lua: require('crush-lsp').setup()
--
-- Or with lazy.nvim:
--   { dir = "path/to/crush-lsp", config = function() require('crush-lsp').setup() end }

local M = {}

-- Configuration
M.config = {
  -- Debounce delay for cursor notifications (ms)
  debounce_ms = 50,

  -- LSP client name to look for
  client_name = "crush-lsp",

  -- Enable debug logging
  debug = false,
}

-- State
local timer = nil
local enabled = false

-- Debug logging
local function log(msg, ...)
  if M.config.debug then
    print(string.format("[crush-lsp] " .. msg, ...))
  end
end

-- Get the crush-lsp client for current buffer
local function get_client()
  local clients = vim.lsp.get_clients({ name = M.config.client_name })
  if #clients > 0 then
    return clients[1]
  end
  return nil
end

-- Send cursor position notification
local function send_cursor_notification()
  local client = get_client()
  if not client then
    return
  end

  local bufnr = vim.api.nvim_get_current_buf()
  local uri = vim.uri_from_bufnr(bufnr)
  local pos = vim.api.nvim_win_get_cursor(0)

  -- LSP positions are 0-indexed
  local line = pos[1] - 1
  local character = pos[2]

  -- Get visual selection if in visual mode
  local selection = nil
  local mode = vim.fn.mode()
  if mode == 'v' or mode == 'V' or mode == '\22' then -- visual, visual line, visual block
    local start_pos = vim.fn.getpos('v')
    local end_pos = vim.fn.getpos('.')

    selection = {
      start = { line = start_pos[2] - 1, character = start_pos[3] - 1 },
      ['end'] = { line = end_pos[2] - 1, character = end_pos[3] - 1 },
    }
  end

  local params = {
    textDocument = { uri = uri },
    position = { line = line, character = character },
    selection = selection,
  }

  log("Sending cursor: line=%d, char=%d", line, character)

  client.notify("crush/cursorMoved", params)
end

-- Debounced cursor notification
local function notify_cursor_debounced()
  if timer then
    timer:stop()
  end

  timer = vim.loop.new_timer()
  timer:start(M.config.debounce_ms, 0, vim.schedule_wrap(function()
    send_cursor_notification()
  end))
end

-- Setup autocmds for cursor tracking
local function setup_autocmds()
  local group = vim.api.nvim_create_augroup("CrushLSP", { clear = true })

  -- Cursor moved in normal mode
  vim.api.nvim_create_autocmd("CursorMoved", {
    group = group,
    callback = notify_cursor_debounced,
  })

  -- Cursor moved in insert mode
  vim.api.nvim_create_autocmd("CursorMovedI", {
    group = group,
    callback = notify_cursor_debounced,
  })

  -- Selection changed (for visual mode)
  vim.api.nvim_create_autocmd("ModeChanged", {
    group = group,
    pattern = "*:[vV\x16]*", -- entering visual mode
    callback = function()
      -- Setup a timer to track selection changes
      local selection_timer = vim.loop.new_timer()
      selection_timer:start(100, 100, vim.schedule_wrap(function()
        local mode = vim.fn.mode()
        if mode ~= 'v' and mode ~= 'V' and mode ~= '\22' then
          selection_timer:stop()
          selection_timer:close()
          return
        end
        send_cursor_notification()
      end))
    end,
  })

  -- Buffer enter (focus change)
  vim.api.nvim_create_autocmd("BufEnter", {
    group = group,
    callback = function()
      -- Small delay to let buffer fully load
      vim.defer_fn(send_cursor_notification, 10)
    end,
  })

  log("Autocmds setup complete")
end

-- Setup LSP client configuration
local function setup_lsp_config()
  -- You can customize the LSP config here
  -- This is a basic config that should work with the default crush-lsp binary

  local lsp_config = {
    name = M.config.client_name,
    cmd = { "crush-lsp" },
    root_dir = vim.fn.getcwd(),
    capabilities = vim.lsp.protocol.make_client_capabilities(),

    -- Handle custom notifications from server
    handlers = {
      -- Document changed (from Crush edits)
      ["crush/documentChanged"] = function(err, result, ctx, config)
        if err then
          log("Error in documentChanged: %s", vim.inspect(err))
          return
        end

        log("Document changed: %s (source: %s)", result.textDocument.uri, result.changeSource)

        -- The workspace/applyEdit from server should handle buffer updates
        -- This notification is informational for Crush clients
      end,

      -- Focus changed (from Crush)
      ["crush/focusChanged"] = function(err, result, ctx, config)
        if err then
          log("Error in focusChanged: %s", vim.inspect(err))
          return
        end

        log("Focus changed: %s (source: %s)", result.textDocument.uri, result.source)

        -- Only act on focus changes from Crush, not our own
        if result.source == "crush" then
          local uri = result.textDocument.uri
          local bufnr = vim.uri_to_bufnr(uri)

          if bufnr and vim.api.nvim_buf_is_valid(bufnr) then
            -- Switch to the buffer
            vim.api.nvim_set_current_buf(bufnr)
          else
            -- Open the file
            local path = vim.uri_to_fname(uri)
            vim.cmd.edit(path)
          end
        end
      end,
    },
  }

  return lsp_config
end

-- Main setup function
function M.setup(opts)
  -- Merge user config
  if opts then
    M.config = vim.tbl_deep_extend("force", M.config, opts)
  end

  -- Setup autocmds
  setup_autocmds()

  enabled = true
  log("crush-lsp plugin initialized")
end

-- Start the LSP client manually
function M.start()
  local config = setup_lsp_config()
  vim.lsp.start(config)
  log("LSP client started")
end

-- Get current session ID (if available)
function M.get_session_id()
  return os.getenv("CRUSH_SESSION_ID")
end

-- Check if crush-lsp is connected
function M.is_connected()
  return get_client() ~= nil
end

-- Manually send cursor position (useful for testing)
function M.send_cursor()
  send_cursor_notification()
end

-- Disable cursor notifications
function M.disable()
  enabled = false
  vim.api.nvim_del_augroup_by_name("CrushLSP")
  log("crush-lsp disabled")
end

-- Enable cursor notifications
function M.enable()
  enabled = true
  setup_autocmds()
  log("crush-lsp enabled")
end

return M
