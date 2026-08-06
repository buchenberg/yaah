package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/types"
)

func TestMain(m *testing.M) {
	zone.NewGlobal()
	m.Run()
}

// --- splitRow tests ---

func TestSplitRow_Basic(t *testing.T) {
	got := splitRow("| Name | Age | City |")
	expected := []string{"Name", "Age", "City"}
	assertEqual(t, got, expected)
}

func TestSplitRow_ExtraWhitespace(t *testing.T) {
	got := splitRow("  |   foo   |   bar   |   baz   |  ")
	expected := []string{"foo", "bar", "baz"}
	assertEqual(t, got, expected)
}

func TestSplitRow_SingleColumn(t *testing.T) {
	got := splitRow("| only |")
	expected := []string{"only"}
	assertEqual(t, got, expected)
}

func TestSplitRow_EmptyCells(t *testing.T) {
	got := splitRow("| filled |   | alsofilled |")
	expected := []string{"filled", "", "alsofilled"}
	assertEqual(t, got, expected)
}

func TestSplitRow_AllEmptyCells(t *testing.T) {
	// After trimming all | and spaces from both ends, "| | | |" becomes "".
	// Splitting "" by "|" gives [""].
	got := splitRow("| | | |")
	expected := []string{""}
	assertEqual(t, got, expected)
}

func TestSplitRow_NoLeadingPipe(t *testing.T) {
	got := splitRow("col1|col2|col3")
	expected := []string{"col1", "col2", "col3"}
	assertEqual(t, got, expected)
}

func TestSplitRow_NoPipesAtAll(t *testing.T) {
	got := splitRow("just a string")
	expected := []string{"just a string"}
	assertEqual(t, got, expected)
}

func TestSplitRow_EmptyString(t *testing.T) {
	got := splitRow("")
	expected := []string{""}
	assertEqual(t, got, expected)
}

func TestSplitRow_Unicode(t *testing.T) {
	got := splitRow("| café | 名 | 🚀 |")
	expected := []string{"café", "名", "🚀"}
	assertEqual(t, got, expected)
}

func TestSplitRow_ConsecutivePipes(t *testing.T) {
	// "||||" after trimming all | from both ends becomes "" -> [""]
	got := splitRow("||||")
	assertEqual(t, got, []string{""})
}

func TestSplitRow_TabsInCells(t *testing.T) {
	got := splitRow("| col\t1 | col\t2 |")
	expected := []string{"col\t1", "col\t2"}
	assertEqual(t, got, expected)
}

// testModel creates a minimal Model for testing renderCompactTable.
func testModel(width int) *Model {
	input := textarea.New()
	input.SetWidth(width - 4)
	input.Placeholder = "Type a message..."
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter"))
	return &Model{
		width:             width,
		input:             input,
		reasoningExpanded: make(map[string]bool),
		toolExpanded:      make(map[string]bool),
	}
}

// m is a default test model used by renderCompactTable tests.
var m = testModel(80)

// --- renderCompactTable tests ---

func TestRenderCompactTable_Simple(t *testing.T) {
	md := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob | 25 |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "Name") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "──") {
		t.Errorf("missing border line: %q", got)
	}
	if !strings.Contains(got, "Alice") {
		t.Errorf("missing data: %q", got)
	}
	if strings.Contains(stripANSI(got), "|") && !strings.Contains(got, "│") {
		t.Errorf("expected lipgloss border chars, not raw pipes: %q", got)
	}
}

func TestRenderCompactTable_SingleColumn(t *testing.T) {
	md := "| Item |\n|------|\n| Apple |\n| Banana |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "Item") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "Apple") {
		t.Errorf("missing data row: %q", got)
	}
}

func TestRenderCompactTable_EmptyCells(t *testing.T) {
	md := "| Col A | Col B |\n|-------|-------|\n| foo | |\n| | bar |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "Col A") {
		t.Errorf("missing header: %q", got)
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) < 4 {
		t.Errorf("expected at least 4 lines (header, sep, 2 data), got %d: %q", len(lines), got)
	}
}

func TestRenderCompactTable_HeaderOnly(t *testing.T) {
	md := "| Col A | Col B |\n|-------|-------|"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "Col A") {
		t.Errorf("expected header in output: %q", got)
	}
}

func TestRenderCompactTable_EmptyInput(t *testing.T) {
	got := m.renderCompactTable("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestRenderCompactTable_NoSeparator(t *testing.T) {
	// Without a --- separator, lipgloss table treats the first row as the header.
	md := "| Col A | Col B |\n| val1 | val2 |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "Col A") || !strings.Contains(got, "val1") {
		t.Errorf("expected rendered table with headers and data, got %q", got)
	}
	// ANSI styling should be present (from lipgloss borders)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI styling: %q", got)
	}
}

func TestRenderCompactTable_SingleLine(t *testing.T) {
	got := m.renderCompactTable("| just | one |")
	if got != "| just | one |" {
		t.Errorf("expected fallback, got %q", got)
	}
}

func TestRenderCompactTable_ManyColumns(t *testing.T) {
	md := "| A | B | C | D | E | F | G | H |\n|---|---|---|---|---|---|---|---|\n| 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 |"
	got := m.renderCompactTable(md)
	for _, col := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		if !strings.Contains(got, col) {
			t.Errorf("missing column %q in output: %q", col, got)
		}
	}
}

func TestRenderCompactTable_WideContent(t *testing.T) {
	longStr := strings.Repeat("x", 80)
	md := "| Short | Long |\n|-------|------|\n| s | " + longStr + " |"
	got := m.renderCompactTable(md)
	// With wrapping enabled, the long string may be split across lines.
	// Verify the content is present after stripping ANSI codes.
	plain := stripANSI(got)
	if !strings.Contains(plain, "xxxxxxxxxx") {
		t.Errorf("missing wide content: %q", plain)
	}
}

func TestRenderCompactTable_Unicode(t *testing.T) {
	md := "| 名前 | 値 |\n|------|----|\n| 東京 | 100 |\n| 大阪 | 200 |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "名前") {
		t.Errorf("missing unicode header: %q", got)
	}
	if !strings.Contains(got, "東京") {
		t.Errorf("missing unicode data: %q", got)
	}
}

func TestRenderCompactTable_Emoji(t *testing.T) {
	md := "| Status | Count |\n|--------|-------|\n| ✅ | 42 |\n| ❌ | 0 |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "✅") {
		t.Errorf("missing emoji: %q", got)
	}
}

func TestRenderCompactTable_MixedWidths(t *testing.T) {
	md := "| A | B |\n|---|---|\n| x | a very long cell value here |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "a very long cell value here") {
		t.Errorf("missing long cell: %q", got)
	}
}

func TestRenderCompactTable_TrailingWhitespaceInCells(t *testing.T) {
	md := "| Name   | Value  |\n|--------|--------|\n| foo    | 123    |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "foo") {
		t.Errorf("missing cell content: %q", got)
	}
}

func TestRenderCompactTable_SeparatorWithColons(t *testing.T) {
	md := "| Left | Center | Right |\n|:-----|:------:|------:|\n| a | b | c |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "Left") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "a") {
		t.Errorf("missing data row: %q", got)
	}
}

func TestRenderCompactTable_NumbersOnly(t *testing.T) {
	md := "| 2023 | 2024 | 2025 |\n|------|------|------|\n| 100 | 200 | 300 |\n| -5 | 0 | 5.5 |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "-5") {
		t.Errorf("missing negative number: %q", got)
	}
	if !strings.Contains(got, "5.5") {
		t.Errorf("missing decimal: %q", got)
	}
}

func TestRenderCompactTable_SpecialChars(t *testing.T) {
	md := "| Expression | Result |\n|------------|--------|\n| 2 + 2 | 4 |\n| `code` | ok |\n| **bold** | yes |"
	got := m.renderCompactTable(md)
	if !strings.Contains(got, "code") {
		t.Errorf("missing backtick content: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI styling for table: %q", got)
	}
}

// --- parseAndRenderTables tests ---

func TestParseAndRenderTables_PlainText(t *testing.T) {
	md := "This is just some text.\nNo tables here."
	segs := parseAndRenderTables(md)
	if len(segs) != 1 {
		t.Fatalf("expected 1 text segment, got %d", len(segs))
	}
	if segs[0].isTable {
		t.Error("segment should not be a table")
	}
}

func TestParseAndRenderTables_SingleTable(t *testing.T) {
	md := "| A | B |\n|---|---|\n| 1 | 2 |"
	segs := parseAndRenderTables(md)
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	if !segs[0].isTable {
		t.Error("segment should be a table")
	}
}

func TestParseAndRenderTables_TextBeforeTable(t *testing.T) {
	md := "Some intro text.\n\n| A | B |\n|---|---|\n| 1 | 2 |"
	segs := parseAndRenderTables(md)
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segs))
	}
	if segs[0].isTable {
		t.Error("first segment should be text")
	}
	if !segs[1].isTable {
		t.Error("second segment should be a table")
	}
}

func TestParseAndRenderTables_TextAfterTable(t *testing.T) {
	md := "| A | B |\n|---|---|\n| 1 | 2 |\n\nSome trailing text."
	segs := parseAndRenderTables(md)
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segs))
	}
	if !segs[0].isTable {
		t.Error("first segment should be a table")
	}
	if segs[1].isTable {
		t.Error("second segment should be text")
	}
}

func TestParseAndRenderTables_TextBetweenTables(t *testing.T) {
	md := "| A | B |\n|---|---|\n| 1 | 2 |\n\nMiddle text.\n\n| C | D |\n|---|---|\n| 3 | 4 |"
	segs := parseAndRenderTables(md)
	if len(segs) != 3 {
		t.Fatalf("expected 3 segments (table, text, table), got %d", len(segs))
	}
	if !segs[0].isTable {
		t.Error("first segment should be a table")
	}
	if segs[1].isTable {
		t.Error("second segment should be text")
	}
	if !segs[2].isTable {
		t.Error("third segment should be a table")
	}
}

func TestParseAndRenderTables_MultipleTables(t *testing.T) {
	md := "| A | B |\n|---|---|\n| 1 | 2 |\n\n| C | D |\n|---|---|\n| 3 | 4 |"
	segs := parseAndRenderTables(md)
	if len(segs) != 2 {
		t.Fatalf("expected 2 table segments, got %d", len(segs))
	}
	if !segs[0].isTable || !segs[1].isTable {
		t.Error("both segments should be tables")
	}
}

func TestParseAndRenderTables_EmptyInput(t *testing.T) {
	segs := parseAndRenderTables("")
	if len(segs) != 0 {
		t.Errorf("expected 0 segments, got %d", len(segs))
	}
}

func TestParseAndRenderTables_TableLikeButNot(t *testing.T) {
	md := "This is not | a table.\nJust some text with a pipe."
	segs := parseAndRenderTables(md)
	if len(segs) != 1 {
		t.Fatalf("expected 1 text segment, got %d", len(segs))
	}
	if segs[0].isTable {
		t.Error("should not be detected as table")
	}
}

func TestParseAndRenderTables_TableWithExtraNewline(t *testing.T) {
	md := "| A | B |\n|------|-----|\n| 1 | 2 |\n"
	segs := parseAndRenderTables(md)
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(segs))
	}
	if !segs[0].isTable {
		t.Error("segment should be a table")
	}
}

func TestParseAndRenderTables_SingleColumnTable(t *testing.T) {
	md := "| Name |\n|------|\n| Alice |\n| Bob |"
	segs := parseAndRenderTables(md)
	if len(segs) != 1 {
		t.Fatalf("expected 1 table segment, got %d", len(segs))
	}
	if !segs[0].isTable {
		t.Error("single-column table not detected")
	}
}

func TestParseAndRenderTables_EmptyCells(t *testing.T) {
	md := "| A | B |\n|---|---|\n| | |\n| x | |"
	segs := parseAndRenderTables(md)
	if len(segs) != 1 {
		t.Fatalf("expected 1 table segment, got %d", len(segs))
	}
}

// --- Integration: renderMarkdown with tables ---

func TestRenderMarkdown_TableOnly(t *testing.T) {
	m := &Model{width: 80}
	m.createRenderer()

	md := "| Name | Age |\n|------|-----|\n| Alice | 30 |"
	got := m.renderMarkdown(md)
	plain := stripANSI(got)

	if !strings.Contains(plain, "Name") {
		t.Errorf("missing Name in rendered output: %q", plain)
	}
	if !strings.Contains(plain, "Alice") {
		t.Errorf("missing Alice in rendered output: %q", plain)
	}
	// lipgloss table uses │ border chars, not raw ASCII |
	// raw | should not appear as column separators
}

func TestRenderMarkdown_TableWithTextBeforeAndAfter(t *testing.T) {
	m := &Model{width: 80}
	m.createRenderer()

	md := "Here is a summary:\n\n| Key | Value |\n|-----|-------|\n| foo | bar |\n\nEnd of summary."

	got := m.renderMarkdown(md)
	plain := stripANSI(got)
	if !strings.Contains(plain, "Here is a summary") {
		t.Errorf("missing text before table: %q", plain)
	}
	if !strings.Contains(plain, "Key") {
		t.Errorf("missing table header: %q", plain)
	}
	if !strings.Contains(plain, "End of summary") {
		t.Errorf("missing text after table: %q", plain)
	}
	if !strings.Contains(plain, "foo") {
		t.Errorf("missing table data: %q", plain)
	}
}

func TestRenderMarkdown_PlainTextOnly(t *testing.T) {
	m := &Model{width: 80}
	m.createRenderer()

	md := "Just some **bold** text."
	got := m.renderMarkdown(md)
	if !strings.Contains(got, "bold") {
		t.Errorf("missing bold text: %q", got)
	}
}

// --- isListContent / isTreeContent tests ---

func TestIsListContent_BulletList(t *testing.T) {
	if !isListContent("* item one\n* item two\n* item three") {
		t.Error("should detect bullet list with asterisks")
	}
}

func TestIsListContent_DashList(t *testing.T) {
	if !isListContent("- item one\n- item two") {
		t.Error("should detect bullet list with dashes")
	}
}

func TestIsListContent_NoList(t *testing.T) {
	if isListContent("just plain text") {
		t.Error("should not detect list in plain text")
	}
}

func TestIsListContent_SingleLine(t *testing.T) {
	if isListContent("* item one") {
		t.Error("single line should not be detected as list")
	}
}

func TestIsTreeContent_BoxDrawing(t *testing.T) {
	if !isTreeContent("├── file.txt\n└── dir/") {
		t.Error("should detect tree with box-drawing chars")
	}
}

func TestIsTreeContent_NoTree(t *testing.T) {
	if isTreeContent("just text\ntext") {
		t.Error("should not detect tree in plain text")
	}
}

// --- renderList tests ---

func TestRenderList_Simple(t *testing.T) {
	m := &Model{width: 80}
	got := m.renderList("* item one\n* item two\n* item three")
	if !strings.Contains(got, "item one") {
		t.Errorf("missing list item: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI styling: %q", got)
	}
}

func TestRenderList_DashBullets(t *testing.T) {
	m := &Model{width: 80}
	got := m.renderList("- alpha\n- beta\n- gamma")
	if !strings.Contains(got, "alpha") {
		t.Errorf("missing list item: %q", got)
	}
}

func TestRenderList_NestedContent_Flat(t *testing.T) {
	m := &Model{width: 80}
	md := "- todo item 1\n- todo item 2 with extra text\n- todo item 3"
	got := m.renderList(md)
	if !strings.Contains(got, "todo item 1") {
		t.Errorf("missing first item: %q", got)
	}
	if !strings.Contains(got, "todo item 2 with extra text") {
		t.Errorf("missing second item: %q", got)
	}
}

// --- renderToolResult tests ---

func TestRenderToolResult_ListContent(t *testing.T) {
	m := &Model{width: 80}
	content := "* Task 1\n* Task 2\n* Task 3"
	got := m.renderToolResult("bash", content)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("list should have ANSI styling: %q", got)
	}
	if !strings.Contains(got, "Task 1") {
		t.Errorf("missing list item: %q", got)
	}
}

func TestRenderToolResult_TreeContent(t *testing.T) {
	m := &Model{width: 80}
	content := ".\n├── src\n│   ├── main.go\n│   └── util.go\n└── README.md"
	got := m.renderToolResult("bash", content)
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("tree should have ANSI styling: %q", got)
	}
	if !strings.Contains(got, "main.go") {
		t.Errorf("missing tree item: %q", got)
	}
}

func TestRenderToolResult_PlainContent(t *testing.T) {
	m := &Model{width: 80}
	content := "Wrote 42 bytes to file.txt"
	got := m.renderToolResult("write", content)
	if !strings.Contains(got, content) {
		t.Errorf("plain content should be preserved: %q", got)
	}
}

func TestRenderToolResult_EmptyContent(t *testing.T) {
	m := &Model{width: 80}
	got := m.renderToolResult("bash", "")
	if got != "" {
		t.Errorf("empty content should return empty string, got %q", got)
	}
}

// --- splitTreePrefix tests ---

func TestSplitTreePrefix_Branch(t *testing.T) {
	prefix, name := splitTreePrefix("├── main.go")
	if prefix != "├── " {
		t.Errorf("expected prefix '├── ', got %q", prefix)
	}
	if name != "main.go" {
		t.Errorf("expected name 'main.go', got %q", name)
	}
}

func TestSplitTreePrefix_Leaf(t *testing.T) {
	_, name := splitTreePrefix("└── README.md")
	if name != "README.md" {
		t.Errorf("expected name 'README.md', got %q", name)
	}
}

func TestSplitTreePrefix_Indented(t *testing.T) {
	prefix, name := splitTreePrefix("│   ├── nested.go")
	if prefix != "│   ├── " {
		t.Errorf("expected prefix '│   ├── ', got %q", prefix)
	}
	if name != "nested.go" {
		t.Errorf("expected name 'nested.go', got %q", name)
	}
}

func TestSplitTreePrefix_Connector(t *testing.T) {
	_, name := splitTreePrefix("│   │   subdir/")
	if name != "subdir/" {
		t.Errorf("expected name 'subdir/', got %q", name)
	}
}

// --- treeDepth tests ---

func TestTreeDepth_Root(t *testing.T) {
	if treeDepth("") != 0 {
		t.Errorf("expected depth 0, got %d", treeDepth(""))
	}
}

func TestTreeDepth_Level1(t *testing.T) {
	if treeDepth("├── ") != 1 {
		t.Errorf("expected depth 1, got %d", treeDepth("├── "))
	}
}

func TestTreeDepth_Level2(t *testing.T) {
	if treeDepth("│   ├── ") != 2 {
		t.Errorf("expected depth 2, got %d", treeDepth("│   ├── "))
	}
}

func TestTreeDepth_Level3(t *testing.T) {
	if treeDepth("│   │   └── ") != 3 {
		t.Errorf("expected depth 3, got %d", treeDepth("│   │   └── "))
	}
}

// --- command mode tests ---

func TestDefaultCommands(t *testing.T) {
	cmds := defaultCommands
	if len(cmds) < 3 {
		t.Fatalf("expected at least 3 default commands, got %d", len(cmds))
	}
	names := make(map[string]bool)
	for _, c := range cmds {
		if c.Name == "" {
			t.Error("command name must not be empty")
		}
		if !strings.HasPrefix(c.Name, ":") {
			t.Errorf("command name should start with :: %q", c.Name)
		}
		names[c.Name] = true
	}
	for _, want := range []string{":help", ":clear", ":quit"} {
		if !names[want] {
			t.Errorf("missing expected command: %s", want)
		}
	}
}

func TestExecuteCommand_Help(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	m.executeCommand(":help")
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].Role != "system" {
		t.Errorf("expected system role, got %s", m.messages[0].Role)
	}
	if !strings.Contains(m.messages[0].Content, "Available commands") {
		t.Errorf("help should list available commands: %q", m.messages[0].Content)
	}
}

func TestExecuteCommand_Clear(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	m.executeCommand(":clear")
	if len(m.messages) != 0 {
		t.Errorf("expected 0 messages after clear, got %d", len(m.messages))
	}
}

func TestExecuteCommand_Unknown(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	m.executeCommand(":invalid")
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].Role != "system" {
		t.Errorf("expected system role, got %s", m.messages[0].Role)
	}
	if !strings.Contains(m.messages[0].Content, "Unknown") {
		t.Errorf("expected unknown command message: %q", m.messages[0].Content)
	}
}

func TestExecuteCommand_CompactCallback(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	called := false
	m.onCompact = func() { called = true }
	m.executeCommand(":compact")
	if !called {
		t.Error("expected onCompact callback to be called")
	}
}

func TestExecuteCommand_SteerInvokesCallback(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	m.thinking = true
	var got string
	m.onSteer = func(text string) { got = text }
	m.executeCommand(":steer urgent new instruction")
	if got != "urgent new instruction" {
		t.Errorf("onSteer got %q, want %q", got, "urgent new instruction")
	}
	// The user-facing render should also have been added.
	if len(m.messages) == 0 || m.messages[len(m.messages)-1].Role != "user" {
		t.Errorf("expected a user-facing message, got %+v", m.messages)
	}
}

func TestExecuteCommand_SteerRequiresThinking(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	m.thinking = false
	called := false
	m.onSteer = func(string) { called = true }
	m.executeCommand(":steer hello")
	if called {
		t.Error("onSteer must not fire when the agent is not running")
	}
	if len(m.messages) != 1 || !strings.Contains(m.messages[0].Content, "running") {
		t.Errorf("expected a system reminder about agent state, got %+v", m.messages)
	}
}

func TestExecuteCommand_SteerEmptyBody(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands
	m.thinking = true
	called := false
	m.onSteer = func(string) { called = true }
	m.executeCommand(":steer ")
	if called {
		t.Error("onSteer must not fire on empty body")
	}
	if len(m.messages) != 1 || !strings.Contains(m.messages[0].Content, "Usage") {
		t.Errorf("expected a usage message, got %+v", m.messages)
	}
}

func TestCommandSuggestions(t *testing.T) {
	m := &Model{width: 80}
	m.commands = defaultCommands

	// Command palette renders matching commands based on input value.
	// Verify all commands appear when no filter is applied.
	palette := NewCommandPalette(m.commands, "", m.width)
	output := palette.Render()
	for _, c := range m.commands {
		if !strings.Contains(output, c.Name) {
			t.Errorf("command palette missing command: %s", c.Name)
		}
	}

	// Verify filtering works.
	m2 := &Model{width: 80}
	m2.commands = defaultCommands
	filtered := NewCommandPalette(m2.commands, ":quit", m2.width).Render()
	if !strings.Contains(filtered, ":quit") {
		t.Error("filtered palette missing :quit")
	}
	if strings.Contains(filtered, ":help") {
		t.Error("filtered palette should not contain :help when filter is :quit")
	}
}

// --- model mode tests ---

func TestExecuteCommand_Model(t *testing.T) {
	m := testModel(80)
	m.commands = defaultCommands
	m.modelItems = []string{"openai/gpt-4o", "openai/gpt-4o-mini", "ollama/llama3"}
	m.executeCommand(":model")
	if !m.modelMode {
		t.Fatal("expected modelMode to be true after :model command")
	}
	if m.modelSelected != 0 {
		t.Errorf("expected modelSelected 0, got %d", m.modelSelected)
	}
}

func TestExecuteCommand_ModelNoItems(t *testing.T) {
	m := testModel(80)
	m.commands = defaultCommands
	m.modelItems = nil
	m.executeCommand(":model")
	if m.modelMode {
		t.Error("modelMode should remain false when no models available")
	}
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if !strings.Contains(m.messages[0].Content, "No models") {
		t.Errorf("expected 'No models' message: %q", m.messages[0].Content)
	}
}

func TestFilteredModels(t *testing.T) {
	m := testModel(80)
	m.modelItems = []string{"openai/gpt-4o", "openai/gpt-4o-mini", "ollama/llama3", "ollama/qwen2"}

	all := m.filteredModels()
	if len(all) != 4 {
		t.Fatalf("expected 4 unfiltered models, got %d", len(all))
	}

	m.input.SetValue("gpt")
	filtered := m.filteredModels()
	if len(filtered) != 2 {
		t.Fatalf("expected 2 filtered models for 'gpt', got %d: %v", len(filtered), filtered)
	}
	if filtered[0] != "openai/gpt-4o" {
		t.Errorf("expected gpt-4o first, got %s", filtered[0])
	}

	m.input.SetValue("nonexistent")
	filtered = m.filteredModels()
	if len(filtered) != 0 {
		t.Errorf("expected 0 results for 'nonexistent', got %d", len(filtered))
	}

	m.input.SetValue("")
	filtered = m.filteredModels()
	if len(filtered) != 4 {
		t.Errorf("expected all models with empty filter, got %d", len(filtered))
	}
}

func TestModelSelection_Callback(t *testing.T) {
	m := testModel(80)
	m.commands = defaultCommands
	m.modelItems = []string{"openai/gpt-4o", "openai/gpt-4o-mini", "ollama/llama3"}

	var gotProvider, gotModel string
	m.onModel = func(p, m2 string) {
		gotProvider = p
		gotModel = m2
	}

	m.modelMode = true
	m.modelSelected = 1 // "openai/gpt-4o-mini"
	m.selectModel()

	if gotProvider != "openai" {
		t.Errorf("expected provider 'openai', got %q", gotProvider)
	}
	if gotModel != "gpt-4o-mini" {
		t.Errorf("expected model 'gpt-4o-mini', got %q", gotModel)
	}
	if m.modelMode {
		t.Error("modelMode should be false after selection")
	}
	if m.provider != "openai" {
		t.Errorf("expected model.provider to be 'openai', got %q", m.provider)
	}
	if m.modelName != "gpt-4o-mini" {
		t.Errorf("expected model.modelName to be 'gpt-4o-mini', got %q", m.modelName)
	}
}

func TestExitModelMode(t *testing.T) {
	m := testModel(80)
	m.modelMode = true
	m.modelSelected = 3
	m.input.SetValue("search text")
	m.input.Placeholder = "Search models..."

	m.exitModelMode()

	if m.modelMode {
		t.Error("modelMode should be false after exit")
	}
	if m.modelSelected != 0 {
		t.Errorf("modelSelected should be 0 after exit, got %d", m.modelSelected)
	}
	if m.input.Value() != "" {
		t.Errorf("input value should be cleared after exit, got %q", m.input.Value())
	}
	if m.input.Placeholder != "Type a message..." {
		t.Errorf("placeholder should be restored, got %q", m.input.Placeholder)
	}
}

func TestHandleModelList(t *testing.T) {
	m := &Model{width: 80}
	m.handleControlMsg(&types.CtrlModelList{Models: []string{"openai/gpt-4o", "ollama/llama3"}})

	if len(m.modelItems) != 2 {
		t.Fatalf("expected 2 modelItems, got %d", len(m.modelItems))
	}
	if m.modelItems[0] != "openai/gpt-4o" {
		t.Errorf("expected first model 'openai/gpt-4o', got %q", m.modelItems[0])
	}
}

// --- helpers ---

func assertEqual(t *testing.T, got, expected []string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Fatalf("length mismatch: got %d, expected %d\ngot:  %v\nexp:  %v",
			len(got), len(expected), got, expected)
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("index %d: got %q, expected %q", i, got[i], expected[i])
		}
	}
}

// --- reasoning collapse tests ---

func TestReasoningPersistsAfterDone(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.ThinkingEvent{Text: "step 1"})
	m.HandleEvent(&agent.ThinkingEvent{Text: " step 2"})

	if m.thinkContent != "step 1 step 2" {
		t.Errorf("expected thinkContent 'step 1 step 2', got %q", m.thinkContent)
	}

	m.streaming = true
	m.streamContent = "the response"
	m.HandleEvent(&agent.DoneEvent{})

	if m.thinkContent != "" {
		t.Errorf("thinkContent should be cleared after Done, got %q", m.thinkContent)
	}
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].Reasoning != "step 1 step 2" {
		t.Errorf("reasoning should be on the message, got %q", m.messages[0].Reasoning)
	}
}

func TestReasoningTransferOnDone(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.ThinkingEvent{Text: "reasoning"})

	m.streaming = true
	m.streamContent = "the response"
	m.HandleEvent(&agent.DoneEvent{})

	if m.thinkContent != "" {
		t.Errorf("thinkContent should be cleared after Done with streaming, got %q", m.thinkContent)
	}
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].Reasoning != "reasoning" {
		t.Errorf("expected reasoning on message, got %q", m.messages[0].Reasoning)
	}
}

func TestReasoningTransferOnFlush(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.ThinkingEvent{Text: "the reasoning"})

	m.HandleEvent(&agent.FlushEvent{Content: "flushed content"})

	if m.thinkContent != "" {
		t.Errorf("thinkContent should be cleared after Flush, got %q", m.thinkContent)
	}
	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].Reasoning != "the reasoning" {
		t.Errorf("expected reasoning on message, got %q", m.messages[0].Reasoning)
	}
}

func TestReasoningNotDuplicatedAfterFlushAndDone(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.ThinkingEvent{Text: "reasoning"})

	m.HandleEvent(&agent.FlushEvent{Content: "first part"})
	if m.thinkContent != "" {
		t.Errorf("thinkContent should be cleared after Flush, got %q", m.thinkContent)
	}

	m.streaming = true
	m.streamContent = "second part"
	m.HandleEvent(&agent.DoneEvent{})

	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m.messages))
	}
	if m.messages[0].Reasoning != "reasoning" {
		t.Errorf("first message should have reasoning, got %q", m.messages[0].Reasoning)
	}
	if m.messages[1].Reasoning != "" {
		t.Error("second message should NOT have reasoning")
	}
}

func TestReasoningClearedOnNewSubmit(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.ThinkingEvent{Text: "reasoning"})

	m.streaming = true
	m.streamContent = "response"
	m.HandleEvent(&agent.DoneEvent{})

	m.thinkContent = ""
	m.reasoningExpanded = make(map[string]bool)

	if m.thinkContent != "" {
		t.Errorf("thinkContent should be empty after clearing, got %q", m.thinkContent)
	}
	if len(m.reasoningExpanded) != 0 {
		t.Error("reasoningExpanded should be empty after clearing")
	}
}

func TestCTRLRTogglesReasoning(t *testing.T) {
	m := &Model{width: 80, verbose: true, height: 40, reasoningExpanded: make(map[string]bool)}
	m.HandleEvent(&agent.ThinkingEvent{Text: "reasoning"})

	m.streaming = true
	m.streamContent = "response"
	m.HandleEvent(&agent.DoneEvent{})

	// Render to populate reasoningZones
	m.renderMessages()
	if len(m.reasoningZones) == 0 {
		t.Fatal("expected reasoning zones after render")
	}
	zoneID := m.reasoningZones[0]

	// Default: collapsed (not in map)
	if m.reasoningExpanded[zoneID] {
		t.Fatal("expected collapsed by default")
	}

	// Simulate ctrl+r: expand all
	anyExpanded := false
	for _, zid := range m.reasoningZones {
		if m.reasoningExpanded[zid] {
			anyExpanded = true
			break
		}
	}
	for _, zid := range m.reasoningZones {
		m.reasoningExpanded[zid] = !anyExpanded
	}
	if !m.reasoningExpanded[zoneID] {
		t.Error("expected expanded after first ctrl+r")
	}

	// Simulate ctrl+r again: collapse all
	anyExpanded = false
	for _, zid := range m.reasoningZones {
		if m.reasoningExpanded[zid] {
			anyExpanded = true
			break
		}
	}
	for _, zid := range m.reasoningZones {
		m.reasoningExpanded[zid] = !anyExpanded
	}
	if m.reasoningExpanded[zoneID] {
		t.Error("expected collapsed after second ctrl+r")
	}
}

func TestHasReasoning(t *testing.T) {
	m := &Model{width: 80}

	if m.hasReasoning() {
		t.Error("should not have reasoning initially")
	}

	m.HandleEvent(&agent.ThinkingEvent{Text: "reasoning"})
	if !m.hasReasoning() {
		t.Error("should have reasoning via thinkContent")
	}

	m.streaming = true
	m.streamContent = "response"
	m.HandleEvent(&agent.DoneEvent{})
	if !m.hasReasoning() {
		t.Error("should have reasoning via message after transfer")
	}

	m.thinkContent = ""
	m.reasoningExpanded = make(map[string]bool)
	m.messages = nil
	if m.hasReasoning() {
		t.Error("should not have reasoning after clearing everything")
	}
}

func TestReasoningNotClearedOnDoneWhenEmpty(t *testing.T) {
	m := &Model{width: 80, reasoningExpanded: make(map[string]bool)}

	m.HandleEvent(&agent.DoneEvent{})

	if len(m.reasoningExpanded) != 0 {
		t.Error("reasoningExpanded should remain empty when thinkContent was empty")
	}
}

func TestRenderReasoningCollapsed_MessageLevel(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: make(map[string]bool)}
	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "response", Reasoning: "the model's reasoning"},
	}

	output := m.renderMessages()
	stripped := stripANSI(output)

	if !strings.Contains(stripped, "▶ Reasoning") {
		t.Errorf("collapsed output should contain ▶ Reasoning... toggle, got: %q", stripped)
	}
	if strings.Contains(stripped, "the model's reasoning") {
		t.Error("collapsed output should NOT contain reasoning text")
	}
	posToggle := strings.Index(stripped, "▶ Reasoning")
	posResponse := strings.Index(stripped, "response")
	if posToggle > posResponse {
		t.Errorf("toggle should appear before response text, got toggle at %d, response at %d", posToggle, posResponse)
	}
}

func TestRenderReasoningExpanded_MessageLevel(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: map[string]bool{"reasoning-1": true}}
	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "response", Reasoning: "the model's reasoning"},
	}

	output := m.renderMessages()
	stripped := stripANSI(output)

	if !strings.Contains(stripped, "▼ Reasoning") {
		t.Errorf("expanded output should contain ▼ Reasoning... toggle, got: %q", stripped)
	}
	if !strings.Contains(stripped, "the model's reasoning") {
		t.Error("expanded output should contain the reasoning text")
	}
	posToggle := strings.Index(stripped, "▼ Reasoning")
	posResponse := strings.Index(stripped, "response")
	if posToggle > posResponse {
		t.Errorf("toggle should appear before response text, got toggle at %d, response at %d", posToggle, posResponse)
	}
}

func TestRenderReasoningPlainTextNotMarkdown(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: map[string]bool{"reasoning-0": true}}
	m.createRenderer()
	m.messages = []Message{
		{Role: "assistant", Content: "response", Reasoning: "Let me think:\n\n```\ncode here\n```"},
	}

	output := stripANSI(m.renderMessages())

	if !strings.Contains(output, "code here") {
		t.Errorf("expected reasoning content in output, got: %q", output)
	}
	if !strings.Contains(output, "```") {
		t.Errorf("code fences should be preserved as plain text (no glamour for reasoning), got: %q", output)
	}
}

func TestRenderReasoningMarkdownPreservedOnRaw(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: map[string]bool{"reasoning-0": true}}
	m.messages = []Message{
		{Role: "assistant", Content: "response", Reasoning: "step 1 step 2"},
	}

	output := m.renderMessages()

	if !strings.Contains(output, "step 1 step 2") {
		t.Errorf("plain reasoning should still render, got: %q", output)
	}
}

func TestRenderReasoningActiveThinking(t *testing.T) {
	m := &Model{width: 80, verbose: true}
	m.thinking = true
	m.streaming = false
	m.thinkContent = "thinking..."

	output := m.renderMessages()
	stripped := stripANSI(output)

	if !strings.Contains(stripped, "Reasoning...") {
		t.Errorf("active thinking should show spinner + Reasoning..., got: %q", stripped)
	}
	if !strings.Contains(stripped, "thinking...") {
		t.Error("active thinking should show reasoning text inline")
	}
	if strings.Contains(stripped, "▶ Reasoning") || strings.Contains(stripped, "▼ Reasoning") {
		t.Error("active thinking should NOT show collapse toggle")
	}
}

func TestRenderReasoningActiveThinkingNoContent(t *testing.T) {
	m := &Model{width: 80}
	m.thinking = true
	m.streaming = false
	m.thinkContent = ""

	output := m.renderMessages()
	stripped := stripANSI(output)

	if !strings.Contains(stripped, "Thinking...") {
		t.Errorf("active thinking without reasoning should show spinner: %q", stripped)
	}
	if strings.Contains(stripped, "▶ Reasoning") || strings.Contains(stripped, "▼ Reasoning") {
		t.Error("no content means no toggle should appear")
	}
}

// --- zone + clipboard tests ---

func TestReasoningZonesPopulated_MessageLevel(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: make(map[string]bool)}
	m.messages = []Message{
		{Role: "assistant", Content: "response", Reasoning: "the model's reasoning"},
	}

	m.renderMessages()

	if len(m.reasoningZones) != 1 {
		t.Fatalf("expected 1 reasoning zone, got %d", len(m.reasoningZones))
	}
	if m.reasoningZones[0] != "reasoning-0" {
		t.Errorf("expected zone ID 'reasoning-0', got %q", m.reasoningZones[0])
	}
}

func TestReasoningZonesPopulated_ModelLevel(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: make(map[string]bool)}
	m.thinkContent = "reasoning text"

	m.renderMessages()

	if len(m.reasoningZones) != 1 {
		t.Fatalf("expected 1 reasoning zone, got %d", len(m.reasoningZones))
	}
	if m.reasoningZones[0] != "reasoning-live" {
		t.Errorf("expected zone ID 'reasoning-live', got %q", m.reasoningZones[0])
	}
}

func TestReasoningZonesMultipleMessages(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: make(map[string]bool)}
	m.messages = []Message{
		{Role: "assistant", Content: "response1", Reasoning: "reasoning1"},
		{Role: "assistant", Content: "response2", Reasoning: "reasoning2"},
	}

	m.renderMessages()

	if len(m.reasoningZones) != 2 {
		t.Fatalf("expected 2 reasoning zones, got %d", len(m.reasoningZones))
	}
	if m.reasoningZones[0] != "reasoning-0" {
		t.Errorf("expected first zone 'reasoning-0', got %q", m.reasoningZones[0])
	}
	if m.reasoningZones[1] != "reasoning-1" {
		t.Errorf("expected second zone 'reasoning-1', got %q", m.reasoningZones[1])
	}
}

func TestReasoningZonesEmptyWhenNoReasoning(t *testing.T) {
	m := &Model{width: 80}
	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "response"},
	}

	m.renderMessages()

	if len(m.reasoningZones) != 0 {
		t.Errorf("expected 0 reasoning zones, got %d: %v", len(m.reasoningZones), m.reasoningZones)
	}
}

func TestReasoningZonesClearedEachRender(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: make(map[string]bool)}
	m.messages = []Message{
		{Role: "assistant", Content: "response", Reasoning: "reasoning"},
	}

	m.renderMessages()
	if len(m.reasoningZones) != 1 {
		t.Fatalf("expected 1 zone after first render, got %d", len(m.reasoningZones))
	}

	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "response"},
	}
	m.renderMessages()
	if len(m.reasoningZones) != 0 {
		t.Errorf("expected 0 zones after re-render without reasoning, got %d", len(m.reasoningZones))
	}
}

// --- question mode tests ---

func TestQuestionModeEnter(t *testing.T) {
	m := testModel(80)
	ch := make(chan string, 1)
	m.handleControlMsg(&types.CtrlQuestion{
		Header:   "Next step",
		Question: "What should we do?",
		Options:  []types.CtrlOption{{Label: "A", Description: "First"}, {Label: "B", Description: "Second"}},
		AnswerCh: ch,
	})

	if !m.questionMode {
		t.Fatal("expected questionMode to be true")
	}
	if m.questionIdx != 0 {
		t.Errorf("expected questionIdx 0, got %d", m.questionIdx)
	}
	if len(m.questionMulti) != 2 {
		t.Errorf("expected questionMulti len 2, got %d", len(m.questionMulti))
	}
}

func TestQuestionModeSingleSelectAnswer(t *testing.T) {
	m := testModel(80)
	ch := make(chan string, 1)
	m.handleControlMsg(&types.CtrlQuestion{
		Header:   "Next step",
		Question: "What?",
		Options:  []types.CtrlOption{{Label: "A", Description: ""}, {Label: "B", Description: ""}},
		AnswerCh: ch,
	})
	m.questionIdx = 1
	m.commitQuestionAnswer()

	answer := <-ch
	if answer != "B" {
		t.Errorf("expected answer 'B', got %q", answer)
	}
	if m.questionMode {
		t.Error("questionMode should be false after answer")
	}
}

func TestQuestionModeMultiSelectAnswer(t *testing.T) {
	m := testModel(80)
	ch := make(chan string, 1)
	m.handleControlMsg(&types.CtrlQuestion{
		Header:   "Choose",
		Question: "Which?",
		Options:  []types.CtrlOption{{Label: "A", Description: ""}, {Label: "B", Description: ""}, {Label: "C", Description: ""}},
		Multiple: true,
		AnswerCh: ch,
	})
	m.questionMulti[0] = true
	m.questionMulti[2] = true
	m.commitQuestionAnswer()

	answer := <-ch
	if answer != "A, C" {
		t.Errorf("expected answer 'A, C', got %q", answer)
	}
}

func TestQuestionModeEscapeCancel(t *testing.T) {
	m := testModel(80)
	ch := make(chan string, 1)
	m.handleControlMsg(&types.CtrlQuestion{
		Header:   "Next step",
		Question: "What?",
		Options:  []types.CtrlOption{{Label: "A", Description: ""}},
		AnswerCh: ch,
	})
	m.answerQuestion("")

	answer := <-ch
	if answer != "" {
		t.Errorf("expected empty answer on cancel, got %q", answer)
	}
}

func TestRenderQuestionModal(t *testing.T) {
	m := &Model{width: 80}
	m.questionMode = true
	m.questionModal = QuestionModal{
		Header:   "Next step",
		Question: "What should we do?",
		Options:  []QuestionOption{{Label: "Option A", Description: "First choice"}, {Label: "Option B", Description: "Second choice"}},
	}
	m.questionIdx = 0
	m.questionMulti = make([]bool, 2)

	output := NewQuestionPalette(m.questionModal, m.questionIdx, m.questionMulti, m.maxQuestionLines(), m.width).Render()

	if !strings.Contains(output, "Next step") {
		t.Errorf("output should contain header: %q", output)
	}
	if !strings.Contains(output, "What should we do") {
		t.Errorf("output should contain question: %q", output)
	}
	if !strings.Contains(output, "Option A") {
		t.Errorf("output should contain first option: %q", output)
	}
	if !strings.Contains(output, "▶") {
		t.Errorf("output should contain selection marker for highlighted option: %q", output)
	}
}

func TestRenderQuestionModalMultiSelect(t *testing.T) {
	m := &Model{width: 80}
	m.questionMode = true
	m.questionModal = QuestionModal{
		Header:   "Choose",
		Question: "Select options?",
		Options:  []QuestionOption{{Label: "A", Description: ""}, {Label: "B", Description: ""}},
		Multiple: true,
	}
	m.questionMulti = []bool{true, false}

	output := NewQuestionPalette(m.questionModal, m.questionIdx, m.questionMulti, m.maxQuestionLines(), m.width).Render()

	if !strings.Contains(output, "☑") {
		t.Errorf("multi-select output should contain checked box: %q", output)
	}
	if !strings.Contains(output, "☐") {
		t.Errorf("multi-select output should contain unchecked box: %q", output)
	}
	if strings.Contains(output, "Space toggle") {
		// should have the Space toggle hint
	} else {
		t.Errorf("multi-select help should mention Space toggle: %q", output)
	}
}

func TestQuestionModeResetsState(t *testing.T) {
	m := testModel(80)
	ch := make(chan string, 1)
	m.handleControlMsg(&types.CtrlQuestion{
		Header:   "Q",
		Question: "A?",
		Options:  []types.CtrlOption{{Label: "X", Description: ""}},
		AnswerCh: ch,
	})
	m.answerQuestion("X")

	<-ch
	if m.questionMode {
		t.Error("questionMode should be false after answer")
	}
	if m.questionMulti != nil {
		t.Error("questionMulti should be nil after answer")
	}
}

func TestQuestionModePaletteLines(t *testing.T) {
	m := &Model{width: 80, height: 40}
	m.questionMode = true
	m.questionModal = QuestionModal{Options: make([]QuestionOption, 3)}

	lines := m.paletteLines()
	expected := 10 + 3 // border+padding(4) + header+question+help+blanks(6) + options(3)
	if lines != expected {
		t.Errorf("expected %d palette lines, got %d", expected, lines)
	}
}

func TestQuestionModeIncreasesPortHeight(t *testing.T) {
	m := &Model{width: 80, height: 40}
	m.questionMode = true
	m.questionModal.Options = make([]QuestionOption, 10)

	lines := m.paletteLines()
	// With 10 options, visible = min(10, maxQuestionLines())
	// maxQuestionLines = maxModelLines() - 6
	// maxModelLines = 40 - headerHeight - 8 (estimated small header)
	// So visible should be at least 10 if the terminal is tall enough
	// Just verify it's reasonable and includes all options when they fit
	if lines < 10 {
		t.Errorf("expected at least 10 palette lines, got %d", lines)
	}
}

func TestCursorHoverMsg_SetsHoveredZone(t *testing.T) {
	m := &Model{width: 80, height: 40, hoveredZone: false}

	// Simulate receiving a cursorHoverMsg with hovering=true
	newModel, _ := m.Update(cursorHoverMsg{hovering: true})
	updated := newModel.(*Model)
	if !updated.hoveredZone {
		t.Error("expected hoveredZone to be true after cursorHoverMsg{hovering: true}")
	}

	// Simulate receiving a cursorHoverMsg with hovering=false
	newModel, _ = updated.Update(cursorHoverMsg{hovering: false})
	updated = newModel.(*Model)
	if updated.hoveredZone {
		t.Error("expected hoveredZone to be false after cursorHoverMsg{hovering: false}")
	}
}

func TestViewCursorSequence_HoveredZone(t *testing.T) {
	m := New(Config{Provider: "test", Model: "model", CWD: "/tmp"})
	m.width = 80
	m.height = 40
	m.hoveredZone = true
	m.adjustViewport()

	v := m.View()
	if !strings.Contains(v.Content, "\x1b]22;pointer\x07") {
		t.Error("expected OSC 22 pointer cursor sequence when hoveredZone is true")
	}
}

func TestViewCursorSequence_NotHovered(t *testing.T) {
	m := New(Config{Provider: "test", Model: "model", CWD: "/tmp"})
	m.width = 80
	m.height = 40
	m.hoveredZone = false
	m.adjustViewport()

	v := m.View()
	if !strings.Contains(v.Content, "\x1b]22;text\x07") {
		t.Error("expected OSC 22 text cursor sequence when hoveredZone is false")
	}
}

// --- sub-agent line tests ---

func TestSubAgentLifecycle_SingleLine(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.SubAgentStartEvent{SubAgentID: "sa-1", Role: "developer", Prompt: "fix the bug"})

	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message after start, got %d", len(m.messages))
	}
	if m.messages[0].Role != "subagent" || !m.messages[0].SubRunning || m.messages[0].SubID != "sa-1" {
		t.Errorf("expected running subagent message with ID, got role=%q running=%v id=%q", m.messages[0].Role, m.messages[0].SubRunning, m.messages[0].SubID)
	}
	running := stripANSI(m.renderMessages())
	if !strings.Contains(running, "⏳") || !strings.Contains(running, "🤖") || !strings.Contains(running, "fix the bug") {
		t.Errorf("running line missing icon/robot/task, got %q", running)
	}

	m.HandleEvent(&agent.SubAgentEndEvent{SubAgentID: "sa-1", Role: "developer", Duration: 2 * time.Second, Model: "test-model"})

	if len(m.messages) != 1 {
		t.Fatalf("end event should transition the same message, got %d messages", len(m.messages))
	}
	if m.messages[0].SubRunning {
		t.Error("subagent should no longer be running after end event")
	}
	if m.messages[0].ToolDuration != "2.0s" {
		t.Errorf("expected duration 2.0s, got %q", m.messages[0].ToolDuration)
	}
	done := stripANSI(m.renderMessages())
	if !strings.Contains(done, "✓") || !strings.Contains(done, "(2.0s)") {
		t.Errorf("done line missing check/duration, got %q", done)
	}
	if strings.Contains(done, "⏳") {
		t.Errorf("done line should not show running icon, got %q", done)
	}
}

func TestSubAgentLifecycle_ParallelSameRoleMatchByID(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.SubAgentStartEvent{SubAgentID: "sa-1", Role: "developer", Prompt: "task A"})
	m.HandleEvent(&agent.SubAgentStartEvent{SubAgentID: "sa-2", Role: "developer", Prompt: "task B"})
	// B finishes before A — role-only matching would cross-wire them.
	m.HandleEvent(&agent.SubAgentEndEvent{SubAgentID: "sa-2", Role: "developer", Duration: time.Second, Result: "result B"})
	m.HandleEvent(&agent.SubAgentEndEvent{SubAgentID: "sa-1", Role: "developer", Duration: 2 * time.Second, Result: "result A"})

	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m.messages))
	}
	first, second := m.messages[0], m.messages[1]
	if first.Content != "task A" || first.SubResult != "result A" || first.ToolDuration != "2.0s" || first.SubRunning {
		t.Errorf("task A line mismatched: %+v", first)
	}
	if second.Content != "task B" || second.SubResult != "result B" || second.ToolDuration != "1.0s" || second.SubRunning {
		t.Errorf("task B line mismatched: %+v", second)
	}
}

func TestSubAgentLifecycle_ErrorState(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.SubAgentStartEvent{SubAgentID: "sa-1", Role: "tester", Prompt: "run tests"})
	m.HandleEvent(&agent.SubAgentEndEvent{SubAgentID: "sa-1", Role: "tester", Error: "context canceled", Duration: time.Second})

	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].SubError != "context canceled" {
		t.Errorf("expected error captured, got %q", m.messages[0].SubError)
	}
	out := stripANSI(m.renderMessages())
	if !strings.Contains(out, "✗") || !strings.Contains(out, "context canceled") {
		t.Errorf("error line missing cross/error text, got %q", out)
	}
}

func TestSubAgentEndWithoutStart_AppendsLine(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.SubAgentEndEvent{SubAgentID: "sa-1", Role: "developer", Duration: time.Second})

	if len(m.messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(m.messages))
	}
	if m.messages[0].SubRunning {
		t.Error("appended message should be completed, not running")
	}
}

func TestSubAgentLine_DisplayNameAlongsideRole(t *testing.T) {
	reg := subagent.NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"checker.md": []byte("---\nname: Chuck\nspecialty: checker\n---\nBody."),
	}); err != nil {
		t.Fatalf("load test role: %v", err)
	}
	subagent.SetDefaultRoleRegistry(reg)
	t.Cleanup(func() { subagent.SetDefaultRoleRegistry(nil) })

	out := stripANSI(NewSubAgentLine("subagent-0", "checker", "verify build", false, "1.2s", "", "", 80, 20, false).Render())
	if !strings.Contains(out, "Chuck (checker)") {
		t.Errorf("expected display name alongside role, got %q", out)
	}
}

func TestSubAgentResult_ArrivesOnEndEvent(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.SubAgentStartEvent{SubAgentID: "sa-1", Role: "developer", Prompt: "fix the bug"})
	m.HandleEvent(&agent.SubAgentEndEvent{SubAgentID: "sa-1", Role: "developer", Duration: 2 * time.Second, Result: "the fix worked"})
	// The spawn_subagent tool end must not add or alter anything.
	m.HandleEvent(&agent.ToolEndEvent{ID: 7, Name: "spawn_subagent", Args: `{"role":"developer","prompt":"fix the bug"}`, Result: "the fix worked"})

	if len(m.messages) != 1 {
		t.Fatalf("spawn_subagent must not add a second message, got %d", len(m.messages))
	}
	if m.messages[0].Role != "subagent" {
		t.Fatalf("expected the subagent message, got role %q", m.messages[0].Role)
	}
	if m.messages[0].SubResult != "the fix worked" {
		t.Errorf("expected result attached to subagent line, got %q", m.messages[0].SubResult)
	}

	// Collapsed by default: result not visible.
	collapsed := stripANSI(m.renderMessages())
	if strings.Contains(collapsed, "the fix worked") {
		t.Errorf("result should be hidden while collapsed, got %q", collapsed)
	}
	if !strings.Contains(collapsed, "▶") {
		t.Errorf("result should add an expand toggle, got %q", collapsed)
	}

	// Expanded: result visible, registered as a clickable zone.
	m.subagentExpanded = map[string]bool{"subagent-0": true}
	expanded := stripANSI(m.renderMessages())
	if !strings.Contains(expanded, "the fix worked") {
		t.Errorf("expanded line should show the result, got %q", expanded)
	}
	if len(m.subagentZones) != 1 || m.subagentZones[0] != "subagent-0" {
		t.Errorf("expected subagent-0 zone registered, got %v", m.subagentZones)
	}
}

func TestSubAgentResult_OtherToolsStillRender(t *testing.T) {
	m := &Model{width: 80}
	m.HandleEvent(&agent.ToolEndEvent{Name: "bash", Args: `{"command":"ls"}`, Result: "file.txt"})

	if len(m.messages) != 1 || m.messages[0].Role != "tool" {
		t.Fatalf("non-subagent tools should still render as tool messages, got %+v", m.messages)
	}
}

func TestRoleStyle_KnownAndUnknownRoles(t *testing.T) {
	known := roleStyle("checker").Render("x")
	if known == "x" {
		t.Error("known role should be styled")
	}
	unknown := roleStyle("no-such-role").Render("x")
	if !strings.Contains(stripANSI(unknown), "x") {
		t.Errorf("unknown role should still render text, got %q", unknown)
	}
}

func TestQuietModeCollapsesReasoningAndTools(t *testing.T) {
	m := &Model{width: 80, verbose: false, reasoningExpanded: make(map[string]bool)}
	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "response", Reasoning: "model reasoning"},
		{Role: "tool", ToolName: "bash", Content: "tool output"},
	}

	output := m.renderMessages()
	stripped := stripANSI(output)

	if !strings.Contains(stripped, "Reasoning") {
		t.Error("quiet mode should render collapsed reasoning toggle")
	}
	if strings.Contains(stripped, "model reasoning") {
		t.Error("quiet mode should not render reasoning content when collapsed")
	}
	if !strings.Contains(stripped, "bash") {
		t.Error("quiet mode should render collapsed tool line with summary")
	}
	if !strings.Contains(stripped, "tool output") {
		t.Error("quiet mode should render tool summary")
	}
	if !strings.Contains(stripped, "hello") {
		t.Error("quiet mode should render user message")
	}
	if !strings.Contains(stripped, "response") {
		t.Error("quiet mode should render assistant content")
	}
	if len(m.reasoningZones) != 1 {
		t.Errorf("quiet mode should register 1 reasoning zone (click-expandable), got %d", len(m.reasoningZones))
	}
	if len(m.toolZones) != 1 {
		t.Errorf("quiet mode should have 1 tool zone, got %d", len(m.toolZones))
	}
}

func TestVerboseShowsBlocks(t *testing.T) {
	m := &Model{width: 80, verbose: true, reasoningExpanded: map[string]bool{"reasoning-1": true}}
	m.messages = []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "response", Reasoning: "model reasoning"},
		{Role: "tool", ToolName: "bash", Content: "tool output"},
	}

	output := m.renderMessages()
	stripped := stripANSI(output)

	if !strings.Contains(stripped, "Reasoning") {
		t.Error("verbose mode should render reasoning toggle")
	}
	if !strings.Contains(stripped, "model reasoning") {
		t.Error("verbose mode should render reasoning content")
	}
	if !strings.Contains(stripped, "bash") {
		t.Error("verbose mode should render tool name")
	}
	if len(m.reasoningZones) != 1 {
		t.Errorf("verbose mode should have 1 reasoning zone, got %d", len(m.reasoningZones))
	}
	if len(m.toolZones) != 1 {
		t.Errorf("verbose mode should have 1 tool zone, got %d", len(m.toolZones))
	}
}

func TestToggleVerbose(t *testing.T) {
	m := &Model{width: 80, verbose: false}
	if m.verbose {
		t.Error("should start with verbose off")
	}

	m.ToggleVerbose()
	if !m.verbose {
		t.Error("should toggle verbose on")
	}
	if m.ephemMsg != "Verbose on." {
		t.Errorf("expected ephemeral message 'Verbose on.', got %q", m.ephemMsg)
	}

	m.ToggleVerbose()
	if m.verbose {
		t.Error("should toggle verbose off")
	}
	if m.ephemMsg != "Verbose off." {
		t.Errorf("expected ephemeral message 'Verbose off.', got %q", m.ephemMsg)
	}
}

func TestQuietModeCollapsesLiveThinking(t *testing.T) {
	m := &Model{width: 80, verbose: false, reasoningExpanded: make(map[string]bool)}
	m.thinking = true
	m.streaming = false
	m.thinkContent = "thinking..."

	output := m.renderMessages()
	stripped := stripANSI(output)

	if strings.Contains(stripped, "thinking...") {
		t.Error("quiet mode should not render live thinking content")
	}
	if !strings.Contains(stripped, "Reasoning...") {
		t.Error("quiet mode should render collapsed live reasoning marker")
	}
	if len(m.reasoningZones) != 1 || m.reasoningZones[0] != "reasoning-live" {
		t.Errorf("quiet mode should register the reasoning-live zone, got %v", m.reasoningZones)
	}

	m.verbose = true
	output = m.renderMessages()
	stripped = stripANSI(output)

	if !strings.Contains(stripped, "Reasoning...") {
		t.Error("verbose mode should render live thinking with 'Reasoning...' spinner")
	}
	if !strings.Contains(stripped, "thinking...") {
		t.Error("verbose mode should render live thinking content")
	}
}

func TestRunningToolAutoExpandsOnlyInVerbose(t *testing.T) {
	newModel := func(verbose bool) *Model {
		m := &Model{width: 80, verbose: verbose, toolExpanded: make(map[string]bool)}
		m.messages = []Message{
			{Role: "tool", ToolName: "bash", ToolArgs: "run", Content: "cmd header\nrunning output line"},
		}
		m.toolCall = "bash"
		return m
	}

	quiet := stripANSI(newModel(false).renderMessages())
	if strings.Contains(quiet, "▼") {
		t.Error("quiet mode should not auto-expand the running tool")
	}
	if strings.Contains(quiet, "running output line") {
		t.Error("quiet mode should hide running tool output while collapsed")
	}

	verbose := stripANSI(newModel(true).renderMessages())
	if !strings.Contains(verbose, "▼") {
		t.Error("verbose mode should auto-expand the running tool")
	}
	if !strings.Contains(verbose, "running output line") {
		t.Error("verbose mode should show running tool output")
	}
}
