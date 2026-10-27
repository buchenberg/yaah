package pipeline

// intentloop.go: the intent-repetition detector (yaah-v378.3, epic
// yaah-v378). The recorded 2026-10-26/27 loops were not tool-result loops —
// the agent re-announced the same plan ("All four findings are real.
// Implementing now…") for a dozen consecutive turns while issuing 2-4
// VARIED read calls per turn, so loop_detection (identical tool+args+
// result) never fired and the convergence nudge (same tool five times IN
// ONE turn) never saw it either.
//
// This detector looks across turns: it normalizes the assistant's text
// preamble, compares consecutive turns by token-set similarity, and counts
// a streak of near-identical preambles. A mutating tool call means progress
// and resets the streak. At the steer threshold the model is told, in the
// conversation, to act or conclude; at the halt threshold the run ends
// honestly instead of burning another hour.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// IntentLoopError halts a run that keeps re-announcing the same plan with
// no workspace change. Use errors.Is with a zero value to match:
//
//	errors.Is(err, IntentLoopError{})
type IntentLoopError struct {
	Streak int
	Steer  int
	Halt   int
}

func (e IntentLoopError) Error() string {
	return fmt.Sprintf("intent loop: the assistant re-announced the same plan %d turns in a row without changing any files — halting (steer %d, halt %d); if the fix is known, restate it and edit",
		e.Streak, e.Steer, e.Halt)
}

func (e IntentLoopError) Is(target error) bool {
	_, ok := target.(IntentLoopError)
	return ok
}

const (
	// Defaults agreed 2026-10-27: the recorded loops ran 12+ turns, so a
	// steer at four gives the model room to correct itself first.
	defaultIntentSteerAfter = 4
	defaultIntentHaltAfter  = 8

	// intentPreambleWords bounds the normalized signature to the opening
	// plan statement; long tails (new tool output quoted back) dilute
	// similarity.
	intentPreambleWords = 24

	// intentMinTokens ignores preambles too short to carry a plan ("Let me
	// read the tests"), where similarity is noise.
	intentMinTokens = 8

	// intentSimilarityThreshold is the token-set OVERLAP COEFFICIENT
	// (|A∩B| / min(|A|,|B|)) against the streak anchor at or above which
	// a preamble counts as the same intent. Tuned on the recorded
	// 2026-10-26/27 loops: consecutive paraphrased announcements scored
	// 0.21–0.38 Jaccard but 0.33–0.53 overlap against the anchor, while
	// unrelated consecutive turns sat around 0.20. Jaccard was rejected:
	// the model paraphrases ("verified as real" / "are legitimate"), and
	// set Jaccard punishes exactly that variation.
	intentSimilarityThreshold = 0.30
)

// delegateStatusWindow bounds the head inspected for a delegate status
// envelope: the status field is first, and the tool-result cap can truncate
// the diff that follows.
const delegateStatusWindow = 512

// delegateStatus matches a head-anchored status envelope, tolerating the
// truncation that cuts the diff tail.
var delegateStatus = regexp.MustCompile(`^\s*\{\s*"status"\s*:\s*"([A-Za-z _-]+)"`)

// delegateFailure matches structuredTaskResult's shape: failures come back
// as {"error":"timed out",...} with a nil Go error.
var delegateFailure = regexp.MustCompile(`^\s*\{\s*"error"\s*:`)

// madeProgress reports whether a successful tool call advanced the plan
// rather than gathering more information. File mutations reuse
// mutationSucceeded (which understands dry_run and per-tool read actions);
// state-changing module, git, and plan actions count too. Unknown tools
// (MCP) count as non-progress: the steer is advisory and the halt is the
// backstop, and treating unclassifiable calls as progress would disable the
// detector for the read-only loops it exists to catch.
func madeProgress(r ToolResult) bool {
	if r.Error != nil {
		return false
	}
	if mutationSucceeded(r) {
		return true
	}
	switch r.Name {
	case "go_mod":
		var p struct {
			Action string `json:"action"`
			DryRun bool   `json:"dry_run"`
		}
		if json.Unmarshal([]byte(r.Args), &p) != nil || p.DryRun {
			return false
		}
		switch p.Action {
		case "add", "remove", "upgrade", "tidy":
			return true
		}
	case "git":
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(r.Args), &p) != nil {
			return false
		}
		switch p.Action {
		case "add", "commit", "push", "pull":
			return true
		}
	case "plan":
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(r.Args), &p) != nil {
			return false
		}
		switch p.Action {
		case "create", "edit", "delete", "approve":
			return true
		}
	case "delegate":
		// A dispatch counts as progress when it produced work: a sealed
		// proposal or a report. Only the HEAD of the result is inspected
		// — the tool-result cap truncates long proposal envelopes and
		// `status` is the first field — and report lanes return the
		// report text itself, with no envelope. Failure-shaped envelopes
		// (timed out, stuck, cancelled, error) do not reset the streak.
		head := r.Result
		if len(head) > delegateStatusWindow {
			head = head[:delegateStatusWindow]
		}
		if delegateFailure.MatchString(head) {
			return false
		}
		if m := delegateStatus.FindStringSubmatch(head); m != nil {
			switch m[1] {
			case "proposal", "report":
				return true
			}
			return false
		}
		// A background dispatch returns only a job id; it is a turn spent
		// talking, not work this loop performed.
		if strings.Contains(head, `"job_id"`) {
			return false
		}
		// Report and shared lanes return the report text itself.
		return strings.TrimSpace(head) != ""
	case "background_process":
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(r.Args), &p) != nil {
			return false
		}
		switch p.Action {
		case "start", "stop", "restart":
			return true
		}
	case "skill":
		var p struct {
			Action string `json:"action"`
		}
		if json.Unmarshal([]byte(r.Args), &p) != nil {
			return false
		}
		switch p.Action {
		case "create", "edit", "delete":
			return true
		}
	}
	return false
}

// IntentLoopMiddleware detects cross-turn plan repetition.
type IntentLoopMiddleware struct {
	steerAfter int
	haltAfter  int

	anchor  map[string]struct{}
	streak  int
	steered bool
}

// NewIntentLoopMiddleware builds the detector. A non-positive steerAfter
// disables steering; a non-positive haltAfter disables halting. A halt at or
// below the steer threshold would end the run before the model ever saw the
// steer, so it is clamped one past the steer.
func NewIntentLoopMiddleware(steerAfter, haltAfter int) *IntentLoopMiddleware {
	if steerAfter > 0 && haltAfter > 0 && haltAfter <= steerAfter {
		haltAfter = steerAfter + 1
	}
	return &IntentLoopMiddleware{steerAfter: steerAfter, haltAfter: haltAfter}
}

func (m *IntentLoopMiddleware) Name() string { return "intent_loop" }

func (m *IntentLoopMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if m.haltAfter > 0 && m.streak >= m.haltAfter {
		return step, IntentLoopError{Streak: m.streak, Steer: m.steerAfter, Halt: m.haltAfter}
	}
	if m.steerAfter > 0 && m.streak >= m.steerAfter && !m.steered {
		m.steered = true
		step.Messages = append(step.Messages, types.UserMsg(intentSteerMessage(m.streak)))
	}
	return step, nil
}

func (m *IntentLoopMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	if msg == nil {
		return step, nil
	}
	tokens := intentTokens(msg.Content)
	if len(tokens) == 0 {
		// A turn with no text preamble (pure tool dispatch) neither
		// extends nor breaks the streak.
		return step, nil
	}
	switch {
	case m.anchor == nil:
		m.anchor = tokens
		m.streak = 1
		m.steered = false
	case overlap(m.anchor, tokens) >= intentSimilarityThreshold:
		// Compared against the streak ANCHOR, not the previous turn:
		// paraphrase drift ("all four findings are real" → "...are
		// legitimate") converges back on the anchor while adjacent turns
		// can score below threshold against each other.
		m.streak++
	default:
		m.anchor = tokens
		m.streak = 1
		m.steered = false
	}
	return step, nil
}

func (m *IntentLoopMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	for _, r := range results {
		if madeProgress(r) {
			// The plan changed the workspace: any following preamble is
			// a new attempt, not a repetition.
			m.streak = 0
			m.steered = false
			m.anchor = nil
			break
		}
	}
	return step, nil
}

// intentSteerMessage is injected once per streak at the steer threshold.
func intentSteerMessage(streak int) string {
	return fmt.Sprintf("[STEER] You have re-announced the same plan %d turns in a row without changing any files. "+
		"Act now — make the edit, or conclude with what you already know. Another pass of reading the same material will not add information.", streak)
}

// intentTokens normalizes an assistant preamble into a token set: lowercase
// words from the opening intent statement, with backticked command text and
// code fences removed because they vary between repetitions while the plan
// sentence does not.
func intentTokens(content string) map[string]struct{} {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		// Drop inline code spans: `sed -n '1,40p' file` differs each turn.
		for {
			start := strings.IndexByte(line, '`')
			if start < 0 {
				break
			}
			end := strings.IndexByte(line[start+1:], '`')
			if end < 0 {
				line = line[:start]
				break
			}
			line = line[:start] + " " + line[start+1+end+1:]
		}
		b.WriteString(line)
		b.WriteString(" ")
	}
	lower := strings.ToLower(b.String())
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	if len(words) > intentPreambleWords {
		words = words[:intentPreambleWords]
	}
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		out[w] = struct{}{}
	}
	if len(out) < intentMinTokens {
		return nil
	}
	return out
}

// overlap is the token-set overlap coefficient |A∩B| / min(|A|,|B|): the
// forgiving sibling of Jaccard, chosen because the model paraphrases the
// same plan rather than repeating it verbatim.
func overlap(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	smallest := len(a)
	if len(b) < smallest {
		smallest = len(b)
	}
	return float64(inter) / float64(smallest)
}
