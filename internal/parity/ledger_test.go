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
		"github.com/buchenberg/goatherd-runtime v0.21.0",
		"github.com/buchenberg/goatherd-runtime/sandbox/croft v0.2.1-0.20261031192038-da4e06c59318",
		"github.com/buchenberg/goatherd-runtime/sandbox/landlock v0.1.2",
		"github.com/buchenberg/goatherd-runtime/sandbox/winacl v0.1.1",
		"github.com/buchenberg/goatherd-runtime/sandbox/seatbelt v0.1.0",
		"github.com/buchenberg/goatherd-croft v0.9.2-0.20261031191238-a6d5f72a9b04",
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

// TestLedgerNamesTheContainmentFamily pins the family's members. Dropping a
// platform from the ledger while it ships is a documentation defect; the pin
// list above catches the module version drift.
func TestLedgerNamesTheContainmentFamily(t *testing.T) {
	ledger := strings.ToLower(read(t, ledgerPath))
	for _, member := range []string{"landlock", "winacl", "seatbelt"} {
		if !strings.Contains(ledger, member) {
			t.Errorf("ledger does not name the %q containment member", member)
		}
	}
}

// TestLedgerNamesUsefulSurfaceOwners keeps the full parity scorecard tied to
// the epic that owns it. The dispatch-world ledger used to list closed beads as
// "remaining"; this guard makes the owner list falsifiable.
func TestLedgerNamesUsefulSurfaceOwners(t *testing.T) {
	ledger := read(t, ledgerPath)
	for _, ref := range []string{
		"yaah-hxk3",
		"yaah-hxk3.1",
		"yaah-hxk3.2",
		"yaah-hxk3.3",
		"yaah-hxk3.4",
		"yaah-hxk3.5",
		"yaah-kit",
		"yaah-1tkp.1",
		"yaah-bp4",
	} {
		if !strings.Contains(ledger, "`"+ref+"`") {
			t.Errorf("parity ledger does not name %s as a bounded bead reference; the scorecard owner list drifted", ref)
		}
	}
	marker := "Remaining useful framework work"
	at := strings.Index(ledger, marker)
	if at < 0 {
		t.Fatalf("parity ledger has no %q section", marker)
	}
	remaining := ledger[at:]
	for _, closed := range []string{"yaah-vlwm", "yaah-qlf9"} {
		if strings.Contains(remaining, closed) {
			t.Errorf("parity ledger still lists closed bead %s as remaining work", closed)
		}
	}
}

// TestLedgerStatesDeliberateBypasses pins the decisions that explain why the
// Python task/materialize surfaces are absent. Without these, a reader sees an
// unimplemented feature rather than a recorded divergence.
func TestLedgerStatesDeliberateBypasses(t *testing.T) {
	ledger := read(t, ledgerPath)
	for _, marker := range []string{"D6 — TaskControl", "D7 — materialize"} {
		if !strings.Contains(ledger, marker) {
			t.Errorf("parity ledger does not record the deliberate bypass %q", marker)
		}
	}
}
