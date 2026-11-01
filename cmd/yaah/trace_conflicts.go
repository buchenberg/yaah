package yaah

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/spf13/cobra"
)

// trace_conflicts.go implements the conflict-evidence report used to decide
// whether preventive dispatch path claims are justified. It is read-only: the
// report never changes a proposal or a trace.

type conflictPathCount struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
}

type conflictAggregate struct {
	Window                      string              `json:"window"`
	Sessions                    int                 `json:"sessions"`
	Dispatches                  map[string]int      `json:"dispatches"`
	SealedProposals             int                 `json:"sealed_proposals"`
	Conflicts                   int                 `json:"conflicts"`
	ConflictSessions            int                 `json:"conflict_sessions"`
	ConflictProposals           int                 `json:"conflict_proposals"`
	FanoutSessions              int                 `json:"fanout_sessions"`
	FanoutSessionsWithConflicts int                 `json:"fanout_sessions_with_conflicts"`
	Kinds                       map[string]int      `json:"kinds"`
	RepeatedPaths               []conflictPathCount `json:"repeated_paths"`
}

func newShepherdTraceConflictsCmd() *cobra.Command {
	var asJSON bool
	var session string

	cmd := &cobra.Command{
		Use:   "conflicts",
		Short: "Aggregate conflict evidence for the prevention decision",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openShepherdTraceStore()
			if err != nil {
				return err
			}
			defer store.Close(cmd.Context())

			slice, err := store.ReadOwnerPrefix(
				cmd.Context(),
				shepherdtrace.TrustedReadContext,
				session, int(^uint(0)>>1), shepherdtrace.ModeBoth,
			)
			if err != nil {
				return fmt.Errorf("read conflict evidence: %w", err)
			}
			report := aggregateConflicts(slice, session)

			if asJSON {
				raw, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				raw = append(raw, '\n')
				_, err = cmd.OutOrStdout().Write(raw)
				return err
			}
			writeConflictAggregate(cmd.OutOrStdout(), report)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the aggregate as JSON")
	cmd.Flags().StringVar(&session, "session", "", "limit the report to one trace owner/session")
	return cmd
}

func aggregateConflicts(slice shepherdtrace.Slice, session string) conflictAggregate {
	window := "all retained trace facts"
	if session != "" {
		window = "session " + session
	}
	report := conflictAggregate{
		Window:     window,
		Dispatches: make(map[string]int),
		Kinds:      make(map[string]int),
	}
	paths := make(map[string]int)
	conflictProposals := make(map[string]bool)
	sessionDispatches := make(map[string]int)
	sessionConflicts := make(map[string]bool)
	sessionsSeen := make(map[string]bool)

	for owner, factIDs := range slice.OwnerPaths {
		for _, factID := range factIDs {
			fact := slice.FactsByID[factID]
			rec, ok := fact.(shepherdtrace.Record)
			if !ok {
				continue
			}
			switch rec.Envelope.SchemaRef {
			case "yaah.subagent.usage.v1":
				sessionsSeen[owner] = true
				lane, _ := rec.Body.Payload["lane"].(string)
				if lane == "" {
					lane = "unknown"
				}
				report.Dispatches[lane]++
				sessionDispatches[owner]++
			case tools.SchemaProposalSealed:
				sessionsSeen[owner] = true
				report.SealedProposals++
			case tools.SchemaProposalConflict:
				sessionsSeen[owner] = true
				report.Conflicts++
				sessionConflicts[owner] = true
				kind, _ := rec.Body.Payload["kind"].(string)
				if kind == "" {
					kind = "unknown"
				}
				report.Kinds[kind]++
				for _, path := range payloadStrings(rec.Body.Payload["paths"]) {
					if path != "" {
						paths[path]++
					}
				}
				for _, id := range payloadStrings(rec.Body.Payload["proposals"]) {
					if id != "" {
						conflictProposals[id] = true
					}
				}
			}
		}
	}

	report.Sessions = len(sessionsSeen)
	report.ConflictSessions = len(sessionConflicts)
	report.ConflictProposals = len(conflictProposals)
	for owner, dispatches := range sessionDispatches {
		if dispatches <= 1 {
			continue
		}
		report.FanoutSessions++
		if sessionConflicts[owner] {
			report.FanoutSessionsWithConflicts++
		}
	}
	for path, count := range paths {
		if count > 1 {
			report.RepeatedPaths = append(report.RepeatedPaths, conflictPathCount{Path: path, Count: count})
		}
	}
	sort.Slice(report.RepeatedPaths, func(i, j int) bool {
		if report.RepeatedPaths[i].Count != report.RepeatedPaths[j].Count {
			return report.RepeatedPaths[i].Count > report.RepeatedPaths[j].Count
		}
		return report.RepeatedPaths[i].Path < report.RepeatedPaths[j].Path
	})
	return report
}

func writeConflictAggregate(out io.Writer, report conflictAggregate) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Conflict evidence: %s\n\n", report.Window)
	fmt.Fprintf(w, "sessions\t%d\n", report.Sessions)
	total := 0
	for _, n := range report.Dispatches {
		total += n
	}
	fmt.Fprintf(w, "dispatches\t%d\n", total)
	for _, lane := range sortedLaneNames(report.Dispatches) {
		fmt.Fprintf(w, "  %s\t%d\n", lane, report.Dispatches[lane])
	}
	fmt.Fprintf(w, "retained proposals sealed\t%d\n", report.SealedProposals)
	fmt.Fprintf(w, "proposal conflicts\t%d\n", report.Conflicts)
	fmt.Fprintf(w, "conflict sessions\t%d\n", report.ConflictSessions)
	fmt.Fprintf(w, "conflict proposals\t%d\n", report.ConflictProposals)
	fmt.Fprintf(w, "sessions with >1 dispatch\t%d\n", report.FanoutSessions)
	fmt.Fprintf(w, "  ...with conflicts\t%d\n", report.FanoutSessionsWithConflicts)
	if len(report.Kinds) > 0 {
		fmt.Fprintln(w, "\nkinds")
		for _, kind := range sortedLaneNames(report.Kinds) {
			fmt.Fprintf(w, "  %s\t%d\n", kind, report.Kinds[kind])
		}
	}
	if len(report.RepeatedPaths) > 0 {
		fmt.Fprintln(w, "\nrepeated paths")
		for _, p := range report.RepeatedPaths {
			fmt.Fprintf(w, "  %s\t%d\n", p.Path, p.Count)
		}
	}
	w.Flush()
	fmt.Fprintln(out, "\nnote: fan-out is session-level here; turn ids are not recorded in trace facts.")
}

func sortedLaneNames(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func payloadStrings(v any) []string {
	switch value := v.(type) {
	case []string:
		return value
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if value == "" {
			return nil
		}
		return []string{value}
	default:
		return nil
	}
}
