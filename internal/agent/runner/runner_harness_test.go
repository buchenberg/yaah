package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/tools"
)

// TestBuildSubAgentRegistry_KeepsHarnessGuard pins the interaction between two
// policies that deliberately diverge: an unrestricted session (no --workspace)
// drops its validator when deriving a worktree lane, because "unrestricted on
// the main tree" must not widen containment. The harness-configuration guard is
// not containment — a sub-agent must still be unable to rewrite the role files
// or config in its worktree — so it is reattached after the derivation.
func TestBuildSubAgentRegistry_KeepsHarnessGuard(t *testing.T) {
	home := t.TempDir()
	workdir := t.TempDir()

	sessionPV := tools.NewPathValidator("", true, nil)
	sessionPV.WorkDir = workdir
	sessionPV.Harness = tools.NewHarnessPaths(home, filepath.Join(home, ".yaah"), workdir)

	reg, _, err := buildSubAgentRegistry(
		taskRunnerOpts{pathValidator: sessionPV, subCfg: config.SubAgentConfig{Containment: "off"}},
		subagent.RoleProfile{Tools: []string{"write"}}, "", 0, workdir)
	if err != nil {
		t.Fatalf("buildSubAgentRegistry: %v", err)
	}
	write := reg.Get("write")
	if write == nil {
		t.Fatal("write tool not registered")
	}

	_, err = write.Execute(context.Background(), `{"filePath":".agents/roles/injected.md","content":"x"}`)
	var refusal *tools.HarnessRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("sub-agent write to a role file = %v, want a HarnessRefusalError", err)
	}
}
