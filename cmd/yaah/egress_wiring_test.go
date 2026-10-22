package yaah

import (
	"strings"
	"testing"

	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"

	"github.com/buchenberg/yaah/internal/config"
)

// Plan 7 Phase 3: agents.subagent.network is lowered into the posture a confined
// dispatch carries, and "broker" actually starts a broker — a mode that pins
// egress to a port needs a port to pin to.
func TestWireSubAgentNetwork(t *testing.T) {
	t.Run("allow_all is the default and starts nothing", func(t *testing.T) {
		for _, mode := range []string{"", "allow_all"} {
			np, broker, err := wireSubAgentNetwork(config.SubAgentConfig{Network: mode})
			if err != nil {
				t.Fatalf("mode %q: %v", mode, err)
			}
			if broker != nil {
				t.Errorf("mode %q must not start a broker", mode)
			}
			if np.Mode != confinement.NetAllowAll {
				t.Errorf("mode %q -> %q, want allow_all", mode, np.Mode)
			}
		}
	})

	t.Run("deny_all needs no broker", func(t *testing.T) {
		np, broker, err := wireSubAgentNetwork(config.SubAgentConfig{Network: "deny_all"})
		if err != nil {
			t.Fatalf("deny_all: %v", err)
		}
		if broker != nil {
			t.Error("deny_all must not start a broker")
		}
		if np.Mode != confinement.NetDenyAll {
			t.Errorf("deny_all -> %q", np.Mode)
		}
	})

	t.Run("broker starts one and pins to its port", func(t *testing.T) {
		np, broker, err := wireSubAgentNetwork(config.SubAgentConfig{
			Network:      "broker",
			NetworkHosts: []string{"api.example.com"},
		})
		if err != nil {
			t.Fatalf("broker: %v", err)
		}
		if broker == nil {
			t.Fatal("broker mode must start a broker")
		}
		defer broker.Stop()
		if np.Mode != confinement.NetBroker || np.BrokerPort <= 0 {
			t.Fatalf("policy = %+v, want broker on a live port", np)
		}
		if np.BrokerPort != broker.Port() {
			t.Errorf("pinned port %d != the broker's %d", np.BrokerPort, broker.Port())
		}
	})

	t.Run("allowlist hosts are normalized to what the broker matches against", func(t *testing.T) {
		// An exact-host allowlist compares normalized host names, so an entry
		// that is padded, upper-cased or trailing-dotted would otherwise never
		// match — a rule that looks present and admits nothing.
		got := normalizeBrokerHosts([]string{" API.Example.com ", "api.example.com.", "", "  "})
		want := []string{"api.example.com", "api.example.com"}
		if len(got) != len(want) {
			t.Fatalf("normalizeBrokerHosts = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("host %d = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("an unknown mode is refused", func(t *testing.T) {
		if _, _, err := wireSubAgentNetwork(config.SubAgentConfig{Network: "borker"}); err == nil {
			t.Fatal("an unknown mode must be refused rather than defaulted")
		} else if !strings.Contains(err.Error(), "borker") {
			t.Errorf("the error should name the mode, got %v", err)
		}
	})
}
