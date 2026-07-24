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
}
