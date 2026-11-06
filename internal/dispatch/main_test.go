package dispatch

import (
	"os"
	"testing"

	"github.com/buchenberg/goatherd-runtime/sandbox/landlock"
)

func TestMain(m *testing.M) {
	// A confined exec (Workspace.Exec on a jailed workspace) re-enters this test
	// binary with a trampoline sentinel as argv[1]; handle it before the test
	// framework runs, exactly as main.go does for the yaah binary. Without this,
	// the re-exec runs the whole suite again instead of applying the jail.
	if code, handled := landlock.MaybeRunTrampoline(os.Args); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
