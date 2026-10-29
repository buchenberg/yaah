package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// ledgerPath is the in-repo parity ledger. The external gap report is a
// snapshot; this file and the code are the source of truth.
const ledgerPath = "../../docs/shepherd-parity.md"

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestLedgerPinsMatchGoMod fails when a module pin moves in go.mod without the
// ledger being updated (or vice versa). The pins are the upstream contracts the
// parity claims rest on.
func TestLedgerPinsMatchGoMod(t *testing.T) {
	gomod := read(t, "../../go.mod")
	ledger := read(t, ledgerPath)

	pins := []string{
		"github.com/buchenberg/goatherd-kernel v0.18.0",
		"github.com/buchenberg/goatherd-runtime v0.20.0",
		"github.com/buchenberg/goatherd-runtime/sandbox/croft v0.2.0",
		"github.com/buchenberg/goatherd-croft v0.9.1",
	}
	for _, pin := range pins {
		module, version, ok := strings.Cut(pin, " ")
		if !ok {
			t.Fatalf("bad pin %q", pin)
		}
		if !strings.Contains(gomod, module+" "+version) {
			t.Errorf("go.mod no longer contains %s — update the parity ledger and this guard together", pin)
		}
		if !strings.Contains(ledger, version) {
			t.Errorf("ledger is missing the %s pin %s — refresh docs/shepherd-parity.md", module, version)
		}
	}
}

// TestLedgerNamesEveryWorld keeps the documented vocabulary and the resolver's
// constants in step. A new world kind must be added to both.
func TestLedgerNamesEveryWorld(t *testing.T) {
	ledger := read(t, ledgerPath)
	for _, kind := range []subagent.WorldKind{
		subagent.WorldAuto,
		subagent.WorldWorktree,
		subagent.WorldContainer,
		subagent.WorldShared,
	} {
		if !strings.Contains(ledger, "`"+string(kind)+"`") && !strings.Contains(ledger, string(kind)) {
			t.Errorf("ledger does not name the %q world", kind)
		}
	}
}

// TestLedgerRecordsBlockers pins the two upstream blockers that keep parts of
// the parity epic open. Dropping the claims when they land is deliberate, and
// this guard makes it explicit rather than accidental.
func TestLedgerRecordsBlockers(t *testing.T) {
	ledger := read(t, ledgerPath)
	for _, blocker := range []string{"settlement wiring", "seatbelt"} {
		if !strings.Contains(strings.ToLower(ledger), strings.ToLower(blocker)) {
			t.Errorf("ledger must name the %q blocker while it is open", blocker)
		}
	}
}
