---
name: web-ui-commands
description: Add colon-command support with live typeahead/filtering to the web UI, matching the TUI's command palette UX.
status: draft
---

# Web UI Command Palette

## Goal
Add colon-command support with live typeahead filtering to the web UI (`yaah web`), matching the TUI's command palette UX.

## Background
The TUI enters command mode when input starts with `:`. The `CommandPalette` component live-filters the command list by prefix match as the user types and renders matches above the input. The web UI currently has no command support — the input only sends prompts.

## Steps

### 1. Define the command registry (shared Go code)
- Extract command definitions (name, description, handler) from `internal/tui/tui.go` into a shared package or export them so the web server can serve them
- Current commands in the TUI: `:compact`, `:steer`, `:model`, `:banner`, `:clear`, `:help`, `:quit`, `:stop`, `:copyview`
- For the web UI: `:compact`, `:steer`, `:model`, `:clear`, `:help` are relevant; `:quit`/`:stop` are server-level; `:banner`/`:copyview` are TUI-only

### 2. Serve the command list via API
- Add `GET /api/commands` returning JSON array of `{name, description}`
- Filter to web-relevant commands

### 3. Build the frontend command palette
- In `cmd/yaah/web/index.html`, add a command-palette dropdown component
- Detect `:` prefix in the input → show palette
- Live-filter by prefix as user types (replicates `CommandPalette` logic)
- Keyboard nav: ↑↓ to highlight, Enter to select, Esc to dismiss
- Mouse click to select

### 4. Wire commands to backend actions
- `:compact` → `POST /api/action {type:"compact"}` ✅ already exists
- `:model` → `POST /api/action {type:"model", ...}` ✅ already exists
- `:steer <text>` → `POST /api/action {type:"steer", text:"..."}` need to add
- `:clear` → client-side DOM clear + optional session reset
- `:help` → client-side overlay showing available commands

### 5. Style the palette
- Match the existing web UI design (dark theme, rounded corners, shadow)
- Position above the input, attached like a popover

## Files affected
- `cmd/yaah/web.go` — add `/api/commands` endpoint, `steer` action
- `cmd/yaah/web/index.html` — command palette HTML/CSS/JS
- `internal/tui/tui.go` — maybe extract command list (or just duplicate for now)

## Out of scope
- `:banner` command — web UI has no banner
- `:copyview` — TUI-only clipboard feature
- Tab-to-autocomplete (the TUI doesn't have it either; live filtering is the paradigm)
