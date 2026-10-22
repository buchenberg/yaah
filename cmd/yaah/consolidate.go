package yaah

import (
	"context"
	"log/slog"
	"strings"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
)

// consolidateKindMemory is the only product the nudge consolidates today:
// durable memory atoms (docs/compaction-as-sleep.md §8 P3, "deep sleep").
// Later phases add "skills".
const consolidateKindMemory = "memory"

// consolidatorRole is the role the deep-sleep dispatch runs as: the profile in
// .agents/roles/consolidator.md, whose tool list is memory_add +
// memory_search only — no file writes, no shell, no delegate — so a
// background consolidation can neither mutate the workspace nor fork further
// sub-agents.
//
// The recursion guard is structural: the sub-agent pipeline
// (pipeline.NewSubAgentPipeline) omits the "consolidate" middleware, so a
// consolidator can never re-trigger itself.
const consolidatorRole = "consolidator"

// delegateToolName is the registry key the sub-agent runner is reached
// through. Reusing the session's DelegateTool is what makes the consolidator a
// real sub-agent: it inherits the same provider/model, context window, budget
// resolution, and trace wiring as any other dispatch, instead of a bespoke
// loop built here.
const delegateToolName = "delegate"

// The transcript handed to the consolidator is bounded twice over, so a single
// giant tool result can neither dominate the prompt nor blow the sub-agent's
// budget.
const (
	consolidateTranscriptMessages = 16
	consolidateTranscriptBytes    = 8000
	consolidateMessageBytes       = 600
)

// consolidateDirective is the consolidator's short instruction preamble. The
// scope and the provenance tag are stated here as well as enforced by the
// write path, so the model's intent and the stored rows agree.
const consolidateDirective = `Consolidate the recent conversation below into durable memory.

Write only atoms that should outlive this conversation: decisions taken, facts
learned about the project or user, and constraints discovered the hard way.
Skip ephemeral tool output, transient errors, and in-progress speculation. Do
not summarise the conversation into prose — persist discrete atoms.

For every atom call memory_add with:
- tags: a JSON array that always contains "source:compaction", plus any
  specific tags that apply (e.g. ["source:compaction","decision"]).
- scope: "session" — these atoms were distilled from this session's work.

If nothing is durable, write nothing and report "0 memories".`

// consolidatePrompt renders the consolidator's prompt: the directive plus a
// bounded tail of the recent conversation.
func consolidatePrompt(msgs []types.Message) string {
	var b strings.Builder
	b.WriteString(consolidateDirective)
	b.WriteString("\n\nRecent conversation (oldest first):\n")
	start := 0
	if len(msgs) > consolidateTranscriptMessages {
		start = len(msgs) - consolidateTranscriptMessages
	}
	for _, m := range msgs[start:] {
		line := m.Role + ": " + collapseLine(m.Content)
		if b.Len()+len(line) >= consolidateTranscriptBytes {
			b.WriteString("\n[transcript truncated]\n")
			break
		}
		b.WriteString("\n")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// collapseLine flattens a message to one bounded, valid-UTF-8 line.
func collapseLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > consolidateMessageBytes {
		s = strings.ToValidUTF8(s[:consolidateMessageBytes], "") + " …"
	}
	return s
}

// dispatchConsolidation runs a consolidator sub-agent out-of-band: the deep
// sleep of docs/compaction-as-sleep.md §8 P3.
//
// It is fire-and-forget in every direction: the caller returns immediately,
// the dispatch runs on a detached goroutine whose context keeps the parent's
// values (trace, session) but not its cancellation or deadline, and a panic or
// error is logged rather than propagated — a background consolidation must
// never fail or block the orchestrator loop.
//
// runner and transcript are functions so the composition root can resolve them
// lazily (the registry and the conversation both evolve after the loop is
// built) and so the dispatch is testable without a live LLM.
func dispatchConsolidation(ctx context.Context, kind string, runner func() tools.TaskRunner, transcript func() []types.Message) {
	if kind != consolidateKindMemory || runner == nil || transcript == nil {
		return
	}
	// Detach: the nudge fires mid-turn, and the consolidator legitimately
	// outlives the turn (or the run) that triggered it.
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("consolidate: consolidator dispatch panicked", "kind", kind, "panic", r)
			}
		}()
		r := runner()
		if r == nil {
			slog.Debug("consolidate: no sub-agent runner available; skipping dispatch", "kind", kind)
			return
		}
		out, err := r(ctx, consolidatePrompt(transcript()), tools.SubAgentParams{Role: consolidatorRole})
		if err != nil {
			slog.Warn("consolidate: consolidator failed", "kind", kind, "err", err)
			return
		}
		slog.Debug("consolidate: consolidator finished",
			"kind", kind, "result", strings.TrimSpace(collapseLine(out)))
	}()
}

// consolidateFn builds the session's Loop.ConsolidateFn. Nil when the session
// has no tool registry (there is nothing to dispatch through), which keeps the
// nudge inert exactly like an unassigned hook.
func (s *agentSession) consolidateFn() func(ctx context.Context, kind string) {
	if s == nil || s.toolReg == nil {
		return nil
	}
	runner := func() tools.TaskRunner {
		d, ok := s.toolReg.Get(delegateToolName).(*tools.DelegateTool)
		if !ok || d == nil {
			return nil
		}
		return d.Runner
	}
	return func(ctx context.Context, kind string) {
		dispatchConsolidation(ctx, kind, runner, s.History)
	}
}
