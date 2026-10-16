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

	t.Run("tool_call wrapper-only markup flagged", func(t *testing.T) {
		// Issue #314's actual wrapper name must be recognized too (#333
		// review): the leak used <tool_response>, not just
		// <tool_response>.
		if !LooksLikeUnexecutedToolCalls(tcOpen() + "\n</" + "tool_call>") {
			t.Fatal("<tool_response> wrapper residue must be flagged")
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

	t.Run("fenced command with tool_call wrapper flagged", func(t *testing.T) {
		content := "```bash\n" +
			"find . -name \"*.go\"\n" +
			"```\n" +
			tcOpen() + "\n" +
			"main.go\ngo.mod\n" +
			"</" + "tool_call>"
		if !LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("a <tool_response>-wrapped invented transcript must be flagged too")
		}
	})

	t.Run("transcript followed by substantial prose passes", func(t *testing.T) {
		// The residual-prose allowance applies to the text OUTSIDE the
		// transcript block (#333 review): a real report that quotes a
		// transcript and then explains it must not be rejected.
		content := "```bash\n" +
			"find . -name \"*.go\"\n" +
			"```\n" +
			trOpen() + "\n" +
			"main.go\ngo.mod\n" +
			trClose() + "\n\n" +
			"The scan above lists the two Go source files at the repository " +
			"root. Both compile cleanly under go build ./... and no further " +
			"action is needed; the package layout is documented in README.md."
		if LooksLikeUnexecutedToolCalls(content) {
			t.Fatal("a transcript quoted inside a substantive report must not be flagged")
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

// TestLooksLikeFabricatedTranscript pins the zero-tool sibling guard from
// issue #329: a role with no tools cannot have executed anything, so a
// wrapper-free command transcript with output evidence is fabricated. The
// runner only calls it for tool-less dispatches.
func TestLooksLikeFabricatedTranscript(t *testing.T) {
	t.Run("issue 329 repro flagged", func(t *testing.T) {
		content := "```bash\n" +
			"git ls-files '*.go' | wc -l\n" +
			"```\n" +
			"stdout: 842"
		if !LooksLikeFabricatedTranscript(content) {
			t.Fatal("a bare fenced command with invented stdout must be flagged")
		}
	})

	t.Run("evidence inside the fence flagged", func(t *testing.T) {
		content := "```\n" +
			"$ git ls-files '*.go' | wc -l\n" +
			"stdout: 842\n" +
			"```"
		if !LooksLikeFabricatedTranscript(content) {
			t.Fatal("a prompt-led transcript inside a bare fence must be flagged")
		}
	})

	t.Run("unterminated fence flagged", func(t *testing.T) {
		content := "```bash\n" +
			"go test ./...\n" +
			"exit code: 0"
		if !LooksLikeFabricatedTranscript(content) {
			t.Fatal("an unterminated fence with an exit-code claim must be flagged")
		}
	})

	t.Run("substantial prose passes", func(t *testing.T) {
		content := "I could not run anything because I have no tools, but here is " +
			"the command that would answer the question if someone ran it:\n\n" +
			"```bash\n" +
			"git ls-files '*.go' | wc -l\n" +
			"```\n\n" +
			"Note that stdout: 842 above would be the expected shape of the " +
			"answer; the real count must be verified by a role with a shell."
		if LooksLikeFabricatedTranscript(content) {
			t.Fatal("a report with real prose around the transcript must not be flagged")
		}
	})

	t.Run("non-shell fence passes", func(t *testing.T) {
		content := "```go\n" +
			"func main() { println(842) }\n" +
			"```\n" +
			"output: 842"
		if LooksLikeFabricatedTranscript(content) {
			t.Fatal("a fenced go example is not a shell transcript")
		}
	})

	t.Run("command suggestion without output evidence passes", func(t *testing.T) {
		content := "```bash\n" +
			"git ls-files '*.go' | wc -l\n" +
			"```"
		if LooksLikeFabricatedTranscript(content) {
			t.Fatal("a suggested command with no claimed output is an escalation, not a transcript")
		}
	})

	t.Run("empty passes", func(t *testing.T) {
		if LooksLikeFabricatedTranscript("  \n ") {
			t.Fatal("empty output is handled by the empty-result guard")
		}
	})
}
