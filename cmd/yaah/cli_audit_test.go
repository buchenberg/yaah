package yaah

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/spf13/cobra"
)

// These tests deliberately avoid the package-level rootCmd and Execute().
// Other tests in this package mutate the shared command tree and flag
// variables, so driving the global tree from here corrupted their state and
// made unrelated tests (goat, release) fail intermittently depending on
// scheduling. Everything below runs against purpose-built commands instead.

// scratchCmd returns a command whose output is captured, with the given flags
// pre-registered so a RunE can read them without touching the real command.
func scratchCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	c := &cobra.Command{Use: "scratch"}
	c.SetOut(&buf)
	c.SetErr(&buf)
	return c, &buf
}

// TestMemoryAddJoinsPositionalArgs pins the fix for the silent-truncation bug:
// 'yaah memory add the CLI audit ran fine' used to store only "the".
func TestMemoryAddJoinsPositionalArgs(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cmd, _ := scratchCmd(t)
	if err := memoryAddCmd.RunE(cmd, []string{"alpha", "beta", "gamma"}); err != nil {
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

// TestMemoryAddJoinsDashLeadingArgs covers what '--' delivers to RunE: cobra
// strips the separator, so the command must accept dash-prefixed words as
// text rather than treating the first one as a flag.
func TestMemoryAddJoinsDashLeadingArgs(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cmd, _ := scratchCmd(t)
	if err := memoryAddCmd.RunE(cmd, []string{"-v", "is", "verbose"}); err != nil {
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
	if len(notes) != 1 || notes[0].Text != "-v is verbose" {
		t.Fatalf("got %#v, want one note '-v is verbose'", notes)
	}
}

// TestMemorySearchJoinsQueryWords pins the same fix for search: the query used
// to be args[0] only, so a natural-language query searched one word.
func TestMemorySearchJoinsQueryWords(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddMemory(memory.Entry{Text: "sandbox denies egress by default"}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cmd, buf := scratchCmd(t)
	cmd.Flags().String("scope", "all", "")
	if err := memorySearchCmd.RunE(cmd, []string{"denies", "egress"}); err != nil {
		t.Fatalf("memory search: %v", err)
	}
	if !strings.Contains(buf.String(), "sandbox denies egress") {
		t.Errorf("search output missing the note:\n%s", buf.String())
	}
}

// TestMCPAddRejectsURLPlusCommand pins the mutual exclusion: --url plus a
// trailing command used to silently drop one of the two forms.
func TestMCPAddRejectsURLPlusCommand(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cmd, _ := scratchCmd(t)
	cmd.Flags().String("url", "http://localhost:3333/mcp", "")
	err := mcpAddCmd.RunE(cmd, []string{"both", "npx", "-y", "pkg"})
	if err == nil {
		t.Fatal("expected an error for --url plus a trailing command")
	}
	if !strings.Contains(err.Error(), "cannot combine --url") {
		t.Errorf("error = %v, want the mutual-exclusion message", err)
	}
}

// TestMCPAddAcceptsCommandWithFlags is the positive half: a command whose own
// arguments look like flags must be accepted once '--' has made the boundary
// explicit. args here are exactly what cobra hands RunE after '--'.
func TestMCPAddAcceptsCommandWithFlags(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())

	cmd, _ := scratchCmd(t)
	if err := mcpAddCmd.RunE(cmd, []string{"filesystem", "npx", "-y", "@some/pkg", "/tmp"}); err != nil {
		t.Fatalf("mcp add: %v", err)
	}
}

// TestAgentFlagGuard exercises the real guard through cobra's parser on a
// throwaway command tree, so it covers flag merging as well as the verdict.
func TestAgentFlagGuard(t *testing.T) {
	// tree builds "yaah <mid> <leaf>" with the agent flags on the root,
	// mirroring how the real tree is wired.
	tree := func(mid, leaf string) *cobra.Command {
		root := &cobra.Command{Use: "yaah", Run: func(*cobra.Command, []string) {}}
		root.PersistentFlags().String("workspace", "", "")
		root.PersistentFlags().String("approval", "", "")
		root.PersistentFlags().String("resume", "", "")
		root.PersistentFlags().String("session", "", "")
		root.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
			return rejectAgentFlagsOnNonAgentCommands(c)
		}
		midCmd := &cobra.Command{Use: mid}
		leafCmd := &cobra.Command{Use: leaf, Run: func(*cobra.Command, []string) {}}
		midCmd.AddCommand(leafCmd)
		root.AddCommand(midCmd)
		return root
	}

	t.Run("non-agent command rejects", func(t *testing.T) {
		root := tree("config", "show")
		root.SetArgs([]string{"config", "show", "--workspace", "/tmp"})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err == nil {
			t.Fatal("expected the guard to reject --workspace on a non-agent command")
		}
	})

	t.Run("agent command is exempt", func(t *testing.T) {
		root := tree("mcp", "serve")
		root.SetArgs([]string{"mcp", "serve", "--workspace", "/tmp"})
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		if err := root.Execute(); err != nil {
			t.Fatalf("guard rejected an allowlisted path: %v", err)
		}
	})
}

// TestAgentCommandPathsAreExempt keeps the allowlist honest: the five
// agent-spawning commands (and both spellings of each server) must stay listed
// or their flags would be rejected.
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

// TestResolveListenAddr covers the unified --listen flag and its aliases,
// which is where "same concept, two flag names" used to live.
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
	t.Run("listen", func(t *testing.T) {
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

// TestServeCommandSpellings pins the canonical/deprecated pairing without
// walking the shared root command.
func TestServeCommandSpellings(t *testing.T) {
	if serveCmd.Name() != "serve" || serveCmd.Deprecated == "" {
		t.Error("top-level 'serve' must be named serve and be marked deprecated")
	}
	if mcpServeCmd.Name() != "serve" || mcpServeCmd.Deprecated != "" {
		t.Error("'mcp serve' must be named serve and must not be deprecated")
	}
	if acpServeCmd.Name() != "acp-serve" || acpServeCmd.Deprecated == "" {
		t.Error("top-level 'acp-serve' must keep its name and be marked deprecated")
	}

	var nested *cobra.Command
	for _, c := range acpCmd.Commands() {
		if c.Name() == "serve" {
			nested = c
		}
	}
	if nested == nil || nested.Deprecated != "" {
		t.Error("'acp serve' must exist and must not be deprecated")
	}

	// Both servers expose the unified address flag; the legacy spellings stay
	// present but hidden so existing invocations keep working.
	for _, tc := range []struct {
		cmd     *cobra.Command
		legacy  string
		primary string
	}{
		{serveCmd, "http", "listen"},
		{mcpServeCmd, "http", "listen"},
		{webCmd, "addr", "listen"},
	} {
		if tc.cmd.Flags().Lookup(tc.primary) == nil {
			t.Errorf("%s: missing --%s", tc.cmd.Name(), tc.primary)
		}
		f := tc.cmd.Flags().Lookup(tc.legacy)
		if f == nil || !f.Hidden {
			t.Errorf("%s: --%s should exist as a hidden deprecated alias", tc.cmd.Name(), tc.legacy)
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

// showCmd returns a command carrying the flags sessions show reads, so the
// RunE can be exercised without touching the real command's flag state.
func showCmd(t *testing.T, jsonOut, noTools bool, maxChars int) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd, buf := scratchCmd(t)
	cmd.Flags().Bool("json", jsonOut, "")
	cmd.Flags().Bool("no-tools", noTools, "")
	cmd.Flags().Int("max-chars", maxChars, "")
	return cmd, buf
}

// TestSessionsShowRendersTranscript covers the command that closes the
// list/delete-only gap.
func TestSessionsShowRendersTranscript(t *testing.T) {
	id := sessionFixture(t)

	cmd, buf := showCmd(t, false, false, 2000)
	if err := sessionsShowCmd.RunE(cmd, []string{id}); err != nil {
		t.Fatalf("sessions show: %v\n%s", err, buf.String())
	}
	out := buf.String()
	for _, want := range []string{id, "4 msg", "gpt-4o-mini", "tokens: 11 in / 7 out", "list the commands", "three servers"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestSessionsShowFlags covers --no-tools, --max-chars and --json.
func TestSessionsShowFlags(t *testing.T) {
	id := sessionFixture(t)

	cmd, buf := showCmd(t, false, true, 2000)
	if err := sessionsShowCmd.RunE(cmd, []string{id}); err != nil {
		t.Fatalf("--no-tools: %v", err)
	}
	if strings.Contains(buf.String(), "serve.go") {
		t.Errorf("--no-tools should hide tool results:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "three servers") {
		t.Errorf("--no-tools should keep assistant turns:\n%s", buf.String())
	}

	cmd, buf = showCmd(t, false, false, 5)
	if err := sessionsShowCmd.RunE(cmd, []string{id}); err != nil {
		t.Fatalf("--max-chars: %v", err)
	}
	if !strings.Contains(buf.String(), "more chars") {
		t.Errorf("--max-chars should leave a truncation marker:\n%s", buf.String())
	}

	cmd, buf = showCmd(t, true, false, 2000)
	if err := sessionsShowCmd.RunE(cmd, []string{id}); err != nil {
		t.Fatalf("--json: %v", err)
	}
	if !strings.Contains(buf.String(), `"messages"`) || !strings.Contains(buf.String(), `"role": "user"`) {
		t.Errorf("--json output looks wrong:\n%s", buf.String())
	}
}

// TestSessionsShowUnknownID checks the clean error path.
func TestSessionsShowUnknownID(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())
	cmd, _ := showCmd(t, false, false, 2000)
	err := sessionsShowCmd.RunE(cmd, []string{"sess-nope"})
	if err == nil {
		t.Fatal("expected an error for an unknown session id")
	}
	if !strings.Contains(err.Error(), "no such session") {
		t.Errorf("error = %v, want 'no such session'", err)
	}
}

// TestHelpDocumentsSeparator guards the documentation half of the audit. It
// asserts on the command definitions rather than running --help, so it stays
// independent of the shared tree.
func TestHelpDocumentsSeparator(t *testing.T) {
	if !strings.Contains(mcpAddCmd.Long, "-- npx -y") {
		t.Errorf("mcp add help must show the '--' stdio form:\n%s", mcpAddCmd.Long)
	}
	if strings.Contains(mcpAddCmd.Long, "filesystem npx -y") {
		t.Errorf("mcp add help still shows the broken separator-less example:\n%s", mcpAddCmd.Long)
	}
	if !strings.Contains(rootCmd.Long, "--") || !strings.Contains(rootCmd.Example, "yaah --") {
		t.Error("root help must document prefixing a prompt with '--'")
	}
	if !strings.Contains(memoryAddCmd.Long, "--") || !strings.Contains(memorySearchCmd.Long, "--") {
		t.Error("memory add/search help must document the '--' escape hatch")
	}
}
