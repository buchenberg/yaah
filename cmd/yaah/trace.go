package yaah

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"

	shepherdtrace "github.com/buchenberg/shepherd-kernel-go"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(newShepherdTraceCmd())
}

func newShepherdTraceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shepherd-trace",
		Short: "Inspect and manage Shepherd execution traces",
	}

	cmd.AddCommand(newShepherdTraceListCmd())
	cmd.AddCommand(newShepherdTraceShowCmd())
	cmd.AddCommand(newShepherdTraceProfileCmd())

	return cmd
}

func newShepherdTraceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List recent trace sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openShepherdTraceStore()
			if err != nil {
				return err
			}
			defer store.Close(cmd.Context())

			out := cmd.OutOrStdout()
			slice, err := store.ReadOwnerPrefix(
				cmd.Context(),
				shepherdtrace.TrustedReadContext,
				"", 99999, shepherdtrace.ModeDeclarationsOnly,
			)
			if err != nil {
				return fmt.Errorf("read traces: %w", err)
			}

			owners := make(map[string]bool)
			for owner := range slice.OwnerPaths {
				owners[owner] = true
			}

			if len(owners) == 0 {
				fmt.Fprintln(out, "No trace sessions found.")
				return nil
			}

			sorted := sortedKeys(owners)

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION\tFACTS")
			for _, owner := range sorted {
				fmt.Fprintf(w, "%s\t%d\n", owner, len(slice.OwnerPaths[owner]))
			}
			w.Flush()
			return nil
		},
	}
}

func newShepherdTraceShowCmd() *cobra.Command {
	var latest bool

	cmd := &cobra.Command{
		Use:   "show [session-id]",
		Short: "Show tool calls in a trace session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openShepherdTraceStore()
			if err != nil {
				return err
			}
			defer store.Close(cmd.Context())

			out := cmd.OutOrStdout()
			var sessionID string
			if latest || len(args) == 0 {
				slice, err := store.ReadOwnerPrefix(
					cmd.Context(),
					shepherdtrace.TrustedReadContext,
					"", 99999, shepherdtrace.ModeDeclarationsOnly,
				)
				if err != nil {
					return fmt.Errorf("read traces: %w", err)
				}
				if len(slice.OwnerPaths) == 0 {
					fmt.Fprintln(out, "No trace sessions found.")
					return nil
				}
				ownerIDs := make([]string, 0, len(slice.OwnerPaths))
				for k := range slice.OwnerPaths {
					ownerIDs = append(ownerIDs, k)
				}
				sort.Sort(sort.Reverse(sort.StringSlice(ownerIDs)))
				sessionID = ownerIDs[0]
			} else {
				sessionID = args[0]
			}

			slice, err := store.ReadOwnerPrefix(
				cmd.Context(),
				shepherdtrace.TrustedReadContext,
				sessionID, 99999, shepherdtrace.ModeBoth,
			)
			if err != nil {
				return fmt.Errorf("read session: %w", err)
			}

			if len(slice.FactIDs()) == 0 {
				fmt.Fprintf(out, "No tool calls found in session %s.\n", sessionID)
				return nil
			}

			captureStatusByParent := make(map[string]string)
			for _, factID := range slice.FactIDs() {
				fact := slice.FactsByID[factID]
				if fact.GetEnvelope().Mode == shepherdtrace.Capture {
					parents := fact.GetEnvelope().CausedByIDs
					if len(parents) > 0 {
						parentID := parents[0]
						captureStatusByParent[parentID] = captureStatus(fact)
					}
				}
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Session: %s\n\n", sessionID)
			fmt.Fprintln(w, "#\tTOOL\tARGS\tSTATUS")

			num := 0
			for _, factID := range slice.FactIDs() {
				fact := slice.FactsByID[factID]
				if fact.GetEnvelope().Mode != shepherdtrace.Declaration {
					continue
				}

				num++
				tool := fact.GetView().KindLabel
				argsJSON := ""
				if rec, ok := fact.(shepherdtrace.Record); ok {
					if args, ok2 := rec.Body.Payload["args"]; ok2 {
						switch a := args.(type) {
						case json.RawMessage:
							argsJSON = truncateString(string(a), 60)
						case string:
							argsJSON = truncateString(a, 60)
						default:
							b, _ := json.Marshal(a)
							argsJSON = truncateString(string(b), 60)
						}
					}
				}

				status := "pending"
				if s, ok := captureStatusByParent[factID]; ok {
					status = s
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", num, tool, argsJSON, status)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().BoolVar(&latest, "latest", false, "Show the most recent session")

	return cmd
}

func newShepherdTraceProfileCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "profile [session-id]",
		Short: "Show execution profile for a trace session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openShepherdTraceStore()
			if err != nil {
				return err
			}
			defer store.Close(cmd.Context())

			out := cmd.OutOrStdout()
			var sessionID string
			if len(args) == 0 {
				slice, err := store.ReadOwnerPrefix(
					cmd.Context(),
					shepherdtrace.TrustedReadContext,
					"", 99999, shepherdtrace.ModeDeclarationsOnly,
				)
				if err != nil {
					return fmt.Errorf("read traces: %w", err)
				}
				if len(slice.OwnerPaths) == 0 {
					fmt.Fprintln(out, "No trace sessions found.")
					return nil
				}
				ownerIDs := make([]string, 0, len(slice.OwnerPaths))
				for k := range slice.OwnerPaths {
					ownerIDs = append(ownerIDs, k)
				}
				sort.Sort(sort.Reverse(sort.StringSlice(ownerIDs)))
				sessionID = ownerIDs[0]
			} else {
				sessionID = args[0]
			}

			slice, err := store.ReadOwnerPrefix(
				cmd.Context(),
				shepherdtrace.TrustedReadContext,
				sessionID, 99999, shepherdtrace.ModeBoth,
			)
			if err != nil {
				return fmt.Errorf("read session: %w", err)
			}

			if len(slice.FactIDs()) == 0 {
				fmt.Fprintf(out, "No facts found in session %s.\n", sessionID)
				return nil
			}

			captureByParent := make(map[string]captureRecord)
			for _, factID := range slice.FactIDs() {
				fact := slice.FactsByID[factID]
				if fact.GetEnvelope().Mode != shepherdtrace.Capture {
					continue
				}
				causedBy := fact.GetEnvelope().CausedByIDs
				if len(causedBy) == 0 {
					continue
				}
				parentID := causedBy[0]
				captureByParent[parentID] = readCapturePayload(fact)
			}

			type toolRecord struct {
				name     string
				args     string
				duration string
				success  bool
				errMsg   string
			}
			type turnRecord struct {
				number           int
				status           string
				tools            []toolRecord
				promptTokens     int
				completionTokens int
			}

			var turns []turnRecord
			var current *turnRecord

			for _, factID := range slice.FactIDs() {
				fact := slice.FactsByID[factID]
				if fact.GetEnvelope().Mode != shepherdtrace.Declaration {
					continue
				}

				kind := fact.GetView().KindLabel

				if kind == "turn:created" {
					cap := captureByParent[factID]
					turn := turnRecord{
						status:           cap.turnStatus,
						promptTokens:     cap.promptTokens,
						completionTokens: cap.completionTokens,
					}
					if rec, ok := fact.(shepherdtrace.Record); ok {
						if n, ok2 := payloadNumber(rec.Body.Payload["turn"]); ok2 {
							turn.number = int(n)
						}
					}
					turns = append(turns, turn)
					current = &turns[len(turns)-1]
					continue
				}

				if current == nil {
					continue
				}

				cap := captureByParent[factID]
				tool := toolRecord{
					name:     kind,
					success:  cap.success,
					errMsg:   cap.errMsg,
					duration: cap.duration,
				}
				if rec, ok := fact.(shepherdtrace.Record); ok {
					if args, ok2 := rec.Body.Payload["args"]; ok2 {
						b, _ := json.Marshal(args)
						tool.args = truncateString(string(b), 50)
					}
				}
				current.tools = append(current.tools, tool)
			}

			if len(turns) == 0 {
				fmt.Fprintln(out, "No turns found.")
				return nil
			}

			var totalTools, totalSuccess int
			var totalPrompt, totalCompletion int
			toolStats := make(map[string]struct {
				count   int
				success int
				durSum  time.Duration
				durN    int
			})

			for _, t := range turns {
				for _, tool := range t.tools {
					totalTools++
					if tool.success {
						totalSuccess++
					}
					ts := toolStats[tool.name]
					ts.count++
					if tool.success {
						ts.success++
					}
					if d, dErr := time.ParseDuration(tool.duration); dErr == nil {
						ts.durSum += d
						ts.durN++
					}
					toolStats[tool.name] = ts
				}
				totalPrompt += t.promptTokens
				totalCompletion += t.completionTokens
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

			fmt.Fprintf(w, "Session:\t%s\n", sessionID)
			fmt.Fprintf(w, "Turns:\t%d\tTools:\t%d\n", len(turns), totalTools)
			fmt.Fprintf(w, "Tokens:\t%s prompt\t%s completion\n",
				formatNum(totalPrompt), formatNum(totalCompletion))
			if totalTools > 0 {
				rate := float64(totalSuccess) / float64(totalTools) * 100
				fmt.Fprintf(w, "Success:\t%d/%d (%.0f%%)\n", totalSuccess, totalTools, rate)
			}

			fmt.Fprintln(w)

			for _, t := range turns {
				tag := t.status
				if tag == "" {
					tag = "continued"
				}
				fmt.Fprintf(w, "T%d\t%s", t.number+1, tag)
				if t.promptTokens > 0 {
					fmt.Fprintf(w, "\t%s + %s tokens",
						formatNum(t.promptTokens), formatNum(t.completionTokens))
				}
				fmt.Fprintln(w)

				for _, tool := range t.tools {
					status := "ok"
					if tool.errMsg != "" {
						status = "error: " + tool.errMsg
					}
					dur := tool.duration
					if dur != "" {
						dur = " " + dur
					}
					fmt.Fprintf(w, "  %s\t%s%s\t%s\n", tool.name, tool.args, dur, status)
				}
			}

			if len(toolStats) > 0 {
				fmt.Fprintln(w)
				fmt.Fprintln(w, "TOOL\tCALLS\tSUCCESS\tAVG DUR")
				names := make([]string, 0, len(toolStats))
				for n := range toolStats {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					ts := toolStats[n]
					rate := float64(ts.success) / float64(ts.count) * 100
					avgDur := ""
					if ts.durN > 0 {
						avgDur = (ts.durSum / time.Duration(ts.durN)).String()
					}
					fmt.Fprintf(w, "%s\t%d\t%.0f%%\t%s\n", n, ts.count, rate, avgDur)
				}
			}

			w.Flush()
			return nil
		},
	}
}

type captureRecord struct {
	success          bool
	errMsg           string
	duration         string
	turnStatus       string
	promptTokens     int
	completionTokens int
}

func readCapturePayload(fact shepherdtrace.VisibleRecord) captureRecord {
	rec, ok := fact.(shepherdtrace.Record)
	if !ok {
		return captureRecord{}
	}
	success, _ := rec.Body.Payload["success"].(bool)
	errMsg := ""
	if e, ok2 := rec.Body.Payload["error"].(string); ok2 {
		errMsg = e
	}
	duration := ""
	if d, ok2 := rec.Body.Payload["duration"].(string); ok2 {
		duration = d
	}

	cr := captureRecord{
		success:  success,
		errMsg:   errMsg,
		duration: duration,
	}

	kind := rec.View.KindLabel
	if kind == "turn:completed" {
		cr.turnStatus = "completed"
	} else if kind == "turn:failed" {
		cr.turnStatus = "failed"
	}
	if kind == "turn:completed" || kind == "turn:failed" {
		if pt, ok2 := payloadNumber(rec.Body.Payload["prompt_tokens"]); ok2 {
			cr.promptTokens = int(pt)
		}
		if ct, ok2 := payloadNumber(rec.Body.Payload["completion_tokens"]); ok2 {
			cr.completionTokens = int(ct)
		}
	}

	return cr
}

func formatNum(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func openShepherdTraceStore() (*shepherdtrace.SQLiteTraceStore, error) {
	cfg, _ := config.Load()
	path := config.Defaults{}.TracePath()
	if cfg != nil {
		path = cfg.Agent.Default.TracePath()
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no trace store at %s (it is created on first use)", path)
	}
	return shepherdtrace.NewSQLiteTraceStore(path)
}

func captureStatus(fact shepherdtrace.VisibleRecord) string {
	rec, ok := fact.(shepherdtrace.Record)
	if !ok {
		return "pending"
	}
	success, _ := rec.Body.Payload["success"].(bool)
	hasError := rec.Body.Payload["error"] != nil
	if hasError {
		return "error"
	}
	if success {
		return "ok"
	}
	return "pending"
}

// payloadNumber reads a numeric payload field across the kernel's encodings:
// records stored before the canonical-parity work (kernel v0.5.0) hold
// float64, newer ones hold the canonical string form. Both parse; anything
// else reports absent.
func payloadNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	case json.Number:
		f, err := strconv.ParseFloat(n.String(), 64)
		return f, err == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
