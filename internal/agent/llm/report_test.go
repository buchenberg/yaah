package llm

import (
	"strings"
	"testing"
)

func TestLooksLikeUnexecutedToolCalls(t *testing.T) {
	t.Run("issue 314 fixture flagged", func(t *testing.T) {
		if !LooksLikeUnexecutedToolCalls(issue314Fixture) {
			t.Fatal("markup-only truncated fixture must be flagged")
		}
	})

	t.Run("complete attribute call flagged", func(t *testing.T) {
		content := strings.Join([]string{
			trOpen(),
			fnOpen("bash"),
			paramOpen("command") + "go build ./..." + paramClose(),
			fnClose(),
			trClose(),
		}, "\n")
		if !LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("markup-only complete call must be flagged")
		}
	})

	t.Run("wrapper-only markup flagged", func(t *testing.T) {
		// Unrecoverable residue: the wrapper arrived but no parseable
		// function block, so nothing executed and nothing can be salvaged.
		if !LooksLikeUnexecutedToolCalls(trOpen() + "\n" + trClose()) {
			t.Fatal("wrapper-only markup must be flagged")
		}
	})

	t.Run("DSML block flagged", func(t *testing.T) {
		content := "<\uff5c\uff5cDSML\uff5c\uff5ctool_calls>\n" +
			"<\uff5c\uff5cDSML\uff5c\uff5cinvoke name=\"read\">\n" +
			"<\uff5c\uff5cDSML\uff5c\uff5cparameter name=\"filePath\" string=\"true\">main.go</\uff5c\uff5cDSML\uff5c\uff5cparameter>\n" +
			"</\uff5c\uff5cDSML\uff5c\uff5cinvoke>\n" +
			"</\uff5c\uff5cDSML\uff5c\uff5ctool_calls>"
		if !LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("DSML-only markup must be flagged")
		}
	})

	t.Run("fenced command with invented transcript flagged", func(t *testing.T) {
		content := "```bash\n" +
			"find . -name \"*.go\"\n" +
			"```\n" +
			trOpen() + "\n" +
			"main.go\ngo.mod\n" +
			trClose()
		if !LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("fenced command presented with made-up output must be flagged")
		}
	})

	t.Run("normal prose report passes", func(t *testing.T) {
		content := "I counted the Go files under internal/. There are 182 .go files " +
			"across 24 packages; the largest is internal/agent with 61 files."
		if LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("legitimate prose report must not be flagged")
		}
	})

	t.Run("prose discussing the dialect passes", func(t *testing.T) {
		content := "When a local model degrades under high tool load it can emit an " +
			"attribute dialect such as " + fnOpen("bash") + " with " + paramOpen("command") +
			" tags instead of structured tool calls. The recovery parser in " +
			"attrdialect.go re-parses these into real calls; see issue #314 for " +
			"the original trace and reproduction steps."
		if LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("substantive discussion of markup must not be flagged")
		}
	})

	t.Run("prose report with fenced example passes", func(t *testing.T) {
		content := "Here is the summary of findings.\n\n```go\nfunc main() {}\n```\n\n" +
			"All tests pass and the refactor is complete; the report follows below."
		if LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("fenced code without response markup must not be flagged")
		}
	})

	t.Run("empty passes", func(t *testing.T) {
		if LooksLikeUnexecutedToolCalls("   \n\t ") {
			t.Fatal("empty output is handled by the empty-result guard, not this one")
		}
	})
}
