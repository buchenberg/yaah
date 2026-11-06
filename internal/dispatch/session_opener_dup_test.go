package dispatch

// session_opener_dup_test.go duplicates cleanOpener from
// internal/tools/substrate's lifecycle tests: test helpers cannot cross
// package boundaries. Keep the copies in sync.

import (
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// cleanOpener hands out fresh fakes in call order — probe sandbox, then
// session sandbox — and reaches them through the returned slice. Every probe
// (an even position) gets a passing ExecHook: the fake's shell does not
// implement the contract script's loop surface, and the probe is not under
// test here.
func cleanOpener() (SandboxOpener, func() []*sandboxfake.Sandbox) {
	var opened []*sandboxfake.Sandbox
	open := func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		sb := sandboxfake.New()
		if len(opened)%2 == 0 {
			sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
				return goatherdsandbox.ExecResult{ExitCode: 0}, nil
			}
		}
		opened = append(opened, sb)
		return sb, nil
	}
	return open, func() []*sandboxfake.Sandbox { return opened }
}
