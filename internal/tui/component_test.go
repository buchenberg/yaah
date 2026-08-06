package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/types"
)

func TestScrollWindow(t *testing.T) {
	cases := []struct {
		name               string
		selected           int
		maxVisible         int
		total              int
		wantStart, wantEnd int
	}{
		{"no scroll needed", 2, 10, 5, 0, 5},
		{"centered selection", 10, 5, 20, 8, 13},
		{"clamped at start", 1, 5, 20, 0, 5},
		{"clamped at end", 19, 5, 20, 15, 20},
		{"total smaller than window", 3, 10, 4, 0, 4},
		{"selection at exact boundary", 5, 5, 10, 3, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end := scrollWindow(c.selected, c.maxVisible, c.total)
			if start != c.wantStart || end != c.wantEnd {
				t.Errorf("scrollWindow(%d, %d, %d) = (%d, %d), want (%d, %d)",
					c.selected, c.maxVisible, c.total, start, end, c.wantStart, c.wantEnd)
			}
		})
	}
}

func TestChatBubble(t *testing.T) {
	t.Run("renders content at width", func(t *testing.T) {
		out := chatBubble("hello world", 40, userStyle, userBgStyle)
		if !strings.Contains(out, "hello world") {
			t.Errorf("expected content, got %q", out)
		}
	})

	t.Run("wraps long lines", func(t *testing.T) {
		long := strings.Repeat("word ", 30)
		out := chatBubble(long, 20, userStyle, userBgStyle)
		if !strings.Contains(out, "\n") {
			t.Errorf("expected wrapping at width 20, got %q", out)
		}
	})
}

func TestUserMessage_Render(t *testing.T) {
	out := NewUserMessage("how do I exit vim", 80).Render()
	if !strings.Contains(out, "how do I exit vim") {
		t.Errorf("expected message content, got %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("expected trailing newline, got %q", out)
	}
}

func TestAssistantMessage_Render(t *testing.T) {
	out := NewAssistantMessage("run tests first").Render()
	if !strings.Contains(out, "run tests first") {
		t.Errorf("expected message content, got %q", out)
	}
}

func TestSubAgentLine_Render(t *testing.T) {
	t.Run("running shows hourglass, robot, role and task", func(t *testing.T) {
		out := stripANSI(NewSubAgentLine("subagent-0", "developer", "fix the bug", true, "", "", "", 80, 20, false).Render())
		if !strings.Contains(out, "⏳") || !strings.Contains(out, "🤖") {
			t.Errorf("expected running icon and robot, got %q", out)
		}
		if !strings.Contains(out, "developer") || !strings.Contains(out, "fix the bug") {
			t.Errorf("expected role and task, got %q", out)
		}
	})

	t.Run("done shows check and duration", func(t *testing.T) {
		out := stripANSI(NewSubAgentLine("subagent-0", "developer", "fix the bug", false, "2.3s", "", "", 80, 20, false).Render())
		if !strings.Contains(out, "✓") || !strings.Contains(out, "(2.3s)") {
			t.Errorf("expected check icon and duration, got %q", out)
		}
		if !strings.Contains(out, "🤖") {
			t.Errorf("expected robot icon, got %q", out)
		}
	})

	t.Run("error shows cross and error text", func(t *testing.T) {
		out := stripANSI(NewSubAgentLine("subagent-0", "reviewer", "review pr", false, "1.0s", "timed out", "", 80, 20, false).Render())
		if !strings.Contains(out, "✗") || !strings.Contains(out, "timed out") {
			t.Errorf("expected error icon and message, got %q", out)
		}
	})

	t.Run("output is a single line", func(t *testing.T) {
		out := NewSubAgentLine("subagent-0", "tester", "run tests", false, "500ms", "", "", 80, 20, false).Render()
		if !strings.HasSuffix(out, "\n") {
			t.Errorf("expected trailing newline, got %q", out)
		}
		if strings.Count(strings.TrimSuffix(out, "\n"), "\n") != 0 {
			t.Errorf("expected exactly one line, got %q", out)
		}
	})

	t.Run("result adds expand toggle; expanded shows output box", func(t *testing.T) {
		collapsed := stripANSI(NewSubAgentLine("subagent-0", "checker", "verify", false, "1.0s", "", "all good", 80, 20, false).Render())
		if !strings.Contains(collapsed, "▶") || strings.Contains(collapsed, "all good") {
			t.Errorf("collapsed line should show toggle but hide result, got %q", collapsed)
		}
		expanded := stripANSI(NewSubAgentLine("subagent-0", "checker", "verify", false, "1.0s", "", "all good", 80, 20, true).Render())
		if !strings.Contains(expanded, "▼") || !strings.Contains(expanded, "all good") {
			t.Errorf("expanded line should show toggle open and result, got %q", expanded)
		}
	})
}

func TestSystemMessage_Render(t *testing.T) {
	out := NewSystemMessage("session resumed", 80).Render()
	if !strings.Contains(out, "session resumed") {
		t.Errorf("expected message content, got %q", out)
	}
}

func TestExpandableSection_Render(t *testing.T) {
	t.Run("collapsed shows toggle only", func(t *testing.T) {
		e := NewExpandableSection("reasoning-0", "Reasoning...", false, "secret thoughts", 80, reasoningBgStyle, thinkingStyle)
		out := e.Render()
		if !strings.Contains(out, "▶ Reasoning...") {
			t.Errorf("expected collapsed toggle, got %q", out)
		}
		if strings.Contains(out, "secret thoughts") {
			t.Errorf("collapsed section should not show content, got %q", out)
		}
	})

	t.Run("expanded shows toggle and content", func(t *testing.T) {
		e := NewExpandableSection("reasoning-0", "Reasoning...", true, "secret thoughts", 80, reasoningBgStyle, thinkingStyle)
		out := e.Render()
		if !strings.Contains(out, "▼ Reasoning...") {
			t.Errorf("expected expanded toggle, got %q", out)
		}
		if !strings.Contains(out, "secret thoughts") {
			t.Errorf("expanded section should show content, got %q", out)
		}
	})

	t.Run("pre-wrapped content keeps indentation", func(t *testing.T) {
		// chatWrap collapses leading whitespace via strings.Fields; a
		// pre-wrapped (e.g. glamour-rendered) code block must keep it.
		code := "    x := compute()\n    return x"
		e := NewExpandableSection("reasoning-0", "Reasoning...", true, code, 80, reasoningBgStyle, thinkingStyle).AsPreWrapped()
		out := e.Render()
		if !strings.Contains(out, "    x := compute()") {
			t.Errorf("pre-wrapped content lost indentation, got %q", out)
		}

		wrapped := NewExpandableSection("reasoning-0", "Reasoning...", true, code, 80, reasoningBgStyle, thinkingStyle)
		if strings.Contains(wrapped.Render(), "    x := compute()") {
			t.Errorf("default mode should collapse indentation via chatWrap")
		}
	})
}

func TestToolMessage_Header(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		toolArgs string
		want     string
	}{
		{"plain tool", "read", `{"path":"/x"}`, "read"},
		{"spawn_subagent with role and desc", "spawn_subagent", `{"role":"developer","description":"list files","prompt":"y"}`, "sub-agent: developer · list files"},
		{"spawn_subagent with desc only", "spawn_subagent", `{"description":"list files","prompt":"y"}`, "sub-agent · list files"},
		{"spawn_subagent with role only", "spawn_subagent", `{"role":"developer","prompt":"y"}`, "sub-agent: developer"},
		{"spawn_subagent bare", "spawn_subagent", `{"prompt":"y"}`, "sub-agent"},
		{"webfetch with url", "webfetch", `{"url":"https://example.com"}`, "web_fetch → https://example.com"},
		{"bash", "bash", `{"command":"ls"}`, "bash — {\"command\":\"ls\"}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toolHeader(c.toolName, c.toolArgs); got != c.want {
				t.Errorf("toolHeader(%q, %q) = %q, want %q", c.toolName, c.toolArgs, got, c.want)
			}
		})
	}
}

func TestToolMessage_Render(t *testing.T) {
	t.Run("collapsed hides content", func(t *testing.T) {
		tm := NewToolMessage("tool-0", "read", `{"path":"/x"}`, "file body", 80, 20, false, false, "")
		out := tm.Render()
		if !strings.Contains(out, "▶ ✓ read") {
			t.Errorf("expected collapsed header, got %q", out)
		}
		if strings.Contains(out, "file body") {
			t.Errorf("collapsed tool should hide content, got %q", out)
		}
	})

	t.Run("expanded shows content in box", func(t *testing.T) {
		tm := NewToolMessage("tool-0", "read", `{"path":"/x"}`, "file body", 80, 20, true, false, "")
		out := tm.Render()
		if !strings.Contains(out, "▼ ✓ read") {
			t.Errorf("expected expanded header, got %q", out)
		}
		if !strings.Contains(out, "file body") {
			t.Errorf("expanded tool should show content, got %q", out)
		}
	})

	t.Run("running tool shows hourglass", func(t *testing.T) {
		tm := NewToolMessage("tool-0", "bash", `{"command":"ls"}`, "output", 80, 20, true, true, "")
		out := tm.Render()
		if !strings.Contains(out, "⏳") {
			t.Errorf("expected hourglass for running tool, got %q", out)
		}
	})

	t.Run("long output is truncated with notice", func(t *testing.T) {
		var lines []string
		for i := 0; i < 100; i++ {
			lines = append(lines, "line of output")
		}
		tm := NewToolMessage("tool-0", "bash", `{}`, strings.Join(lines, "\n"), 80, 12, true, false, "")
		out := tm.Render()
		if !strings.Contains(out, "more lines above") {
			t.Errorf("expected truncation notice, got %q", out)
		}
	})
}

func TestStatusBar_Render(t *testing.T) {
	t.Run("without context window", func(t *testing.T) {
		out := NewStatusBar("/home/user/proj", 5, 0, false, 80).Render()
		if !strings.Contains(out, "messages: 5") {
			t.Errorf("expected message count, got %q", out)
		}
		if strings.Contains(out, "%]") {
			t.Errorf("should not render context bar without window, got %q", out)
		}
	})

	t.Run("with context window", func(t *testing.T) {
		out := NewStatusBar("/home/user/proj", 5, 42, true, 80).Render()
		if !strings.Contains(out, "42%]") {
			t.Errorf("expected context bar with percentage, got %q", out)
		}
	})
}

func TestHeader_Render(t *testing.T) {
	t.Run("with banner", func(t *testing.T) {
		out := NewHeader("YAHHH", "deepseek", "v4-pro", true, 80, nil, "test").Render()
		if !strings.Contains(out, "YAHHH") {
			t.Errorf("expected banner, got %q", out)
		}
		if !strings.Contains(out, "deepseek/v4-pro") {
			t.Errorf("expected provider/model, got %q", out)
		}
	})

	t.Run("banner hidden", func(t *testing.T) {
		out := NewHeader("YAHHH", "deepseek", "v4-pro", false, 80, nil, "").Render()
		if strings.Contains(out, "YAHHH") {
			t.Errorf("banner should be hidden, got %q", out)
		}
		if !strings.Contains(out, "yaah · deepseek/v4-pro") {
			t.Errorf("expected compact title, got %q", out)
		}
	})

	t.Run("right column stays right with MCP servers", func(t *testing.T) {
		mcp := []ServerInfo{
			{Name: "filesystem", Transport: "stdio", Connected: true},
			{Name: "github", Transport: "http", Connected: false},
			{Name: "slack", Transport: "http", Connected: true},
		}
		out := NewHeader("YAHHH", "deepseek", "v4-pro", true, 100, mcp, "").Render()
		plain := stripANSI(out)

		lines := strings.Split(plain, "\n")
		if len(lines) < 3 {
			t.Fatal("expected at least 3 lines after borders")
		}

		// Check that each content line has a key hint, MCP entry, or provider
		// appearing near the right side (past position 50 on 100-wide header).
		for _, line := range lines[2 : len(lines)-1] {
			trimmed := strings.TrimRight(line, " ")
			// Lines with right-aligned content end with the item text,
			// not whitespace. Blank lines (all spaces) are separators.
			if strings.TrimSpace(trimmed) == "" {
				continue
			}
		}

		// All MCP servers should appear in the output.
		for _, info := range mcp {
			if !strings.Contains(plain, info.Name) {
				t.Errorf("MCP server %q not found in header", info.Name)
			}
		}
	})

	t.Run("adding MCP servers does not shift right column leftward", func(t *testing.T) {
		outNone := NewHeader("YAHHH", "deepseek", "v4-pro", true, 100, nil, "").Render()
		plainNone := stripANSI(outNone)

		mcp := []ServerInfo{
			{Name: "filesystem", Transport: "stdio", Connected: true},
		}
		outWith := NewHeader("YAHHH", "deepseek", "v4-pro", true, 100, mcp, "").Render()
		plainWith := stripANSI(outWith)

		// The provider/model text should appear in both outputs.
		if !strings.Contains(plainNone, "deepseek/v4-pro") {
			t.Error("expected provider/model in output without MCP")
		}
		if !strings.Contains(plainWith, "deepseek/v4-pro") {
			t.Error("expected provider/model in output with MCP")
		}
	})

	t.Run("left column stays left of right column content", func(t *testing.T) {
		out := NewHeader("YAHHH", "deepseek", "v4-pro", true, 100, nil, "").Render()
		plain := stripANSI(out)

		lines := strings.Split(plain, "\n")
		for _, line := range lines {
			bannerIdx := strings.Index(line, "YAHHH")
			provIdx := strings.Index(line, "deepseek/v4-pro")
			if bannerIdx >= 0 && provIdx >= 0 && bannerIdx > provIdx {
				t.Errorf("banner appears to the right of provider info: banner@%d, provider@%d: %q", bannerIdx, provIdx, line)
			}
		}
	})
}

func TestCommandPalette_Render(t *testing.T) {
	commands := []Command{
		{Name: ":help", Description: "Show help"},
		{Name: ":model", Description: "Switch model"},
		{Name: ":quit", Description: "Exit"},
	}

	t.Run("empty filter shows all", func(t *testing.T) {
		out := NewCommandPalette(commands, ":", 80).Render()
		for _, c := range commands {
			if !strings.Contains(out, c.Name) {
				t.Errorf("expected %q in palette, got %q", c.Name, out)
			}
		}
	})

	t.Run("filter narrows results", func(t *testing.T) {
		out := NewCommandPalette(commands, ":mo", 80).Render()
		if !strings.Contains(out, ":model") {
			t.Errorf("expected :model, got %q", out)
		}
		if strings.Contains(out, ":help") {
			t.Errorf(":help should be filtered out, got %q", out)
		}
	})

	t.Run("no matches returns empty", func(t *testing.T) {
		out := NewCommandPalette(commands, ":zzz", 80).Render()
		if out != "" {
			t.Errorf("expected empty palette, got %q", out)
		}
	})
}

func TestModelPalette_Render(t *testing.T) {
	models := []string{"deepseek/v4-pro", "deepseek/v4-flash", "glm/4.6"}

	t.Run("empty list", func(t *testing.T) {
		out := NewModelPalette(nil, nil, 0, "", 10, 80).Render()
		if !strings.Contains(out, "No matching models") {
			t.Errorf("expected empty notice, got %q", out)
		}
	})

	t.Run("shows provider headings and models", func(t *testing.T) {
		out := NewModelPalette(models, map[string]string{"deepseek": "DeepSeek"}, 1, "deepseek/v4-pro", 10, 80).Render()
		if !strings.Contains(out, "DeepSeek") {
			t.Errorf("expected provider display name, got %q", out)
		}
		if !strings.Contains(out, "v4-flash") {
			t.Errorf("expected model name, got %q", out)
		}
		if !strings.Contains(out, "v4-pro") || !strings.Contains(out, "(current)") {
			t.Errorf("expected current marker, got %q", out)
		}
		if !strings.Contains(out, " ▶ ") {
			t.Errorf("expected ▶ selection marker, got %q", out)
		}
	})

	t.Run("shows overflow indicator when scrolled", func(t *testing.T) {
		var many []string
		for i := 0; i < 20; i++ {
			many = append(many, fmt.Sprintf("p/m-%02d", i))
		}
		out := NewModelPalette(many, nil, 10, "", 5, 80).Render()
		if !strings.Contains(out, "of 21") { // 20 models + 1 provider heading
			t.Errorf("expected overflow indicator, got %q", out)
		}
	})
}

func TestQuestionPalette_Render(t *testing.T) {
	modal := QuestionModal{
		Header:   "Pick one",
		Question: "Which color?",
		Options: []QuestionOption{
			{Label: "Red", Description: "warm"},
			{Label: "Blue", Description: "cool"},
		},
	}

	t.Run("single select shows cursor", func(t *testing.T) {
		out := NewQuestionPalette(modal, 0, make([]bool, 2), 10, 80).Render()
		if !strings.Contains(out, "Pick one") {
			t.Errorf("expected header, got %q", out)
		}
		if !strings.Contains(out, "Which color?") {
			t.Errorf("expected question, got %q", out)
		}
		if !strings.Contains(out, "Red") || !strings.Contains(out, "Blue") {
			t.Errorf("expected options, got %q", out)
		}
		if !strings.Contains(out, "▶") {
			t.Errorf("expected cursor on selected option, got %q", out)
		}
	})

	t.Run("multi select shows checkboxes", func(t *testing.T) {
		modal.Multiple = true
		out := NewQuestionPalette(modal, 0, []bool{true, false}, 10, 80).Render()
		if !strings.Contains(out, "☑") {
			t.Errorf("expected checked box, got %q", out)
		}
		if !strings.Contains(out, "☐") {
			t.Errorf("expected unchecked box, got %q", out)
		}
		if !strings.Contains(out, "Space toggle") {
			t.Errorf("expected space-toggle hint, got %q", out)
		}
	})
}

func TestHelpOverlay_Render(t *testing.T) {
	out := NewHelpOverlay(80).Render()
	for _, want := range []string{"Keybindings", "Navigation", "Actions", "Input", "Commands", "System", "Press any key to close"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in help overlay, got %q", want, out)
		}
	}
}

func TestTodoTable_Render(t *testing.T) {
	t.Run("empty renders nothing", func(t *testing.T) {
		out := NewTodoTable(nil, 72).Render()
		if out != "" {
			t.Errorf("expected empty render for no todos, got %q", out)
		}
	})

	t.Run("shows status icons and priorities", func(t *testing.T) {
		items := []todo.Item{
			{ID: "td-1", Content: "write tests", Status: "completed", Priority: "high"},
			{ID: "td-2", Content: "fix the bug", Status: "in_progress", Priority: "medium"},
			{ID: "td-3", Content: "refactor later", Status: "pending", Priority: "low"},
		}
		out := NewTodoTable(items, 72).Render()
		for _, want := range []string{"\u2705", "\U0001F504", "\u23F3", "HIGH", "MED", "LOW", "write tests", "fix the bug", "refactor later"} {
			if !strings.Contains(out, want) {
				t.Errorf("expected %q in table, got %q", want, out)
			}
		}
	})

	t.Run("has border and title", func(t *testing.T) {
		items := []todo.Item{
			{ID: "td-1", Content: "task", Status: "pending", Priority: "medium"},
		}
		out := NewTodoTable(items, 72).Render()
		if !strings.Contains(out, "📋 Tasks") {
			t.Errorf("expected title %q in table, got %q", "📋 Tasks", out)
		}
		if !strings.Contains(out, "TASK") {
			t.Errorf("expected header %q in table, got %q", "TASK", out)
		}
	})

	t.Run("long content is truncated not wrapped", func(t *testing.T) {
		items := []todo.Item{
			{ID: "td-1", Content: strings.Repeat("a very long task description ", 10), Status: "pending", Priority: "high"},
		}
		out := NewTodoTable(items, 52).Render()
		if !strings.Contains(out, "…") {
			t.Errorf("expected truncation ellipsis, got %q", out)
		}
	})
}

func TestRenderToolResult_Todowrite(t *testing.T) {
	m := &Model{
		width: 80,
		todos: []todo.Item{
			{ID: "td-1", Content: "ship the feature", Status: "in_progress", Priority: "high"},
		},
	}
	out := m.renderToolResult("todowrite", "Updated todo list: 1 total, 0 completed, 1 in progress, 0 pending")
	if !strings.Contains(out, "✓ Todo list updated") {
		t.Errorf("expected compact summary in tool result, got %q", out)
	}
	// The tool result should NOT contain a full table — the persistent
	// todo table renders separately in renderMessages().
	if strings.Contains(out, "ship the feature") {
		t.Errorf("expected no full table in tool result, got %q", out)
	}

	// Without a snapshot, falls back to the plain summary text.
	m.todos = nil
	out = m.renderToolResult("todowrite", "Updated todo list: 1 total")
	if !strings.Contains(out, "✓ Todo list updated") {
		t.Errorf("expected compact summary fallback, got %q", out)
	}
}

func TestHandleControlMsg_Todos(t *testing.T) {
	m := &Model{width: 80, height: 30}
	items := []todo.Item{
		{ID: "td-1", Content: "do a thing", Status: "in_progress", Priority: "high"},
	}
	m.handleControlMsg(&types.CtrlTodos{Items: items})
	if len(m.todos) != 1 {
		t.Fatalf("expected 1 todo stored, got %d", len(m.todos))
	}
	if m.todos[0].Content != "do a thing" {
		t.Errorf("expected todo content stored, got %q", m.todos[0].Content)
	}

	// Empty (non-nil) list clears the panel.
	m.handleControlMsg(&types.CtrlTodos{Items: []todo.Item{}})
	if len(m.todos) != 0 {
		t.Errorf("expected todos cleared, got %d", len(m.todos))
	}
}
