package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/buchenberg/yaah/internal/types"
)

const (
	defaultCategoryStrikeCap = 3   // consecutive turns of same-tool dominance
	defaultCategoryMinCalls  = 5   // a tool is "dominant" if called ≥ this many times
	defaultSteerThreshold    = 0.8 // fraction of MaxTurns at which steering begins
)

// LoopDetectionMiddleware detects when the agent is stuck in a loop and either
// halts (exact tool+result repeats) or injects a convergence hint (category
// repetition across turns, or approaching the turn limit).
type LoopDetectionMiddleware struct {
	// --- exact-hash loop detection (hard halt) ---
	history []string
	count   int
	window  int

	// --- category repetition (convergence nudge) ---
	categoryStrikes   map[string]int // tool name → consecutive dominant-turn count
	categoryStrikeCap int
	categoryMinCalls  int

	// --- iteration steering (near-limit nudge) ---
	steerThreshold    float64
	lastSteerBoundary int
}

func (m *LoopDetectionMiddleware) Name() string { return "loop_detection" }

// PrepareStep injects a convergence hint when the agent is approaching
// MaxIterations (the hard loop exit), following the autoresearch pattern of
// "keep going but converge with purpose."
func (m *LoopDetectionMiddleware) PrepareStep(ctx context.Context, step *Step) (*Step, error) {
	if step.MaxIterations <= 0 || step.Iteration <= 0 {
		return step, nil
	}
	threshold := m.steerThreshold
	if threshold <= 0 {
		threshold = defaultSteerThreshold
	}
	boundary := int(float64(step.MaxIterations) * threshold)
	if step.Iteration >= boundary && boundary > m.lastSteerBoundary {
		m.lastSteerBoundary = boundary
		remaining := step.MaxIterations - step.Iteration
		step.Messages = append(step.Messages, types.UserMsg(fmt.Sprintf(
			"[STEER] You are at iteration %d of %d — %d remain before the run is forced to exit. "+
				"Converge on your findings and produce a final answer within the remaining iterations. "+
				"Do not start new investigations.",
			step.Iteration, step.MaxIterations, remaining)))
	}
	return step, nil
}

func (m *LoopDetectionMiddleware) PostModel(ctx context.Context, msg *types.Message, step *Step) (*Step, error) {
	return step, nil
}

func (m *LoopDetectionMiddleware) PostTool(ctx context.Context, results []ToolResult, step *Step) (*Step, error) {
	// Apply defaults for fields the caller didn't configure.
	if m.categoryStrikes == nil {
		m.categoryStrikes = make(map[string]int)
	}
	if m.categoryStrikeCap <= 0 {
		m.categoryStrikeCap = defaultCategoryStrikeCap
	}
	if m.categoryMinCalls <= 0 {
		m.categoryMinCalls = defaultCategoryMinCalls
	}

	// --- exact-hash loop detection (hard halt) ---
	var haltedTool string
	if m.count > 0 && m.window > 0 {
		for _, r := range results {
			hashKey := toolCallHash(r.Name, r.Args, r.Result)
			m.history = append(m.history, hashKey)
			if len(m.history) > m.window {
				m.history = m.history[len(m.history)-m.window:]
			}
			c := 0
			for _, h := range m.history {
				if h == hashKey {
					c++
				}
			}
			if c >= m.count {
				haltedTool = r.Name
				break
			} // end for
		}
	}
	if haltedTool != "" {
		return step, fmt.Errorf("loop detected: tool %q produced the same result %d times in the last %d steps — halting to prevent stuck agent",
			haltedTool, m.count, len(m.history))
	}

	// --- category repetition (convergence nudge) ---
	// Count tool calls by name for this turn.
	turnCounts := make(map[string]int)
	for _, r := range results {
		turnCounts[r.Name]++
	}
	// Find the single dominant tool (highest count, no tie).
	dominant := ""
	maxCalls := 0
	tied := false
	for name, n := range turnCounts {
		if n > maxCalls {
			maxCalls = n
			dominant = name
			tied = false
		} else if n == maxCalls && n > 0 {
			tied = true
		}
	}
	if maxCalls >= m.categoryMinCalls && !tied {
		// Same tool dominating consecutive turns (autoresearch convergence pattern).
		m.categoryStrikes[dominant]++
		for k := range m.categoryStrikes {
			if k != dominant {
				delete(m.categoryStrikes, k)
			}
		}
		if m.categoryStrikes[dominant] >= m.categoryStrikeCap {
			step.Messages = append(step.Messages, types.UserMsg(fmt.Sprintf(
				"[STEER] You have called the %q tool heavily for %d consecutive turns. "+
					"Apply the convergence rule: if you have enough information to decide, "+
					"act on your findings now and stop searching. If you still need more, "+
					"make EXACTLY ONE final targeted pass with a different approach. "+
					"Do not repeat the same search pattern.",
				dominant, m.categoryStrikeCap)))
			clear(m.categoryStrikes)
		}
	} else {
		clear(m.categoryStrikes)
	}

	return step, nil
}

// toolCallHash returns a SHA-256 hash of tool name, arguments, and result for loop detection.
func toolCallHash(name, args, content string) string {
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(args))
	h.Write([]byte{0})
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}
