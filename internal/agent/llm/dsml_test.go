package llm

import (
	"encoding/json"
	"testing"
)

func TestParseDSMLToolCalls(t *testing.T) {
	t.Run("no DSML", func(t *testing.T) {
		content := "Here is the result of your request."
		cleaned, calls, ok := parseDSMLToolCalls(content)
		if ok {
			t.Fatal("expected no DSML detected")
		}
		if cleaned != content {
			t.Fatalf("content modified: %q", cleaned)
		}
		if len(calls) != 0 {
			t.Fatalf("expected 0 calls, got %d", len(calls))
		}
	})

	t.Run("single invoke with string and numeric params", func(t *testing.T) {
		content := "<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"powershell\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"command\" string=\"true\">git status</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"timeout\" string=\"false\">15</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"

		cleaned, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected DSML detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned content, got %q", cleaned)
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Function.Name != "powershell" {
			t.Fatalf("expected powershell, got %q", calls[0].Function.Name)
		}
		if calls[0].Type != "function" {
			t.Fatalf("expected function type, got %q", calls[0].Type)
		}

		var args map[string]any
		if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
			t.Fatalf("invalid arguments JSON: %v", err)
		}
		if args["command"] != "git status" {
			t.Fatalf("expected command=git status, got %v", args["command"])
		}
		if args["timeout"] != float64(15) {
			t.Fatalf("expected timeout=15 (numeric), got %v (%T)", args["timeout"], args["timeout"])
		}
	})

	t.Run("content before and after DSML block", func(t *testing.T) {
		content := "Let me run that for you.\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"read\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"filePath\" string=\"true\">main.go</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"Done."

		cleaned, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected DSML detected")
		}
		if cleaned != "Let me run that for you.Done." {
			t.Fatalf("unexpected cleaned content: %q", cleaned)
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}
		if calls[0].Function.Name != "read" {
			t.Fatalf("expected read, got %q", calls[0].Function.Name)
		}
	})

	t.Run("multiple invokes", func(t *testing.T) {
		content := "<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"git\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"action\" string=\"true\">status</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"read\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"filePath\" string=\"true\">go.mod</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"

		cleaned, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected DSML detected")
		}
		if cleaned != "" {
			t.Fatalf("expected empty cleaned, got %q", cleaned)
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 calls, got %d", len(calls))
		}
		if calls[0].Function.Name != "git" {
			t.Fatalf("expected git, got %q", calls[0].Function.Name)
		}
		if calls[1].Function.Name != "read" {
			t.Fatalf("expected read, got %q", calls[1].Function.Name)
		}
	})

	t.Run("parameter with special characters in value", func(t *testing.T) {
		content := "<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"powershell\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"command\" string=\"true\">Get-Content .scratch\\pr-body.md -Raw | gh pr edit 67 --body -</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"

		_, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected DSML detected")
		}
		if len(calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(calls))
		}

		var args map[string]any
		if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
			t.Fatalf("invalid arguments JSON: %v", err)
		}
		expected := `Get-Content .scratch\pr-body.md -Raw | gh pr edit 67 --body -`
		if args["command"] != expected {
			t.Fatalf("expected %q, got %q", expected, args["command"])
		}
	})

	t.Run("DSML mixed with existing tool calls", func(t *testing.T) {
		content := "Let me help with that.\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"grep\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"pattern\" string=\"true\">TODO</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"

		cleaned, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected DSML detected")
		}
		if cleaned != "Let me help with that." {
			t.Errorf("cleaned = %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "grep" {
			t.Fatalf("calls = %+v", calls)
		}
	})

	t.Run("truncated DSML block with salvageable invoke", func(t *testing.T) {
		content := "I'll write that file.\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"write\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"filePath\" string=\"true\">test.go</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"bash\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter name=\"command\" string=\"true\">go test</\uFF5C\uFF5CDSML\uFF5C\uFF5Cparameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_c"

		cleaned, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected truncated DSML detected")
		}
		if cleaned != "I'll write that file." {
			t.Errorf("cleaned = %q", cleaned)
		}
		if len(calls) != 1 || calls[0].Function.Name != "write" {
			t.Fatalf("expected 1 salvaged call (write), got %+v", calls)
		}
	})

	t.Run("truncated DSML with no complete invokes", func(t *testing.T) {
		content := "Let me check.\n<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n<\uFF5C\uFF5CDSML\uFF5C\uFF5Cinvoke name=\"grep\">\n<\uFF5C\uFF5CDSML\uFF5C\uFF5Cpar"

		cleaned, calls, ok := parseDSMLToolCalls(content)
		if !ok {
			t.Fatal("expected truncated DSML detected")
		}
		if cleaned != "Let me check." {
			t.Errorf("cleaned = %q", cleaned)
		}
		if len(calls) != 0 {
			t.Fatalf("expected 0 salvaged calls, got %+v", calls)
		}
	})
}

func TestDSMLTokenFilter(t *testing.T) {
	open := "<\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"
	close := "</\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"

	t.Run("no DSML passes through", func(t *testing.T) {
		var f dsmlTokenFilter
		if got := f.filterToken("hello"); got != "hello" {
			t.Errorf("expected hello, got %q", got)
		}
		if got := f.filterToken(" world"); got != " world" {
			t.Errorf("expected ' world', got %q", got)
		}
	})

	t.Run("complete DSML block suppressed", func(t *testing.T) {
		var f dsmlTokenFilter
		got := f.filterToken(open + "invoke stuff" + close)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("content before DSML forwarded", func(t *testing.T) {
		var f dsmlTokenFilter
		got := f.filterToken("before " + open + "invoke" + close)
		if got != "before " {
			t.Errorf("expected 'before ', got %q", got)
		}
	})

	t.Run("content after DSML forwarded", func(t *testing.T) {
		var f dsmlTokenFilter
		f.filterToken(open + "invoke" + close)
		got := f.filterToken(" after")
		if got != " after" {
			t.Errorf("expected ' after', got %q", got)
		}
	})

	t.Run("DSML across chunks", func(t *testing.T) {
		var f dsmlTokenFilter
		if got := f.filterToken("pre "); got != "pre " {
			t.Errorf("chunk 1: expected 'pre ', got %q", got)
		}
		if got := f.filterToken(open[:10]); got != "" {
			t.Errorf("chunk 2 (partial open tag): expected '', got %q", got)
		}
		if got := f.filterToken(open[10:] + "foo"); got != "" {
			t.Errorf("chunk 3 (rest of open tag): expected '', got %q", got)
		}
		if got := f.filterToken(close); got != "" {
			t.Errorf("chunk 4 (close tag): expected '', got %q", got)
		}
		if got := f.filterToken(" post"); got != " post" {
			t.Errorf("chunk 5: expected ' post', got %q", got)
		}
	})

	t.Run("content + DSML + content single chunk", func(t *testing.T) {
		var f dsmlTokenFilter
		got := f.filterToken("hello" + open + "blah" + close + "world")
		if got != "helloworld" {
			t.Errorf("expected 'helloworld', got %q", got)
		}
		if got := f.filterToken("!!"); got != "!!" {
			t.Errorf("expected '!!', got %q", got)
		}
	})

	t.Run("multiple DSML blocks", func(t *testing.T) {
		var f dsmlTokenFilter
		if got := f.filterToken("a" + open + "x" + close + "b"); got != "ab" {
			t.Errorf("expected 'ab', got %q", got)
		}
		if got := f.filterToken(open + "y" + close + "c"); got != "c" {
			t.Errorf("expected 'c', got %q", got)
		}
		if got := f.filterToken("d"); got != "d" {
			t.Errorf("expected 'd', got %q", got)
		}
	})

	t.Run("partial close tag at end held back", func(t *testing.T) {
		var f dsmlTokenFilter
		f.filterToken(open + "invoke")
		partial := close[:5]
		if got := f.filterToken(partial); got != "" {
			t.Errorf("expected '', got %q", got)
		}
		if got := f.filterToken(close[5:]); got != "" {
			t.Errorf("expected '', got %q", got)
		}
		if got := f.filterToken("end"); got != "end" {
			t.Errorf("expected 'end', got %q", got)
		}
	})

	t.Run("close tag appears after content within DSML", func(t *testing.T) {
		var f dsmlTokenFilter
		f.filterToken(open + "invoke")
		if got := f.filterToken(close + "after"); got != "after" {
			t.Errorf("expected 'after', got %q", got)
		}
		if got := f.filterToken(" more"); got != " more" {
			t.Errorf("expected ' more', got %q", got)
		}
	})
}
