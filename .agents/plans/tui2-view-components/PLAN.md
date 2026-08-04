---
name: tui2-view-components
description: Build the remaining tview-based view components for the TUI2 migration: modals (question, approval, model picker), expandable blocks (tool calls, reasoning), streaming content, and interactive UI (search, help)
status: approved
---

# TUI2 View Components

## Goal
Build the remaining tview view components for the TUI2 migration.

## Architecture
TUI2 uses `github.com/rivo/tview`. Layout:
```
┌─────────────────────────────────┬───────────┐
│ BANNER (figlet + lolcat)        │ INFOPANE  │
├─────────────────────────────────┤ ┌─────────┤
│ MESSAGES (tview.TextView)       │ │ Sub-    │
│  ├─ reasoning block (collapsed) │ │ agents  │
│  ├─ tool block (collapsed)      │ ├─────────┤
│  ├─ sub-agent block (🤖+blink)  │ │ TODOs   │
│  ├─ streamed assistant text     │ ├─────────┤
│  └─ compaction/escalation msgs  │ │ Context │
├─────────────────────────────────┤ ├─────────┤
│ STATUS BAR (tokens,cost,spin)   │ │ MCP     │
├─────────────────────────────────┤ └─────────┘
│ INPUT  │  :cmd palette          │
└─────────────────────────────────┘
```

---

## Sub-Agent Component Design (SINGLE BLOCK, STATEFUL)

A sub-agent block is created on `SubAgentStartEvent` and transitions
on `SubAgentEndEvent`. It is NOT two separate components.

### Active state (SubAgentStartEvent received, not yet ended)
```
▶ 🤖 analyst · "finding bugs in user auth"
```
- 🤖 **blinks** using a timer: the robot emoji toggles visible/invisible
  every ~500ms via `App.QueueUpdateDraw` — giving a "pulsing" effect
- Label (role name) is colored per the role palette
- `▶` symbol or spinner next to the blinking robot
- Collapsed by default — expand to see specialty, full prompt, elapsed time

### Done state (SubAgentEndEvent received)
```
✓ 🤖 analyst · "finding bugs in user auth" (2.3s)
```
- 🤖 is **static** (solid, no blink)
- `✓` checkmark replaces `▶`
- Duration shown
- Error state: `✗` replaces `✓`, error shown in red

### Expanded view (when user opens the block)
```
╭─ 🤖 analyst ──────────────────────────────
│ Specialty:  Finds and gathers information
│ Task:       finding bugs in user auth
│ Model:      claude-sonnet-4-20250514
│ Duration:   2.3s
│ Result:     (summary or error)
╰────────────────────────────────────────────
```

### Color mapping per role
| Role | Color | Label appearance |
|------|-------|-----------------|
| analyst | cyan | `[cyan]analyst` |
| developer | green | `[green]developer` |
| reviewer | yellow | `[yellow]reviewer` |
| tester | magenta | `[magenta]tester` |
| checker | white | `[white]checker` |
| counter | orange | `[orange]counter` |
| security_auditor | red | `[red]security_auditor` |
| goat-joke-teller | hotpink | `[hotpink]goat-joke-teller` |
| golang-developer | teal | `[teal]golang-developer` |
| golang-tester | lime | `[lime]golang-tester` |
| grump | grey | `[grey]grump` |

### Blink mechanism
- Use `time.NewTicker(500ms)` goroutine calling `App.QueueUpdateDraw`
- Toggle a `blinkVisible bool` on the block struct
- When visible: 🤖 renders; when hidden: a space of same width renders
- Stops when block transitions to Done state
- Reference for timer pattern: the thinking spinner in TUI1

---

## Tool Block Component Design

Same shape as sub-agent block. Created on `ToolStartEvent`, updated on `ToolEndEvent`.

### Active state
```
▶ 🔧 go_test · ./... -run TestFoo
```
- Tool icon by category (see icon map below)
- Tool name colored via semantic tag
- Collapsed by default

### Done state
```
✓ 🔧 go_test · ./... -run TestFoo (1.8s)
```
- Static icon, checkmark, duration

### Expanded
```
╭─ 🔧 go_test ──────────────────────────────
│ Package:  ./...
│ Flags:    -run TestFoo
│ Duration: 1.8s
│ Result:   3 passed, 0 failed
╰────────────────────────────────────────────
```

### Tool icons by category
| Icon | Tools |
|------|-------|
| 📖 | read |
| ✍️ | write, edit |
| 🗑️ | delete |
| 🩹 | patch, sed, replace |
| 🔍 | grep, glob |
| 📂 | ls, file_info |
| 🌐 | http, webfetch |
| 💻 | bash |
| 🪟 | powershell |
| 📦 | git, diff, bisect |
| 🧪 | go_test, go_outline, go_refactor, staticcheck, go_mod |
| 📄 | json_query |
| 🧮 | calculate |
| ✅ | todowrite |
| ❓ | question |
| 📋 | plan, list_subagents |
| 🎯 | skill |
| 🎭 | role |
| 🧠 | memory_search, memory_add, memory_update, memory_delete, memory_search_sessions |
| ⚙️ | background_process |
| 🔗 | task |

---

## Reasoning Block Component

### Collapsed
```
▶ 🧠 Reasoning...
```
- "Reasoning..." text uses **lolcat rainbow gradient** (same as spinning indicator)
- Collapsed by default

### Expanded
```
╭─ 🧠 Reasoning ────────────────────────────
│ (chain-of-thought content)
╰────────────────────────────────────────────
```

---

## Lolcat Utility

- **File**: `internal/tui2/lolcat/lolcat.go`
- Port HSL rainbow from `internal/banner/lolcat.go`
- `Rainbow(text string, seed float64) string` — returns `[#RRGGBB]text[-]` tags
- Seed advances per frame for the flowing rainbow effect
- Applied to: "Reasoning..." header, thinking spinner, streaming indicator

---

## TODO Component (Right Side Infopane)

- **File**: `internal/tui2/components/todo/todo.go` (rewrite)
- Lives in the Infopane's "TODOs" tab
- Live-updating `tview.TextView` with scroll
- Updates on `TodoListEvent` from agent
- Format:
```
☐ HIGH   Write integration tests
☑ MEDIUM Fix the auth middleware
☐ LOW    Update README
```
- Checkmarks toggle when items complete

---

## `:` Command Palette

- **File**: `internal/tui2/components/command/command.go`
- Press `:` → command input slides up from bottom
- Commands: `:q`/`:quit`/`:exit`, `:clear`, `:h`/`:help`, `:compact`, `:model <name>`, `:session`, `:mcp`, `:roles`, `:login`, `:logout`
- Tab-completion, command history (up/down arrows)
- Reference: `internal/repl/slash.go` for command definitions

---

## Keybindings

- **File**: `internal/tui2/keymap.go`
| Key | Action |
|-----|--------|
| `Ctrl+C` / `Esc` | Cancel / dismiss |
| `Ctrl+L` | Clear messages |
| `Ctrl+R` | Toggle all reasoning blocks |
| `Ctrl+T` | Toggle all tool blocks |
| `Ctrl+S` | Toggle all sub-agent blocks |
| `?` | Help overlay |
| `/` | Search messages |
| `:` | Command palette |
| `Tab` / `Shift+Tab` | Switch panels |
| `Enter` | Send / expand block |
| `j`/`k` / `↑`/`↓` | Scroll messages |
| `g`/`G` | Top/bottom of messages |

---

## Phase Plan

### 1. Foundation
- `lolcat` utility (rainbow gradient port)
- `keymap` system (bindings map + tview InputCapture chain)
- `command` palette (`:` commands)

### 2. Message blocks
- `reasoning` block (collapsible, lolcat header)
- `toolblock` (collapsible, icon by category, start→done state transition)
- `subagent` block (collapsible, 🤖 with blink on active, color per role)
- `messages` streaming support (Append/Flush/auto-scroll)

### 3. Modals & overlays
- `question` modal (options, keyboard+mouse)
- `approval` modal (Approve/Deny)
- `help` panel (keybindings overlay)
- `modelpicker` (filterable model list)

### 4. Infopane
- `todo` live-updating view in right pane
- Wire existing formatters as live views
