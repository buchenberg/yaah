package yaah

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shepherd "github.com/buchenberg/shepherd-kernel-go"
)

func setTestTraceConfig(t *testing.T, home, traceDir string) {
	t.Helper()
	configContent := `
agents:
  default:
    model: test/model
    shepherd_trace_dir: ` + traceDir + `
`
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte(configContent), 0o644)
}

func seedTestTraceStore(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "trace.sqlite")
	store, err := shepherd.NewSQLiteTraceStore(path)
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	defer store.Close()

	owner := "sess-123"

	// Turn start marker
	turnIntent := owner + ":turn:0"
	store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: turnIntent,
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID: owner,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Declaration,
				SchemaRef: "yaah.execution.created.v1",
				KindLabel: "turn:created",
				Payload:   map[string]any{"turn": float64(0), "model": "test"},
			}},
		}},
	})

	tools := []string{"ls", "read", "edit"}
	var lastFactIDs []string
	for i, name := range tools {
		intent := owner + ":tool:" + string(rune('a'+i))
		declReceipt, _ := store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: intent,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID:  owner,
				CausalParents: lastFactIDs,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Declaration,
					SchemaRef: "yaah.tool." + name + ".v1",
					KindLabel: name,
					Payload:   map[string]any{"tool": name, "args": `{}`},
				}},
			}},
		})
		capture := owner + ":result:" + string(rune('a'+i))
		captReceipt, _ := store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
			AppendIntentID: capture,
			Groups: []shepherd.AppendGroup{{
				TraceOwnerID:  owner,
				CausalParents: declReceipt.FactIDs,
				FactDrafts: []shepherd.RecordDraft{{
					Mode:      shepherd.Capture,
					SchemaRef: "yaah.tool." + name + ".v1.applied",
					KindLabel: name + ":result",
					Payload:   map[string]any{"success": true, "duration": "1ms"},
				}},
			}},
		})
		lastFactIDs = captReceipt.FactIDs
	}

	// Turn complete marker — chain from the turn start
	store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: owner + ":turn:0:done",
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID:  owner,
			CausalParents: lastFactIDs,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Capture,
				SchemaRef: "yaah.execution.completed.v1",
				KindLabel: "turn:completed",
				Payload:   map[string]any{"turn": float64(0), "success": true, "prompt_tokens": float64(5000), "completion_tokens": float64(200)},
			}},
		}},
	})

	// Also add a sub-agent trace
	subOwner := "sub-developer-sess-123-456"
	store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
		AppendIntentID: "sub:tool:a",
		Groups: []shepherd.AppendGroup{{
			TraceOwnerID: subOwner,
			FactDrafts: []shepherd.RecordDraft{{
				Mode:      shepherd.Declaration,
				SchemaRef: "yaah.tool.read.v1",
				KindLabel: "read",
				Payload:   map[string]any{"tool": "read", "args": `{}`},
			}},
		}},
	})

	return path
}

func TestTraceList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	dir := filepath.Join(home, "traces")
	os.MkdirAll(dir, 0o755)
	setTestTraceConfig(t, home, dir)
	seedTestTraceStore(t, dir)

	var buf bytes.Buffer
	cmd := newShepherdTraceListCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "sess-123") {
		t.Error("list should show sess-123")
	}
	if !strings.Contains(output, "sub-developer") {
		t.Error("list should show sub-developer session")
	}
}

func TestTraceShow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	dir := filepath.Join(home, "traces")
	os.MkdirAll(dir, 0o755)
	setTestTraceConfig(t, home, dir)
	seedTestTraceStore(t, dir)

	var buf bytes.Buffer
	cmd := newShepherdTraceShowCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"sess-123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "sess-123") {
		t.Fatal("show should display session ID")
	}
	if !strings.Contains(output, "ls") {
		t.Error("show should display ls tool")
	}
	if !strings.Contains(output, "read") {
		t.Error("show should display read tool")
	}
	if !strings.Contains(output, "edit") {
		t.Error("show should display edit tool")
	}
	if !strings.Contains(output, "ok") {
		t.Error("show should display ok status")
	}
}

func TestTraceProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	dir := filepath.Join(home, "traces")
	os.MkdirAll(dir, 0o755)
	setTestTraceConfig(t, home, dir)
	seedTestTraceStore(t, dir)

	var buf bytes.Buffer
	cmd := newShepherdTraceProfileCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"sess-123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "sess-123") {
		t.Fatal("profile should display session ID")
	}
	if !strings.Contains(output, "Tools:") {
		t.Error("profile should show tool count")
	}
	if !strings.Contains(output, "SUCCESS") {
		t.Error("profile should show tool stats")
	}
}

func TestTraceProfileNoSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	dir := filepath.Join(home, "traces")
	os.MkdirAll(dir, 0o755)
	setTestTraceConfig(t, home, dir)

	store, err := shepherd.NewSQLiteTraceStore(filepath.Join(dir, "trace.sqlite"))
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	store.Close()

	var buf bytes.Buffer
	cmd := newShepherdTraceProfileCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "No trace sessions found") {
		t.Errorf("expected 'No trace sessions found', got %q", output)
	}
}

func TestTraceShowLatest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	dir := filepath.Join(home, "traces")
	os.MkdirAll(dir, 0o755)
	setTestTraceConfig(t, home, dir)
	seedTestTraceStore(t, dir)

	var buf bytes.Buffer
	cmd := newShepherdTraceShowCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--latest"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "sess-123") {
		t.Fatal("show --latest should display session ID")
	}
}

func TestOpenShepherdTraceStore_UsesConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YAAH_HOME", home)

	// Write config with custom trace dir
	configContent := `
agents:
  default:
    model: test/model
    shepherd_trace_dir: ` + filepath.Join(home, "custom-traces") + `
`
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte(configContent), 0o644)

	dir := filepath.Join(home, "custom-traces")
	os.MkdirAll(dir, 0o755)
	seedTestTraceStore(t, dir)

	store, err := openShepherdTraceStore()
	if err != nil {
		t.Fatalf("openShepherdTraceStore() error: %v", err)
	}
	defer store.Close()

	// Verify we can read from it
	slice, err := store.ReadOwnerPrefix(shepherd.TrustedReadContext, "sess-123", 999, "declarations_only")
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if len(slice.OwnerPaths) == 0 {
		t.Error("should find sess-123 in custom trace dir")
	}
}

func TestCaptureStatus(t *testing.T) {
	tests := []struct {
		name    string
		success bool
		errMsg  string
		want    string
	}{
		{"ok", true, "", "ok"},
		{"error", false, "failed", "error"},
		{"pending_no_success", false, "", "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Can't easily construct VisibleRecord, so test via the profile helper
			// which exercises captureStatus internally
			home := t.TempDir()
			t.Setenv("YAAH_HOME", home)
			dir := filepath.Join(home, "traces")
			os.MkdirAll(dir, 0o755)

			path := filepath.Join(dir, "trace.sqlite")
			store, _ := shepherd.NewSQLiteTraceStore(path)
			defer store.Close()

			payload := map[string]any{"success": tt.success}
			if tt.errMsg != "" {
				payload["error"] = tt.errMsg
			}

			store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
				AppendIntentID: "test:decl:" + tt.name,
				Groups: []shepherd.AppendGroup{{
					TraceOwnerID: "owner",
					FactDrafts: []shepherd.RecordDraft{{
						Mode:      shepherd.Declaration,
						SchemaRef: "yaah.test.v1",
						KindLabel: "test",
						Payload:   map[string]any{},
					}},
				}},
			})
			store.Append(shepherd.TrustedAppendContext, shepherd.AppendBatch{
				AppendIntentID: "test:cap:" + tt.name,
				Groups: []shepherd.AppendGroup{{
					TraceOwnerID: "owner",
					FactDrafts: []shepherd.RecordDraft{{
						Mode:      shepherd.Capture,
						SchemaRef: "yaah.test.v1.applied",
						KindLabel: "test:result",
						Payload:   payload,
					}},
				}},
			})

			slice, _ := store.ReadOwnerPrefix(shepherd.TrustedReadContext, "owner", 999, "both")
			for _, factID := range slice.FactIDs() {
				fact := slice.FactsByID[factID]
				if fact.GetEnvelope().Mode == shepherd.Capture {
					got := captureStatus(fact)
					if got != tt.want {
						t.Errorf("captureStatus = %q, want %q", got, tt.want)
					}
					break
				}
			}
		})
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]bool{"c": true, "a": true, "b": true}
	got := sortedKeys(m)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("sortedKeys = %v, want [a b c]", got)
	}
}

func TestTruncateString(t *testing.T) {
	if got := truncateString("hello world", 5); got != "he..." {
		t.Errorf("truncateString = %q, want %q", got, "he...")
	}
	if got := truncateString("hi", 5); got != "hi" {
		t.Errorf("truncateString = %q, want %q", got, "hi")
	}
}

func TestFormatNum(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{10000, "10.0k"},
	}
	for _, tt := range tests {
		got := formatNum(tt.n)
		if got != tt.want {
			t.Errorf("formatNum(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
