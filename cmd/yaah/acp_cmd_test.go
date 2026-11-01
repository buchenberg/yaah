package yaah

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/config"
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

// testCatalog builds an acpModelCatalog whose slow phase is a test stub.
// The wait is long so a coordinated test controls when an attempt ends.
func testCatalog(discover func(context.Context) ([]acp.ModelOption, bool)) *acpModelCatalog {
	return &acpModelCatalog{
		cfg:        &config.Config{},
		logf:       func(string, ...any) {},
		budget:     time.Second,
		wait:       5 * time.Second,
		discoverFn: discover,
	}
}

// TestACPCatalogOptionsSharesOneDiscovery pins single-flight: concurrent
// callers wait on one attempt instead of triggering one each.
func TestACPCatalogOptionsSharesOneDiscovery(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	catalog := testCatalog(func(context.Context) ([]acp.ModelOption, bool) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return []acp.ModelOption{{Provider: "p", Model: "m"}}, true
	})

	first := make(chan []acp.ModelOption, 1)
	go func() { first <- catalog.Options(context.Background()) }()
	<-started

	second := make(chan []acp.ModelOption, 1)
	go func() { second <- catalog.Options(context.Background()) }()

	close(release)
	for i, got := range [][]acp.ModelOption{<-first, <-second} {
		if len(got) != 1 || got[0].Model != "m" {
			t.Errorf("caller %d options = %#v, want the discovered model", i, got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("discovery ran %d times, want 1", n)
	}
}

// TestACPCatalogOptionsHonorsContext pins that a caller's cancellation
// bounds only its own wait: the attempt keeps running and its result is
// cached for the next request.
func TestACPCatalogOptionsHonorsContext(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	catalog := testCatalog(func(context.Context) ([]acp.ModelOption, bool) {
		started <- struct{}{}
		<-release
		return []acp.ModelOption{{Provider: "p", Model: "m"}}, true
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan []acp.ModelOption, 1)
	go func() { result <- catalog.Options(ctx) }()
	<-started
	cancel()

	select {
	case got := <-result:
		if len(got) != 0 {
			t.Errorf("canceled caller options = %#v, want the empty snapshot", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Options ignored its canceled context")
	}

	close(release)
	// The aborted caller must not have discarded the attempt: the next
	// caller sees its result without paying again.
	got := catalog.Options(context.Background())
	if len(got) != 1 || got[0].Model != "m" {
		t.Errorf("next caller options = %#v, want the discovered model", got)
	}
}

// TestACPCatalogOptionsRetryCooldown pins that an incomplete discovery is
// not re-run on every call.
func TestACPCatalogOptionsRetryCooldown(t *testing.T) {
	var calls atomic.Int32
	catalog := testCatalog(func(context.Context) ([]acp.ModelOption, bool) {
		calls.Add(1)
		return []acp.ModelOption{{Provider: "p", Model: "m"}}, false
	})

	if got := catalog.Options(context.Background()); len(got) != 1 {
		t.Fatalf("first options = %#v, want the config-declared model", got)
	}
	if got := catalog.Options(context.Background()); len(got) != 1 {
		t.Fatalf("cached options = %#v, want the previous snapshot", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("discovery ran %d times inside the cooldown, want 1", n)
	}

	// Past the cooldown a later request may retry.
	catalog.mu.Lock()
	catalog.lastTry = time.Now().Add(-acpModelCatalogRetry - time.Second)
	catalog.mu.Unlock()
	if got := catalog.Options(context.Background()); len(got) != 1 {
		t.Fatalf("retried options = %#v, want the discovered model", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("discovery ran %d times after the cooldown, want 2", n)
	}
}

// TestACPCatalogOptionsWaitIsBounded pins that a request never waits out
// a whole slow attempt: it gets the current snapshot, and the attempt's
// result is there for the next request.
func TestACPCatalogOptionsWaitIsBounded(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	catalog := testCatalog(func(context.Context) ([]acp.ModelOption, bool) {
		started <- struct{}{}
		<-release
		return []acp.ModelOption{{Provider: "p", Model: "m"}}, true
	})
	catalog.wait = 20 * time.Millisecond

	begin := time.Now()
	if got := catalog.Options(context.Background()); len(got) != 0 {
		t.Errorf("options = %#v, want the empty snapshot", got)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("Options waited %v for a slow attempt", elapsed)
	}
	<-started

	close(release)
	if got := catalog.Options(context.Background()); len(got) != 1 || got[0].Model != "m" {
		t.Errorf("next options = %#v, want the discovered model", got)
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
