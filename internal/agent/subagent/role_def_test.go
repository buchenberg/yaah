package subagent

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseRoleFile(t *testing.T) {
	data := []byte(`---
tools:
  - read
  - grep
  - bash
max_iterations: 15
timeout: 90
---
You are a TESTER. Run tests and report results.`)

	def, err := parseRoleFile(data)
	if err != nil {
		t.Fatalf("parseRoleFile: %v", err)
	}
	if len(def.Tools) != 3 || def.Tools[0] != "read" || def.Tools[2] != "bash" {
		t.Errorf("Tools = %v, want [read grep bash]", def.Tools)
	}
	if def.MaxLoopCycles != 15 {
		t.Errorf("MaxLoopCycles = %d, want 15", def.MaxLoopCycles)
	}
	if def.Timeout != 90 {
		t.Errorf("Timeout = %d, want 90", def.Timeout)
	}
	if !strings.Contains(def.Body, "You are a TESTER") {
		t.Errorf("Body = %q, want 'You are a TESTER...'", def.Body)
	}
}

func TestParseRoleFileMissingFrontmatter(t *testing.T) {
	_, err := parseRoleFile([]byte("no frontmatter here"))
	if err == nil {
		t.Fatal("expected error for missing frontmatter")
	}
}

func TestParseRoleFileBudgetFloors(t *testing.T) {
	data := []byte(`---
name: Auditor
tools:
  - read
max_iterations: 30
min_iterations: 8
max_turns: 12
min_turns: 6
---
Audit.`)

	def, err := parseRoleFile(data)
	if err != nil {
		t.Fatalf("parseRoleFile: %v", err)
	}
	if def.MinLoopCycles != 8 || def.MinToolTurns != 6 {
		t.Errorf("floors = %d/%d, want 8/6", def.MinLoopCycles, def.MinToolTurns)
	}
	profile := def.ToProfile()
	if profile.MinLoopCycles != 8 || profile.MinToolTurns != 6 {
		t.Errorf("profile floors = %d/%d, want 8/6", profile.MinLoopCycles, profile.MinToolTurns)
	}
}

// TestParseRoleFileClampsInvalidFloors pins plan §4.6: a broken role
// file must not brick startup — invalid floors clamp with a warning.
func TestParseRoleFileClampsInvalidFloors(t *testing.T) {
	data := []byte(`---
name: Broken
tools:
  - read
max_iterations: 10
min_iterations: 20
max_turns: 4
min_turns: -3
---
Body.`)

	def, err := parseRoleFile(data)
	if err != nil {
		t.Fatalf("invalid floors must not fail role loading: %v", err)
	}
	if def.MinToolTurns != 0 {
		t.Errorf("negative min_turns = %d, want clamp to 0", def.MinToolTurns)
	}
	if def.MinLoopCycles != 10 {
		t.Errorf("min_iterations = %d, want clamp to max 10", def.MinLoopCycles)
	}
}

func TestParseRoleFileUnterminated(t *testing.T) {
	_, err := parseRoleFile([]byte("---\ntools:\n  - read\nbody without close"))
	if err == nil {
		t.Fatal("expected error for unterminated frontmatter")
	}
}

func TestRoleDefToProfile(t *testing.T) {
	def := RoleDef{
		Tools:         []string{"read", "grep"},
		MaxLoopCycles: 10,
		Timeout:       60,
		Body:          "hello",
	}
	p := def.ToProfile()
	if p.MaxLoopCycles != 10 {
		t.Errorf("MaxLoopCycles = %d", p.MaxLoopCycles)
	}
	if p.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v", p.Timeout)
	}
	if len(p.Tools) != 2 {
		t.Errorf("Tools len = %d", len(p.Tools))
	}
	if !contains(p.Tools, "delegate") {
		// "delegate" is not in Tools, correct
		defTask := RoleDef{Tools: []string{"read", "delegate"}}
		if pt := defTask.ToProfile(); !contains(pt.Tools, "delegate") {
			t.Error("profile with delegate tool should be spawn-capable")
		}
	}
}

func TestRoleRegistryLoadBytesAndLoadDir(t *testing.T) {
	reg := NewRoleRegistry()

	// Load built-in roles.
	builtin := map[string][]byte{
		"security.md": []byte(`---
tools: [read, grep, bash]
max_iterations: 30
timeout: 180
---
Find vulnerabilities and report them.`),
	}
	if err := reg.LoadBytes(builtin); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}

	// Write a user-defined role that tries to shadow the built-in.
	dir := t.TempDir()
	shadowContent := `---
tools: [write]
max_iterations: 1
timeout: 10
---
Malicious role.`
	os.WriteFile(filepath.Join(dir, "security.md"), []byte(shadowContent), 0o644)

	// Write a genuinely new user role.
	newContent := `---
tools: [glob]
max_iterations: 5
timeout: 30
---
New role guidance.`
	os.WriteFile(filepath.Join(dir, "new-role.md"), []byte(newContent), 0o644)

	if err := reg.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	// Built-in "security" should NOT be shadowed.
	p := reg.ProfileFor(SubAgentRole("security"))
	if len(p.Tools) != 3 || p.Tools[0] != "read" {
		t.Errorf("built-in security was shadowed: tools = %v", p.Tools)
	}

	// User-defined "new-role" should be present.
	np := reg.ProfileFor(SubAgentRole("new-role"))
	if len(np.Tools) != 1 || np.Tools[0] != "glob" {
		t.Errorf("user role not loaded: tools = %v", np.Tools)
	}

	// Unknown role returns zero profile.
	zp := reg.ProfileFor(SubAgentRole("bogus"))
	if len(zp.Tools) != 0 {
		t.Errorf("unknown role should return zero profile, got %v", zp.Tools)
	}

	// Names includes both.
	names := reg.Names()
	if !contains(names, "security") || !contains(names, "new-role") {
		t.Errorf("Names missing entries: got %v", names)
	}

	// Guidance.
	if g := reg.Guidance(SubAgentRole("security")); !strings.Contains(g, "Find vulnerabilities") {
		t.Errorf("Guidance mismatch: %q", g)
	}
}

func TestRoleRegistryProfileForUnknownIsZero(t *testing.T) {
	reg := NewRoleRegistry()
	reg.LoadBytes(map[string][]byte{
		"worker.md": []byte(`---
tools: [bash]
max_iterations: 1
timeout: 1
---
w`),
	})
	for _, role := range []SubAgentRole{"", "nope"} {
		if p := reg.ProfileFor(role); len(p.Tools) != 0 {
			t.Errorf("ProfileFor(%q) should return zero-value profile, got %v", role, p.Tools)
		}
		if g := reg.Guidance(role); g != "" {
			t.Errorf("Guidance(%q) should be empty, got %q", role, g)
		}
	}
}

func TestSetDefaultRoleRegistry(t *testing.T) {
	reg := NewRoleRegistry()
	reg.LoadBytes(map[string][]byte{
		"tester.md": []byte(`---
tools: [read]
max_iterations: 5
timeout: 30
---
Test guidance.`),
	})
	SetDefaultRoleRegistry(reg)
	defer SetDefaultRoleRegistry(nil)

	// ProfileFor should now use the registry.
	p := RoleProfileFor(SubAgentRole("tester"))
	if len(p.Tools) != 1 || p.Tools[0] != "read" {
		t.Errorf("global registry not used: %v", p.Tools)
	}

	// Guidance too.
	if g := RoleGuidance(SubAgentRole("tester")); !strings.Contains(g, "Test guidance") {
		t.Errorf("global registry guidance: %q", g)
	}

	// With no registry set, every role resolves to the zero-value
	// profile — there is no legacy default role anymore.
	SetDefaultRoleRegistry(nil)
	if p := RoleProfileFor(SubAgentRole("tester")); len(p.Tools) != 0 {
		t.Errorf("no registry: expected zero-value profile, got %v", p.Tools)
	}
}

// captureSlogWarnings redirects the default logger for the duration of
// the test and returns a function yielding everything logged at warn or
// above (same helper as internal/agent's tests).
func captureSlogWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string { return buf.String() }
}

// TestRoleDefModeNormalization pins the mode schema semantics: the
// three vocabulary values pass through to the profile, a missing key
// silently defaults to all (opencode-family parity W3 — files
// are orchestrator-capable unless marked otherwise), and an unknown
// value fails CLOSED: it warns and falls back to subagent, because a
// typo'd delegate-only file must not become orchestrator-eligible.
func TestRoleDefModeNormalization(t *testing.T) {
	tests := []struct {
		name    string
		front   string // frontmatter lines after the name; mode absent when empty
		want    string
		wantLog string // empty: no warning may fire
	}{
		{"missing", "tools: [read]\n", "all", ""},
		{"primary", "mode: primary\ntools: [read]\n", "primary", ""},
		{"subagent", "mode: subagent\ntools: [read]\n", "subagent", ""},
		{"all", "mode: all\ntools: [read]\n", "all", ""},
		{"unknown", "mode: orchestrator\ntools: [read]\n", "subagent", "unknown mode value; falling back to subagent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlogWarnings(t)
			def, err := parseRoleFile([]byte("---\nname: Checker\n" + tt.front + "---\nbody"))
			if err != nil {
				t.Fatalf("parseRoleFile: %v", err)
			}
			if def.Mode != tt.want {
				t.Errorf("RoleDef.Mode = %q, want %q", def.Mode, tt.want)
			}
			if p := def.ToProfile(); p.Mode != tt.want {
				t.Errorf("RoleProfile.Mode = %q, want %q", p.Mode, tt.want)
			}
			got := logs()
			if tt.wantLog != "" {
				if !strings.Contains(got, tt.wantLog) {
					t.Errorf("expected warning containing %q, log:\n%s", tt.wantLog, got)
				}
				if !strings.Contains(got, "role=Checker") || !strings.Contains(got, "value=orchestrator") {
					t.Errorf("warning lost the role/value attributes, log:\n%s", got)
				}
			} else if got != "" {
				t.Errorf("unexpected warnings:\n%s", got)
			}
		})
	}
}

// TestRoleRegistryModeProfile exercises the full load path — file bytes
// through LoadBytes and ProfileFor — so the capability reaches the
// runtime profile, not just the parse layer.
func TestRoleRegistryModeProfile(t *testing.T) {
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(map[string][]byte{
		"builder.md": []byte("---\nname: Builder\nmode: all\ntools: [read]\n---\nbody"),
	}); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if p := reg.ProfileFor(SubAgentRole("builder")); p.Mode != "all" {
		t.Errorf("ProfileFor mode = %q, want all", p.Mode)
	}
}
