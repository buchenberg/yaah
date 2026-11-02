package yaah

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/sessions"
)

// TestACPListSessionsCarriesMessageCount pins the session/list _meta
// carrier: the store's message count reaches the wire as
// `"_meta":{"messageCount":N}`, and a session with no persisted messages
// omits `_meta` entirely instead of advertising a zero.
func TestACPListSessionsCarriesMessageCount(t *testing.T) {
	dir := t.TempDir()
	db, err := memory.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	chatty := memory.Session{ID: "sess-chatty", StartedAt: now - 30, CWD: dir, Model: "test/model"}
	quiet := memory.Session{ID: "sess-quiet", StartedAt: now - 60, CWD: dir, Model: "test/model"}
	for _, s := range []memory.Session{chatty, quiet} {
		if err := db.CreateSession(s); err != nil {
			t.Fatalf("create session %s: %v", s.ID, err)
		}
	}
	for i, content := range []string{"hello there", "hi — how can I help?"} {
		if err := db.AddMessage(memory.Message{
			ID:        memory.NewEntryID(),
			SessionID: chatty.ID,
			Idx:       i,
			Role:      "user",
			Content:   content,
			Timestamp: now - 30 + int64(i),
		}); err != nil {
			t.Fatalf("add message %d: %v", i, err)
		}
	}

	infos, _, err := acpListSessions(sessions.New(db))("", "")
	if err != nil {
		t.Fatalf("acpListSessions: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("sessions = %d, want 2", len(infos))
	}
	if infos[0].SessionID != chatty.ID {
		t.Fatalf("newest first: got %s, want %s", infos[0].SessionID, chatty.ID)
	}

	raw, err := json.Marshal(infos[0])
	if err != nil {
		t.Fatalf("marshal session with messages: %v", err)
	}
	if want := `"_meta":{"messageCount":2}`; !strings.Contains(string(raw), want) {
		t.Fatalf("session with messages = %s, want it to contain %s", raw, want)
	}

	emptyRaw, err := json.Marshal(infos[1])
	if err != nil {
		t.Fatalf("marshal empty session: %v", err)
	}
	if strings.Contains(string(emptyRaw), "_meta") {
		t.Fatalf("message-less session = %s, want no _meta", emptyRaw)
	}
}

func TestMcpManifestsFromACP(t *testing.T) {
	got := mcpManifestsFromACP([]acp.McpServerRef{{
		Name:    "stub",
		Command: "/abs/stub",
		Args:    []string{"--stdio"},
		Env:     []acp.EnvVariable{{Name: "A", Value: "1"}},
	}})
	m, ok := got["stub"]
	if !ok {
		t.Fatalf("manifest missing: %#v", got)
	}
	if m.Transport != "stdio" {
		t.Errorf("Transport = %q, want stdio", m.Transport)
	}
	if m.Command != "/abs/stub" || len(m.Args) != 1 || m.Args[0] != "--stdio" {
		t.Errorf("command/args not preserved: %+v", m)
	}
	if m.Env["A"] != "1" {
		t.Errorf("env not preserved: %#v", m.Env)
	}
	if got := mcpManifestsFromACP(nil); got != nil {
		t.Errorf("empty refs = %#v, want nil", got)
	}
}

func TestMCPManifestsFromACP(t *testing.T) {
	got := mcpManifestsFromACP([]acp.McpServerRef{
		{
			Name:    "fs",
			Command: "/bin/fs",
			Args:    []string{"--root", "/repo"},
			Env:     []acp.EnvVariable{{Name: "TOKEN", Value: "x"}, {Name: ""}},
		},
		{Name: "empty"},
	})
	if len(got) != 2 {
		t.Fatalf("manifests = %+v, want two", got)
	}
	fs := got["fs"]
	if fs.Command != "/bin/fs" || !reflect.DeepEqual(fs.Args, []string{"--root", "/repo"}) {
		t.Errorf("fs manifest = %+v", fs)
	}
	if fs.Transport != "stdio" || fs.Env["TOKEN"] != "x" || len(fs.Env) != 1 {
		t.Errorf("fs transport/env = %+v", fs)
	}
	if _, ok := got["empty"]; !ok {
		t.Errorf("empty manifest missing: %+v", got)
	}
}

func TestACPServeCmdHelpMentionsModes(t *testing.T) {
	cmd := newACPServeCmd("acp-serve-test", false)
	if cmd.Use != "acp-serve-test" || cmd.Short == "" {
		t.Errorf("command = %+v", cmd)
	}
	help := cmd.Long
	for _, want := range []string{"session/set_mode", "Plan/Build", "session/set_config_option"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q", want)
		}
	}
}

func TestACPAvailableCommandsDiscoversSkills(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".agents", "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: demo skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commands := acpAvailableCommands(dir)
	for _, command := range commands {
		if command.Name == "demo" {
			if command.Description != "demo skill" || command.Input == nil || command.Input.Hint == "" {
				t.Fatalf("demo command = %+v", command)
			}
			return
		}
	}
	t.Fatalf("demo skill not advertised: %+v", commands)
}
