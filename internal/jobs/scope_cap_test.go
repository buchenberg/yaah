package jobs

import (
	"context"
	"path/filepath"
	"testing"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	goatherd "github.com/buchenberg/goatherd-runtime"
)

func TestResolveDispatchScopeCap(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero selects default", 0, DefaultMaxDispatchScopes},
		{"negative selects default", -7, DefaultMaxDispatchScopes},
		{"explicit cap respected", 16, 16},
		{"one is a legal cap", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveDispatchScopeCap(tt.in); got != tt.want {
				t.Fatalf("ResolveDispatchScopeCap(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestDefaultMaxDispatchScopesIsPositive(t *testing.T) {
	// The cap guards a leak; a default of zero or less would mean "no cap".
	if DefaultMaxDispatchScopes <= 0 {
		t.Fatalf("DefaultMaxDispatchScopes = %d, want > 0", DefaultMaxDispatchScopes)
	}
}

// TestDispatchScopeCount pins the counting rule the two dispatch paths
// share (yaah-95ri.7): active scopes minus the ones startup recovery
// re-admitted from the trace, which hold no loop and therefore no
// capacity.
func TestDispatchScopeCount(t *testing.T) {
	store, err := shepherdtrace.NewSQLiteTraceStore(filepath.Join(t.TempDir(), "trace.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	mgr := goatherd.NewScopeManager(store)
	root, err := mgr.Create("count-root", nil)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	child, err := mgr.Fork(root.ID(), "count-child", nil)
	if err != nil {
		t.Fatalf("fork child: %v", err)
	}
	active := mgr.ActiveScopes()

	tests := []struct {
		name    string
		resumed map[string]bool
		want    int
	}{
		{"nothing resumed counts every active scope", nil, len(active)},
		{"resumed scopes hold no capacity", map[string]bool{root.ID(): true, child.ID(): true}, 0},
		{"a partially resumed tree counts the rest", map[string]bool{child.ID(): true}, len(active) - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DispatchScopeCount(active, tt.resumed); got != tt.want {
				t.Fatalf("DispatchScopeCount = %d, want %d", got, tt.want)
			}
		})
	}

	t.Run("nil scopes are not capacity", func(t *testing.T) {
		if got := DispatchScopeCount([]*goatherd.Scope{nil, nil}, nil); got != 0 {
			t.Fatalf("DispatchScopeCount(nils) = %d, want 0", got)
		}
	})
}
