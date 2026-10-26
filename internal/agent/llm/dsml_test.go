package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDSMLToolCalls(t *testing.T) {
	t.Run("no DSML", func(t *testing.T) {
		content := "Here is the result of your request."
		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		_, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

		cleaned, calls, _, ok := parseDSMLToolCalls(content, new(int))
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

// TestDSMLTolerantGrammar covers emissions that deviate from the canonical
// double-bar tool_calls spelling: fewer bars, a space before the element name,
// and the bare calls element DeepSeek actually emitted in production.
func TestDSMLTolerantGrammar(t *testing.T) {
	// leakedSample is verbatim assistant content from a session where the
	// markup was shown to the user as the answer and nothing executed.
	leakedSample := "<\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>\n" +
		"<\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke name=\"powershell\">\n" +
		"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"command\" string=\"true\">Get-Date</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
		"</\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke>\n" +
		"</\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>"

	t.Run("leaked sample with bare calls element", func(t *testing.T) {
		cleaned, calls, rejected, ok := parseDSMLToolCalls(leakedSample, new(int))
		if !ok {
			t.Fatal("expected markup detected")
		}
		if len(rejected) != 0 {
			t.Fatalf("unexpected rejections: %v", rejected)
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
		var args map[string]any
		if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
			t.Fatalf("invalid arguments JSON: %v", err)
		}
		if args["command"] != "Get-Date" {
			t.Fatalf("expected command=Get-Date, got %v", args["command"])
		}
	})

	t.Run("single bar namespace", func(t *testing.T) {
		content := "<\uFF5CDSML\uFF5Ctool_calls>\n" +
			"<\uFF5CDSML\uFF5Cinvoke name=\"git\">\n" +
			"<\uFF5CDSML\uFF5Cparameter name=\"action\" string=\"true\">status</\uFF5CDSML\uFF5Cparameter>\n" +
			"</\uFF5CDSML\uFF5Cinvoke>\n" +
			"</\uFF5CDSML\uFF5Ctool_calls>"

		_, calls, rejected, ok := parseDSMLToolCalls(content, new(int))
		if !ok || len(rejected) != 0 {
			t.Fatalf("ok=%v rejected=%v", ok, rejected)
		}
		if len(calls) != 1 || calls[0].Function.Name != "git" {
			t.Fatalf("calls = %+v", calls)
		}
	})

	t.Run("tool_call invoke element name", func(t *testing.T) {
		content := "<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5C tool_call name=\"read\">\n" +
			"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"filePath\" string=\"true\">main.go</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5C tool_call>\n" +
			"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"

		_, calls, rejected, ok := parseDSMLToolCalls(content, new(int))
		if !ok || len(rejected) != 0 {
			t.Fatalf("ok=%v rejected=%v", ok, rejected)
		}
		if len(calls) != 1 || calls[0].Function.Name != "read" {
			t.Fatalf("calls = %+v", calls)
		}
	})

	t.Run("prose discussing DSML is untouched", func(t *testing.T) {
		content := "DSML markup from DeepSeek is recovered by dsml.go."
		cleaned, calls, rejected, ok := parseDSMLToolCalls(content, new(int))
		if ok || len(calls) != 0 || len(rejected) != 0 {
			t.Fatalf("ok=%v calls=%v rejected=%v", ok, calls, rejected)
		}
		if cleaned != content {
			t.Fatalf("content modified: %q", cleaned)
		}
	})
}

// TestDSMLRejectsUnparsableBlock pins the failure mode: markup that is
// present but cannot be turned into a dispatchable call must be reported, not
// silently accepted as the answer.
func TestDSMLRejectsUnparsableBlock(t *testing.T) {
	content := "<\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>\n" +
		"no invoke element here\n" +
		"</\uFF5C\uFF5CDSML\uFF5C\uFF5Ctool_calls>"

	cleaned, calls, rejected, ok := parseDSMLToolCalls(content, new(int))
	if !ok {
		t.Fatal("expected markup detected")
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 calls, got %+v", calls)
	}
	if len(rejected) != 1 || rejected[0] != rejectDSMLNoInvoke {
		t.Fatalf("rejected = %v", rejected)
	}
	if cleaned != content {
		t.Fatalf("rejected markup must stay in cleaned content, got %q", cleaned)
	}

	// The rejection must survive the dialect seam, where the caller turns it
	// into an explicit turn failure rather than an accepted answer.
	_, _, seamRejected, seamOK := recoverDialectToolCalls(content, new(int), nil)
	if !seamOK {
		t.Fatal("expected the seam to report markup")
	}
	if len(seamRejected) != 1 {
		t.Fatalf("seam dropped the rejection: %v", seamRejected)
	}
}

// TestDSMLTokenFilterVariants proves the live filter recognises the same
// spellings as the parser, including when the stream splits mid-tag.
func TestDSMLTokenFilterVariants(t *testing.T) {
	leaked := "<\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>\n" +
		"<\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke name=\"powershell\">\n" +
		"<\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter name=\"command\" string=\"true\">Get-Date</\uFF5C\uFF5CDSML\uFF5C\uFF5C parameter>\n" +
		"</\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke>\n" +
		"</\uFF5C\uFF5CDSML\uFF5C\uFF5C calls>"

	t.Run("variant block suppressed byte by byte", func(t *testing.T) {
		var f dsmlTokenFilter
		var out strings.Builder
		for i := 0; i < len(leaked); i++ {
			out.WriteString(f.filterToken(leaked[i : i+1]))
		}
		if got := out.String(); got != "" {
			t.Fatalf("markup reached the view: %q", got)
		}
	})

	t.Run("prose around variant block preserved", func(t *testing.T) {
		var f dsmlTokenFilter
		var out strings.Builder
		out.WriteString(f.filterToken("before "))
		out.WriteString(f.filterToken(leaked))
		out.WriteString(f.filterToken(" after"))
		if got := out.String(); got != "before  after" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("prose with angle bracket is not held up", func(t *testing.T) {
		var f dsmlTokenFilter
		if got := f.filterToken("if a < b"); got != "if a < b" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("bare invoke without wrapper closes on invoke tag", func(t *testing.T) {
		var f dsmlTokenFilter
		suppressed := "<\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke name=\"read\">body</\uFF5C\uFF5CDSML\uFF5C\uFF5C invoke>"
		if got := f.filterToken(suppressed); got != "" {
			t.Fatalf("expected suppression, got %q", got)
		}
		if got := f.filterToken("tail"); got != "tail" {
			t.Fatalf("expected tail forwarded, got %q", got)
		}
	})
}
