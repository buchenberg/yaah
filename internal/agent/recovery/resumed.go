package recovery

import (
	"sort"

	goatherd "github.com/buchenberg/goatherd-runtime"
)

// ResumedExecution is the execution outcome recovery records for a scope
// it re-admitted from the trace (yaah-95ri.7). A resumed scope holds no
// loop in this process: the run that owned it died with the previous
// process, so nothing will ever execute under it again until the scope
// is retired. Naming the state is what keeps "the trace says this scope
// was active" from being read as "something is running under it now" —
// the conflation that let dead scopes hold the dispatch cap.
const ResumedExecution = "resume-complete (recovered)"

// Resumed is one ACTIVE scope startup recovery re-admitted from the
// durable trace. It is a real entry in the rebuilt tree — the kernel
// re-hangs its parent, which is what lets a parent resolve a child it no
// longer runs — but it has no execution in this process. Two invariants
// ride on that (yaah-95ri.7):
//
//   - the dispatch cap must not count it: a trace full of crashed
//     dispatches would otherwise refuse every dispatch of the new
//     process (jobs.DispatchScopeCount excludes these IDs);
//   - the reaper must be able to sweep it without a grace window it
//     never earned: its unbridged clock started before the process did
//     (the reaper's resumed set reaps it on the startup pass).
type Resumed struct {
	// ScopeID is the kernel scope ID ("scope:<owner>").
	ScopeID string
	// Parent is the parent scope's ID, empty for a root. It makes the
	// rebuilt tree shape visible: a resumed child whose parent is also
	// resumed is one dead subtree, not a stray scope.
	Parent string
	// Outcome is ResumedExecution — the execution state recovery records
	// for the scope.
	Outcome string
}

// ResumedScopes lists the manager's ACTIVE scopes as resumed entries,
// sorted by scope ID. Recovery runs in a fresh process that has forked
// nothing yet, so every active scope in the rebuilt manager came from
// the trace; the composition root calls this straight after the rebuild
// and hands the result to the reaper and to the dispatch-cap accounting.
// A nil manager yields nil.
func ResumedScopes(mgr *goatherd.ScopeManager) []Resumed {
	if mgr == nil {
		return nil
	}
	active := mgr.ActiveScopes()
	out := make([]Resumed, 0, len(active))
	for _, sc := range active {
		if sc == nil {
			continue
		}
		entry := Resumed{ScopeID: sc.ID(), Outcome: ResumedExecution}
		if parent := sc.Parent(); parent != nil {
			entry.Parent = parent.ID()
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScopeID < out[j].ScopeID })
	return out
}

// ResumedIDs maps resumed entries to the scope-ID set the reaper and the
// dispatch-cap accounting consume. Empty input yields nil: an absent set
// must read as "nothing was resumed", never as an empty map to test.
func ResumedIDs(resumed []Resumed) map[string]bool {
	if len(resumed) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(resumed))
	for _, entry := range resumed {
		ids[entry.ScopeID] = true
	}
	return ids
}
