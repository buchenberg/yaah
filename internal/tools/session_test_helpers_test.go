package tools

import (
	goatherdsandbox "github.com/buchenberg/goatherd-runtime/sandbox"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/sandboxfake"
)

// recordingOpener hands the probe a hook-wired fake (its shell cannot run
// the contract script) and the session sandbox the given recorder. Duplicated
// from internal/tools/substrate's bootstrap tests; keep the copies in sync.
func recordingOpener(rec goatherdsandbox.Sandbox) SandboxOpener {
	var opened int
	return func(config.WorkspaceConfig) (goatherdsandbox.Sandbox, error) {
		opened++
		if opened == 1 {
			sb := sandboxfake.New()
			sb.ExecHook = func(goatherdsandbox.ExecRequest) (goatherdsandbox.ExecResult, error) {
				return goatherdsandbox.ExecResult{ExitCode: 0}, nil
			}
			return sb, nil
		}
		return rec, nil
	}
}

// cleanOpener hands out fresh fakes in call order — probe sandbox, then
// session sandbox — and reaches them through the returned slice. Every probe
// (an even position) gets a passing ExecHook: the fake's shell does not
// implement the contract script's loop surface, and the probe is not under
// test here. Duplicated from internal/tools/substrate's lifecycle tests; keep
// the copies in sync.
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
