package yaah

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

func TestCustomRoleDiscovery(t *testing.T) {
	tmp := t.TempDir()

	roleDir := filepath.Join(tmp, ".agents", "roles")
	if err := os.MkdirAll(roleDir, 0755); err != nil {
		t.Fatal(err)
	}

	roleDef := `---
name: Sam
specialty: security
contract:
  heading: "## Audit"
  fields: [severity, files_scanned, issues_found, findings, summary]
tools:
  - read
  - grep
  - glob
  - ls
  - powershell
  - bash
  - webfetch
  - file_info
  - go_outline
  - calculate
  - json_query
  - git
max_iterations: 30
timeout: 180
---

You are a SECURITY AUDITOR sub-agent on yaah's team. Scan code for vulnerabilities.
`
	if err := os.WriteFile(filepath.Join(roleDir, "security_auditor.md"), []byte(roleDef), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("roleSearchPaths includes role dir", func(t *testing.T) {
		dirs := roleSearchPaths(tmp)
		found := false
		for _, d := range dirs {
			if d == roleDir {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("roleSearchPaths(%q) did not include %q", tmp, roleDir)
		}
	})

	t.Run("LoadDir discovers custom role", func(t *testing.T) {
		reg := subagent.NewRoleRegistry()
		if err := reg.LoadDir(roleDir); err != nil {
			t.Fatal(err)
		}

		role := subagent.SubAgentRole("security_auditor")
		profile := reg.ProfileFor(role)
		if len(profile.Tools) == 0 {
			t.Fatal("security_auditor role has no tools — not discovered")
		}

		if profile.DisplayName != "Sam" {
			t.Errorf("DisplayName = %q, want %q", profile.DisplayName, "Sam")
		}
		if profile.Specialty != "security" {
			t.Errorf("Specialty = %q, want %q", profile.Specialty, "security")
		}
		if profile.MaxIterations != 30 {
			t.Errorf("MaxIterations = %d, want 30", profile.MaxIterations)
		}
		if profile.Timeout != 180_000_000_000 { // 180s in nanoseconds
			t.Errorf("Timeout = %v, want 180s", profile.Timeout)
		}
		if !contains(profile.Tools, "read") || !contains(profile.Tools, "grep") || !contains(profile.Tools, "git") {
			t.Errorf("tools missing expected entries, got %v", profile.Tools)
		}
		if profile.Contract.Heading != "## Audit" {
			t.Errorf("Contract.Heading = %q, want %q", profile.Contract.Heading, "## Audit")
		}
		if len(profile.Contract.Fields) != 5 {
			t.Errorf("Contract.Fields has %d entries, want 5", len(profile.Contract.Fields))
		}

		guidance := reg.Guidance(role)
		if !strings.Contains(guidance, "vulnerabilities") {
			t.Errorf("Guidance missing role identity text")
		}
	})

	t.Run("Names includes custom role", func(t *testing.T) {
		reg := subagent.NewRoleRegistry()
		reg.LoadDir(roleDir)
		names := reg.Names()
		if !contains(names, "security_auditor") {
			t.Errorf("registry Names() missing 'security_auditor': %v", names)
		}
	})

	t.Run("custom role does not override built-in", func(t *testing.T) {
		reg := subagent.NewRoleRegistry()
		// Load built-in analyst first
		files := builtinRoleFiles()
		if files != nil {
			reg.LoadBytes(files)
		}
		// Try to override with a custom file named analyst.md
		overrideDir := filepath.Join(tmp, ".agents", "roles-override")
		os.MkdirAll(overrideDir, 0755)
		os.WriteFile(filepath.Join(overrideDir, "analyst.md"), []byte(`---
name: Override
specialty: hacker
tools: [read]
max_iterations: 1
timeout: 1
---
Override attempt.
`), 0644)
		reg.LoadDir(overrideDir)

		profile := reg.ProfileFor("analyst")
		// Should still be the built-in, not the override
		if profile.Specialty == "hacker" {
			t.Error("built-in analyst role was overridden by custom role")
		}
		if profile.DisplayName == "Override" {
			t.Error("built-in analyst DisplayName was overridden")
		}
	})
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
