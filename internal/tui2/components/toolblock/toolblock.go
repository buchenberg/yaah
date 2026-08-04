// Package toolblock renders expandable tool call blocks for the TUI2
// message stream. Each block shows the tool icon, name, arguments,
// and result output. It transitions from "running" to "done/error" state.
package toolblock

import (
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/tui2/colors"
)

// Icon returns the emoji icon for a tool name.
func Icon(name string) string {
	switch name {
	case "read":
		return "📖"
	case "write", "edit":
		return "✍️"
	case "delete":
		return "🗑️"
	case "patch", "sed", "replace":
		return "🩹"
	case "grep", "glob":
		return "🔍"
	case "ls", "file_info":
		return "📂"
	case "http", "webfetch":
		return "🌐"
	case "bash":
		return "💻"
	case "powershell":
		return "🪟"
	case "git", "diff", "bisect":
		return "📦"
	case "go_test", "go_outline", "go_refactor", "staticcheck", "go_mod":
		return "🧪"
	case "json_query":
		return "📄"
	case "calculate":
		return "🧮"
	case "todowrite":
		return "✅"
	case "question":
		return "❓"
	case "plan", "list_subagents":
		return "📋"
	case "skill":
		return "🎯"
	case "role":
		return "🎭"
	case "memory_search", "memory_add", "memory_update", "memory_delete", "memory_search_sessions":
		return "🧠"
	case "background_process":
		return "⚙️"
	case "task":
		return "🔗"
	default:
		return "🔧"
	}
}

// State is the current state of a tool block.
type State int

const (
	Running State = iota
	Done
	Error
)

// Block is an expandable tool call block in the message stream.
type Block struct {
	name      string
	icon      string
	args      string
	result    string
	tag       string // formatted summary line (collapsed view)
	state     State
	expanded  bool
	id        string
	startTime time.Time
	endTime   time.Time
}

// New creates a tool block in Running state.
func New(id, name, args string) *Block {
	icon := Icon(name)
	return &Block{
		id:        id,
		name:      name,
		icon:      icon,
		args:      args,
		state:     Running,
		startTime: time.Now(),
	}
}

func (b *Block) ID() string       { return b.id }
func (b *Block) Name() string     { return b.name }
func (b *Block) S() State         { return b.state }
func (b *Block) IsExpanded() bool { return b.expanded }
func (b *Block) Toggle()          { b.expanded = !b.expanded }

// Complete transitions the block to Done with a result summary.
func (b *Block) Complete(summary, result string) {
	b.endTime = time.Now()
	b.state = Done
	b.tag = summary
	b.result = result
}

// Fail transitions the block to Error state.
func (b *Block) Fail(summary, err string) {
	b.endTime = time.Now()
	b.state = Error
	b.tag = summary
	b.result = err
}

// Render returns the full tview-tagged text for the block.
func (b *Block) Render() string {
	if !b.expanded {
		return b.renderCollapsed()
	}
	return b.renderExpanded()
}

func (b *Block) durationStr() string {
	d := b.endTime.Sub(b.startTime)
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (b *Block) renderCollapsed() string {
	hex := colors.ToolHex(b.name)
	switch b.state {
	case Running:
		return fmt.Sprintf(`  [%s]▶ %s %s[-] [%s]· %s[-]`, hex, b.icon, b.name, colors.Dim, b.args)
	case Done:
		return fmt.Sprintf(`  [%s]✓ %s %s[-] [%s]· %s (%s)[-]`, hex, b.icon, b.name, colors.Dim, b.tag, b.durationStr())
	case Error:
		return fmt.Sprintf(`  [%s]✗ %s %s[-] [red]· %s (%s)[-]`, hex, b.icon, b.name, b.tag, b.durationStr())
	default:
		return ""
	}
}

func (b *Block) renderExpanded() string {
	hex := colors.ToolHex(b.name)
	width := 58
	var bld strings.Builder

	// Header line.
	var header string
	switch b.state {
	case Running:
		header = fmt.Sprintf(`  [%s]▶ %s %s[-]`, hex, b.icon, b.name)
	case Done:
		header = fmt.Sprintf(`  [%s]✓ %s %s[-]`, hex, b.icon, b.name)
	case Error:
		header = fmt.Sprintf(`  [%s]✗ %s %s[-]`, hex, b.icon, b.name)
	}
	fill := max(2, width-len(b.icon)-len(b.name)-2)
	bld.WriteString(header)
	bld.WriteString(fmt.Sprintf(`[%s] %s[-]`, colors.Dim, strings.Repeat("─", fill)))
	bld.WriteString("\n")

	// Args.
	if b.args != "" {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] Args: %s`, colors.Dim, b.args))
		bld.WriteString("\n")
	}

	// Duration.
	if b.state != Running {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] Duration: %s`, colors.Dim, b.durationStr()))
		bld.WriteString("\n")
	}

	// Result (only when done and expanded).
	if b.state == Done && b.result != "" {
		bld.WriteString(fmt.Sprintf(`  [%s]│────[-]`, colors.Dim))
		bld.WriteString("\n")
		for _, line := range strings.Split(b.result, "\n") {
			if line != "" {
				bld.WriteString(fmt.Sprintf(`  [%s]│[-] %s`, colors.Dim, line))
			} else {
				bld.WriteString(fmt.Sprintf(`  [%s]│[-]`, colors.Dim))
			}
			bld.WriteString("\n")
		}
	}

	// Error.
	if b.state == Error && b.result != "" {
		bld.WriteString(fmt.Sprintf(`  [%s]│[-] [red]%s[-]`, colors.Dim, b.result))
		bld.WriteString("\n")
	}

	// Footer.
	bld.WriteString(fmt.Sprintf(`  [%s]╰%s[-]`, colors.Dim, strings.Repeat("─", width)))
	return bld.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
