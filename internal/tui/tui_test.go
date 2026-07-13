package tui

import (
	"strings"
	"testing"
)

// --- splitTableRow tests ---

func TestSplitTableRow_Basic(t *testing.T) {
	got := splitTableRow("| Name | Age | City |")
	expected := []string{"Name", "Age", "City"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_ExtraWhitespace(t *testing.T) {
	got := splitTableRow("  |   foo   |   bar   |   baz   |  ")
	expected := []string{"foo", "bar", "baz"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_SingleColumn(t *testing.T) {
	got := splitTableRow("| only |")
	expected := []string{"only"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_EmptyCells(t *testing.T) {
	got := splitTableRow("| filled |   | alsofilled |")
	expected := []string{"filled", "", "alsofilled"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_AllEmptyCells(t *testing.T) {
	// After trimming all | and spaces from both ends, "| | | |" becomes "".
	// Splitting "" by "|" gives [""].
	got := splitTableRow("| | | |")
	expected := []string{""}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_NoLeadingPipe(t *testing.T) {
	got := splitTableRow("col1|col2|col3")
	expected := []string{"col1", "col2", "col3"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_NoPipesAtAll(t *testing.T) {
	got := splitTableRow("just a string")
	expected := []string{"just a string"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_EmptyString(t *testing.T) {
	got := splitTableRow("")
	expected := []string{""}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_Unicode(t *testing.T) {
	got := splitTableRow("| café | 名 | 🚀 |")
	expected := []string{"café", "名", "🚀"}
	assertEqual(t, got, expected)
}

func TestSplitTableRow_ConsecutivePipes(t *testing.T) {
	// "||||" after trimming all | from both ends becomes "" -> [""]
	got := splitTableRow("||||")
	assertEqual(t, got, []string{""})
}

func TestSplitTableRow_TabsInCells(t *testing.T) {
	got := splitTableRow("| col\t1 | col\t2 |")
	expected := []string{"col\t1", "col\t2"}
	assertEqual(t, got, expected)
}

// --- updateWidths tests ---

func TestUpdateWidths_Initializes(t *testing.T) {
	var w []int
	updateWidths(&w, []string{"foo", "bar"})
	if len(w) != 2 {
		t.Fatalf("expected 2 widths, got %d", len(w))
	}
	if w[0] != 3 || w[1] != 3 {
		t.Errorf("expected widths [3,3], got %v", w)
	}
}

func TestUpdateWidths_Grows(t *testing.T) {
	w := []int{3, 3}
	updateWidths(&w, []string{"longer", "short"})
	if w[0] != 6 {
		t.Errorf("expected w[0]=6, got %d", w[0])
	}
	if w[1] != 5 {
		t.Errorf("expected w[1]=5 (grew from 3->5), got %d", w[1])
	}
}

func TestUpdateWidths_DoesNotShrink(t *testing.T) {
	w := []int{10, 10}
	updateWidths(&w, []string{"short", "tiny"})
	if w[0] != 10 || w[1] != 10 {
		t.Errorf("widths should not shrink, got %v", w)
	}
}

func TestUpdateWidths_MoreColumnsAppear(t *testing.T) {
	w := []int{3, 3}
	updateWidths(&w, []string{"a", "b", "c", "d"})
	if len(w) != 4 {
		t.Fatalf("expected 4 widths, got %d", len(w))
	}
}

func TestUpdateWidths_EmptySlice(t *testing.T) {
	var w []int
	updateWidths(&w, []string{})
	if len(w) != 0 {
		t.Errorf("expected 0 widths, got %d", len(w))
	}
}

func TestUpdateWidths_UnicodeWidth(t *testing.T) {
	var w []int
	updateWidths(&w, []string{"café"})
	if w[0] != 4 {
		t.Errorf("expected display width 4, got %d", w[0])
	}
}

func TestUpdateWidths_CJK(t *testing.T) {
	var w []int
	updateWidths(&w, []string{"日本語"})
	if w[0] != 6 {
		t.Errorf("expected display width 6 (3 chars × 2), got %d", w[0])
	}
}

// --- padRight tests ---

func TestPadRight_Shorter(t *testing.T) {
	got := padRight("hi", 5)
	if got != "hi   " {
		t.Errorf("expected 'hi   ', got %q", got)
	}
}

func TestPadRight_Longer(t *testing.T) {
	got := padRight("hello world", 5)
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestPadRight_Exact(t *testing.T) {
	got := padRight("hello", 5)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestPadRight_ZeroWidth(t *testing.T) {
	got := padRight("hello", 0)
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
}

func TestPadRight_EmptyString(t *testing.T) {
	got := padRight("", 3)
	if got != "   " {
		t.Errorf("expected '   ', got %q", got)
	}
}

func TestPadRight_Unicode(t *testing.T) {
	got := padRight("café", 7)
	if got != "café   " {
		t.Errorf("expected 'café   ', got %q", got)
	}
}

// --- renderCompactTable tests ---

func TestRenderCompactTable_Simple(t *testing.T) {
	md := "| Name | Age |\n|------|-----|\n| Alice | 30 |\n| Bob | 25 |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "Name") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "──") {
		t.Errorf("missing separator line: %q", got)
	}
	if !strings.Contains(got, "Alice") {
		t.Errorf("missing data: %q", got)
	}
}

func TestRenderCompactTable_SingleColumn(t *testing.T) {
	md := "| Item |\n|------|\n| Apple |\n| Banana |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "Item") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "Apple") {
		t.Errorf("missing data row: %q", got)
	}
}

func TestRenderCompactTable_EmptyCells(t *testing.T) {
	md := "| Col A | Col B |\n|-------|-------|\n| foo | |\n| | bar |"
	got := renderCompactTable(md)
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
	got := renderCompactTable(md)
	if !strings.Contains(got, "Col A") {
		t.Errorf("expected header in output: %q", got)
	}
}

func TestRenderCompactTable_EmptyInput(t *testing.T) {
	got := renderCompactTable("")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestRenderCompactTable_NoSeparator(t *testing.T) {
	// Without a --- separator, renderCompactTable still renders all pipe-delimited
	// lines as aligned columns. Two lines, both treated as data rows (no header row
	// separator drawn, but columns are aligned).
	md := "| Col A | Col B |\n| val1 | val2 |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "Col A") || !strings.Contains(got, "val1") {
		t.Errorf("expected rendered table with aligned columns, got %q", got)
	}
}

func TestRenderCompactTable_SingleLine(t *testing.T) {
	got := renderCompactTable("| just | one |")
	if got != "| just | one |" {
		t.Errorf("expected fallback, got %q", got)
	}
}

func TestRenderCompactTable_ManyColumns(t *testing.T) {
	md := "| A | B | C | D | E | F | G | H |\n|---|---|---|---|---|---|---|---|\n| 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 |"
	got := renderCompactTable(md)
	for _, col := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		if !strings.Contains(got, col) {
			t.Errorf("missing column %q in output: %q", col, got)
		}
	}
}

func TestRenderCompactTable_WideContent(t *testing.T) {
	longStr := strings.Repeat("x", 80)
	md := "| Short | Long |\n|-------|------|\n| s | " + longStr + " |"
	got := renderCompactTable(md)
	if !strings.Contains(got, longStr) {
		t.Errorf("missing wide content: %q", got)
	}
}

func TestRenderCompactTable_Unicode(t *testing.T) {
	md := "| 名前 | 値 |\n|------|----|\n| 東京 | 100 |\n| 大阪 | 200 |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "名前") {
		t.Errorf("missing unicode header: %q", got)
	}
	if !strings.Contains(got, "東京") {
		t.Errorf("missing unicode data: %q", got)
	}
}

func TestRenderCompactTable_Emoji(t *testing.T) {
	md := "| Status | Count |\n|--------|-------|\n| ✅ | 42 |\n| ❌ | 0 |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "✅") {
		t.Errorf("missing emoji: %q", got)
	}
}

func TestRenderCompactTable_MixedWidths(t *testing.T) {
	md := "| A | B |\n|---|---|\n| x | a very long cell value here |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "a very long cell value here") {
		t.Errorf("missing long cell: %q", got)
	}
}

func TestRenderCompactTable_TrailingWhitespaceInCells(t *testing.T) {
	md := "| Name   | Value  |\n|--------|--------|\n| foo    | 123    |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "foo") {
		t.Errorf("missing cell content: %q", got)
	}
}

func TestRenderCompactTable_SeparatorWithColons(t *testing.T) {
	md := "| Left | Center | Right |\n|:-----|:------:|------:|\n| a | b | c |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "Left") {
		t.Errorf("missing header: %q", got)
	}
	if !strings.Contains(got, "a") {
		t.Errorf("missing data row: %q", got)
	}
}

func TestRenderCompactTable_NumbersOnly(t *testing.T) {
	md := "| 2023 | 2024 | 2025 |\n|------|------|------|\n| 100 | 200 | 300 |\n| -5 | 0 | 5.5 |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "-5") {
		t.Errorf("missing negative number: %q", got)
	}
	if !strings.Contains(got, "5.5") {
		t.Errorf("missing decimal: %q", got)
	}
}

func TestRenderCompactTable_SpecialChars(t *testing.T) {
	md := "| Expression | Result |\n|------------|--------|\n| 2 + 2 | 4 |\n| `code` | ok |\n| **bold** | yes |"
	got := renderCompactTable(md)
	if !strings.Contains(got, "code") {
		t.Errorf("missing backtick content: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("expected ANSI styling for inline markdown: %q", got)
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

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

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
	if strings.Contains(plain, "|") {
		t.Errorf("rendered table should not contain pipes: %q", plain)
	}
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
