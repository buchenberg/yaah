package yaah

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/spf13/cobra"
)

// runCLI executes the real command tree with args and returns combined output.
// Using rootCmd (rather than calling a RunE directly) is what makes these
// tests meaningful for flag placement: the '--' separator and the
// persistent-flag guard both live in cobra's parsing layer.
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	t.Cleanup(func() {
		// Restore root command state for the next test.
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	// Execute (not rootCmd.Execute) so the per-run flag-Changed reset runs too.
	err := Execute()
	return buf.String(), err
}

// TestMemoryAddJoinsPositionalArgs pins the fix for the silent-truncation bug:
// 'yaah memory add the CLI audit ran fine' used to store only "the".
func TestMemoryAddJoinsPositionalArgs(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	if _, err := runCLI(t, "memory", "add", "alpha", "beta", "gamma"); err != nil {
		t.Fatalf("memory add: %v", err)
	}

	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	notes, err := db.ListMemory(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(notes))
	}
	if notes[0].Text != "alpha beta gamma" {
		t.Errorf("note text = %q, want %q", notes[0].Text, "alpha beta gamma")
	}
}

// TestMemorySearchJoinsQueryWords pins the same fix for search: the query used
// to be args[0] only, so a natural-language query searched one word.
func TestMemorySearchJoinsQueryWords(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	if _, err := runCLI(t, "memory", "add", "sandbox denies egress by default"); err != nil {
		t.Fatalf("memory add: %v", err)
	}
	out, err := runCLI(t, "memory", "search", "denies", "egress")
	if err != nil {
		t.Fatalf("memory search: %v", err)
	}
	if !strings.Contains(out, "sandbox denies egress") {
		t.Errorf("search output missing the note:\n%s", out)
	}
}

// TestMemoryAddAcceptsDashLeadingTextWithSeparator covers the documented
// escape hatch: text that starts with a dash needs '--'.
func TestMemoryAddAcceptsDashLeadingTextWithSeparator(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	if _, err := runCLI(t, "memory", "add", "--", "-v", "is", "verbose"); err != nil {
		t.Fatalf("memory add --: %v", err)
	}

	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	notes, err := db.ListMemory(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Text != "-v is verbose" {
		t.Fatalf("got %#v, want one note '-v is verbose'", notes)
	}
}

// TestMCPAddSeparatorAndExclusiveForms pins both halves of the fixed usage:
// '--' is required before a command whose own flags would otherwise be eaten,
// and --url plus a trailing command is rejected instead of silently dropping
// one of them.
func TestMCPAddSeparatorAndExclusiveForms(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	out, err := runCLI(t, "mcp", "add", "filesystem", "--", "npx", "-y", "@some/pkg", "/tmp")
	if err != nil {
		t.Fatalf("mcp add -- ...: %v\n%s", err, out)
	}

	out, err = runCLI(t, "mcp", "add", "both", "--url", "http://localhost:3333/mcp", "--", "npx", "-y", "pkg")
	if err == nil {
		t.Fatalf("expected an error for --url plus trailing command, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "cannot combine --url") {
		t.Errorf("error = %v, want the mutual-exclusion message", err)
	}
}

// TestAgentFlagsRejectedOnNonAgentCommands pins the guard that replaced silent
// flag acceptance. These invocations used to exit 0 having ignored the flag.
func TestAgentFlagsRejectedOnNonAgentCommands(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"config show", []string{"config", "show", "--workspace", "/tmp"}, "--workspace"},
		{"sessions list", []string{"sessions", "list", "--resume=abc123"}, "--resume"},
		{"mcp list", []string{"mcp", "list", "--approval", "yolo"}, "--approval"},
		{"memory stats", []string{"memory", "stats", "--session", "sess-1"}, "--session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("expected rejection for %v", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want mention of %s", err, tc.want)
			}
			if !strings.Contains(err.Error(), "agent-only") {
				t.Errorf("error = %v, want it to explain the flag is agent-only", err)
			}
		})
	}
}

// TestAgentCommandPathsAreExempt is the other half of the guard: the five
// agent-spawning commands (and both spellings of each server) must still
// accept the flags. Paths are checked directly because actually running tui,
// web, serve or acp-serve would start a server.
func TestAgentCommandPathsAreExempt(t *testing.T) {
	for _, path := range []string{
		"yaah", "yaah tui", "yaah web", "yaah serve",
		"yaah mcp serve", "yaah acp-serve", "yaah acp serve",
	} {
		if !agentCommandPaths[path] {
			t.Errorf("%q missing from agentCommandPaths; its flags would be rejected", path)
		}
	}
}

// TestResolveListenAddr covers the unified --listen flag and its deprecated
// aliases, which is where "same concept, two flag names" used to live.
func TestResolveListenAddr(t *testing.T) {
	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "x"}
		c.Flags().String("listen", "", "")
		c.Flags().String("http", "", "")
		return c
	}

	t.Run("default", func(t *testing.T) {
		got, err := resolveListenAddr(newCmd(), "listen", "http", "", "")
		if err != nil || got != "" {
			t.Fatalf("got (%q, %v), want (\"\", nil)", got, err)
		}
	})

	t.Run("listen wins when alone", func(t *testing.T) {
		c := newCmd()
		if err := c.Flags().Parse([]string{"--listen", "127.0.0.1:7333"}); err != nil {
			t.Fatal(err)
		}
		got, err := resolveListenAddr(c, "listen", "http", "127.0.0.1:7333", "")
		if err != nil || got != "127.0.0.1:7333" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})

	t.Run("legacy alias still honoured", func(t *testing.T) {
		c := newCmd()
		if err := c.Flags().Parse([]string{"--http", "127.0.0.1:7444"}); err != nil {
			t.Fatal(err)
		}
		got, err := resolveListenAddr(c, "listen", "http", "", "127.0.0.1:7444")
		if err != nil || got != "127.0.0.1:7444" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})

	t.Run("both is an error", func(t *testing.T) {
		c := newCmd()
		if err := c.Flags().Parse([]string{"--listen", ":1", "--http", ":2"}); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveListenAddr(c, "listen", "http", ":1", ":2"); err == nil {
			t.Fatal("expected an error when both spellings are supplied")
		}
	})
}

// TestServeCommandSpellings pins the canonical/deprecated pairing: the MCP and
// ACP servers answer to both spellings so existing client configs keep
// working, and the old top-level names carry a deprecation notice.
func TestServeCommandSpellings(t *testing.T) {
	find := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}

	if c := find(rootCmd, "serve"); c == nil || c.Deprecated == "" {
		t.Error("top-level 'serve' must exist and be marked deprecated")
	}
	if c := find(find(rootCmd, "mcp"), "serve"); c == nil || c.Deprecated != "" {
		t.Error("'mcp serve' must exist and must not be deprecated")
	}
	if c := find(rootCmd, "acp-serve"); c == nil || c.Deprecated == "" {
		t.Error("top-level 'acp-serve' must exist and be marked deprecated")
	}
	if c := find(find(rootCmd, "acp"), "serve"); c == nil || c.Deprecated != "" {
		t.Error("'acp serve' must exist and must not be deprecated")
	}

	// Both spellings must expose the unified address flag.
	for _, path := range [][2]string{{"serve", "listen"}, {"web", "listen"}} {
		c := find(rootCmd, path[0])
		if c == nil {
			t.Fatalf("command %q missing", path[0])
		}
		if c.Flags().Lookup(path[1]) == nil {
			t.Errorf("%s: missing --%s", path[0], path[1])
		}
		if f := c.Flags().Lookup("http"); path[0] == "serve" && (f == nil || !f.Hidden) {
			t.Error("serve: --http should exist as a hidden deprecated alias")
		}
		if f := c.Flags().Lookup("addr"); path[0] == "web" && (f == nil || !f.Hidden) {
			t.Error("web: --addr should exist as a hidden deprecated alias")
		}
	}
}

// sessionFixture writes one session with a user turn, a tool exchange and an
// assistant reply, the way the engine does.
func sessionFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("YAAH_HOME", t.TempDir())
	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cwd, err := cwdpath.Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	const id = "sess-show-1"
	if err := db.CreateSession(memory.Session{
		ID: id, StartedAt: now, CWD: cwd, Title: "audit the cli",
		Model: "gpt-4o-mini", TokensIn: 11, TokensOut: 7,
	}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []memory.Message{
		{SessionID: id, Idx: 0, Role: "user", Content: "list the commands"},
		{SessionID: id, Idx: 1, Role: "assistant", ToolName: "bash", Content: "ls cmd"},
		{SessionID: id, Idx: 2, Role: "tool", ToolName: "bash", Content: "serve.go\nweb.go"},
		{SessionID: id, Idx: 3, Role: "assistant", Content: "three servers, three spellings"},
	} {
		if err := db.AddMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestSessionsShowRendersTranscript covers the command that closes the
// list/delete-only gap.
func TestSessionsShowRendersTranscript(t *testing.T) {
	id := sessionFixture(t)

	out, err := runCLI(t, "sessions", "show", id)
	if err != nil {
		t.Fatalf("sessions show: %v\n%s", err, out)
	}
	for _, want := range []string{id, "4 msg", "gpt-4o-mini", "tokens: 11 in / 7 out", "list the commands", "three servers"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// --no-tools drops the tool exchange but keeps the conversation.
	out, err = runCLI(t, "sessions", "show", id, "--no-tools")
	if err != nil {
		t.Fatalf("sessions show --no-tools: %v", err)
	}
	if strings.Contains(out, "serve.go") {
		t.Errorf("--no-tools should hide tool results:\n%s", out)
	}
	if !strings.Contains(out, "three servers") {
		t.Errorf("--no-tools should keep assistant turns:\n%s", out)
	}

	// --max-chars truncates.
	out, err = runCLI(t, "sessions", "show", id, "--max-chars", "5")
	if err != nil {
		t.Fatalf("sessions show --max-chars: %v", err)
	}
	if !strings.Contains(out, "more chars") {
		t.Errorf("--max-chars should leave a truncation marker:\n%s", out)
	}

	// --json is machine-readable.
	out, err = runCLI(t, "sessions", "show", id, "--json")
	if err != nil {
		t.Fatalf("sessions show --json: %v", err)
	}
	if !strings.Contains(out, `"messages"`) || !strings.Contains(out, `"role": "user"`) {
		t.Errorf("--json output looks wrong:\n%s", out)
	}

	// Unknown ids are a clean error, not a stack of internal details.
	if _, err := runCLI(t, "sessions", "show", "sess-nope"); err == nil {
		t.Fatal("expected an error for an unknown session id")
	} else if !strings.Contains(err.Error(), "no such session") {
		t.Errorf("error = %v, want 'no such session'", err)
	}
}

// TestHelpDocumentsSeparator guards the documentation half of the audit: the
// '--' escape hatch has to be discoverable from --help, because two commands
// require it.
func TestHelpDocumentsSeparator(t *testing.T) {
	help, err := runCLI(t, "mcp", "add", "--help")
	if err != nil {
		t.Fatalf("mcp add --help: %v", err)
	}
	if !strings.Contains(help, "-- npx -y") {
		t.Errorf("mcp add help must show the '--' stdio form:\n%s", help)
	}
	if strings.Contains(help, "filesystem npx -y") {
		t.Errorf("mcp add help still shows the broken (separator-less) example:\n%s", help)
	}

	root, err := runCLI(t, "--help")
	if err != nil {
		t.Fatalf("root --help: %v", err)
	}
	if !strings.Contains(root, "yaah --") {
		t.Errorf("root help must document prefixing a prompt with '--':\n%s", root)
	}
}

// TestXdgConfigIsNotTouched keeps the suite honest about side effects: every
// test above must operate inside YAAH_HOME, never the real config.
func TestXdgConfigIsNotTouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)
	if _, err := runCLI(t, "memory", "add", "side effect check"); err != nil {
		t.Fatalf("memory add: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected the temp YAAH_HOME to receive the write")
	}
	if _, err := os.Stat(filepath.Join(home, "..", "config.yaml")); err == nil {
		t.Fatal("write escaped YAAH_HOME")
	}
}
