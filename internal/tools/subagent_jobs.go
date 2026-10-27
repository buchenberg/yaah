package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/jobs"
	"github.com/buchenberg/yaah/internal/prompts"
)

// SubAgentJobsTool provides list/status/cancel/wait for background
// sub-agent jobs dispatched via delegate with background:true.
// It is the lifecycle companion to the dispatch, analogous to the
// background_process tool for shell processes.
type SubAgentJobsTool struct {
	Jobs *jobs.BackgroundJobs
}

func (*SubAgentJobsTool) Name() string        { return "subagent_jobs" }
func (*SubAgentJobsTool) Description() string { return prompts.ToolDescription("subagent_jobs") }

func (*SubAgentJobsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["list", "status", "cancel", "wait"],
				"description": "list all jobs; status — get one job's details by job_id; cancel — abort a running job; wait — block until a job finishes or timeout_seconds elapses"
			},
			"job_id": {
				"type": "string",
				"description": "Job identifier (from delegate background dispatch result). Required for status, cancel, and wait."
			},
			"timeout_seconds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 300,
				"description": "Max seconds to wait (wait action only; default 60)"
			}
		},
		"required": ["action"]
	}`)
}

func (t *SubAgentJobsTool) Execute(ctx context.Context, args string) (string, error) {
	if t.Jobs == nil {
		return "", fmt.Errorf("subagent_jobs: no background job manager configured")
	}

	var raw struct {
		Action         string `json:"action"`
		JobID          string `json:"job_id"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", fmt.Errorf("subagent_jobs: invalid arguments: %w", err)
	}

	switch raw.Action {
	case "list":
		return t.executeList()
	case "status":
		if raw.JobID == "" {
			return "", fmt.Errorf("subagent_jobs: job_id is required for status")
		}
		return t.executeStatus(raw.JobID)
	case "cancel":
		if raw.JobID == "" {
			return "", fmt.Errorf("subagent_jobs: job_id is required for cancel")
		}
		return t.executeCancel(raw.JobID)
	case "wait":
		if raw.JobID == "" {
			return "", fmt.Errorf("subagent_jobs: job_id is required for wait")
		}
		return t.executeWait(ctx, raw.JobID, raw.TimeoutSeconds)
	default:
		return "", fmt.Errorf("subagent_jobs: unknown action %q (valid: list, status, cancel, wait)", raw.Action)
	}
}

func (t *SubAgentJobsTool) executeList() (string, error) {
	list := t.Jobs.List()
	if len(list) == 0 {
		return `{"jobs":[],"pending":0}`, nil
	}
	pending := 0
	for _, s := range list {
		if s.Status == jobs.BGStatusRunning {
			pending++
		}
	}
	out := struct {
		Jobs    []jobs.BackgroundJobStatus `json:"jobs"`
		Pending int                        `json:"pending"`
	}{list, pending}
	data, _ := json.Marshal(out)
	return string(data), nil
}

func (t *SubAgentJobsTool) executeStatus(jobID string) (string, error) {
	st, ok := t.Jobs.Status(jobID)
	if !ok {
		return "", fmt.Errorf("subagent_jobs: unknown job_id %q", jobID)
	}
	data, _ := json.Marshal(st)
	return string(data), nil
}

// Cancel outcomes reported in the "cancel" field of a cancel response. They
// name what the verb did, which the job's own status cannot: "cancelled" for a
// live job the request reached, "already_finished" for a job that had already
// reached a terminal state (nothing was interrupted), and "unknown" for an id
// the manager no longer knows. Nothing is force-labelled (yaah-atw).
const (
	// cancelCancelled: the id named a running job and the cancellation
	// request reached it. It may still be winding down — the snapshot's
	// still_running says whether it has actually stopped.
	cancelCancelled = "cancelled"
	// cancelAlreadyFinished: the job was already terminal, so the request
	// changed nothing. The snapshot carries the real final state.
	cancelAlreadyFinished = "already_finished"
	// cancelUnknown: no such job — never dispatched in this session, or its
	// finished record was reaped.
	cancelUnknown = "unknown"
)

// cancelEnvelope is a cancel result: the verb's own outcome in Cancel, plus
// the job snapshot when the job is known. The snapshot is a pointer so an id
// whose record vanished mid-call is described by Cancel and Notes alone,
// never by a zero-value job that appears to exist.
type cancelEnvelope struct {
	*jobs.BackgroundJobStatus
	Cancel string `json:"cancel"`
	Notes  string `json:"notes,omitempty"`
}

// waitEnvelope is a wait result: the job's real snapshot plus which way the
// wait ended early, if either. The snapshot is embedded so status and
// still_running stay truthful whatever ended the wait.
type waitEnvelope struct {
	jobs.BackgroundJobStatus
	// TimedOut: this call's timeout_seconds elapsed with the job unfinished.
	TimedOut bool `json:"timed_out,omitempty"`
	// Interrupted: the caller's context ended (turn cancel, parent deadline,
	// jail teardown) before the job finished. Kept distinct from TimedOut so
	// a caller abort is never reported as a normal timeout (yaah-qwo).
	Interrupted bool `json:"interrupted,omitempty"`
	// Notes carries the caveat behind an early return.
	Notes string `json:"notes,omitempty"`
}

func (t *SubAgentJobsTool) executeCancel(jobID string) (string, error) {
	// Resolve the job before cancelling: Cancel(id) only proves the id was
	// known, so its return value cannot distinguish "stopped a live job"
	// from "the job was already done" (yaah-atw).
	st, ok := t.Jobs.Status(jobID)
	if !ok {
		// Never existed, or its finished record was reaped. Say exactly
		// that instead of marshalling a zero-value snapshot.
		return marshalCancel(cancelEnvelope{
			Cancel: cancelUnknown,
			Notes:  "no such job: this id was never dispatched in this session, or its finished record was reaped",
		})
	}
	if !st.StillRunning {
		// Already terminal: the request cannot change anything, so report
		// the real final state rather than labelling it "cancelled".
		return marshalCancel(cancelEnvelope{BackgroundJobStatus: &st, Cancel: cancelAlreadyFinished})
	}
	if !t.Jobs.Cancel(jobID) {
		// Raced with the record disappearing; the id is no longer known.
		return marshalCancel(cancelEnvelope{
			Cancel: cancelUnknown,
			Notes:  "no such job: the record disappeared before the cancellation request was delivered",
		})
	}
	// A cancellation request only signals the job; it may still be running
	// until the runner observes it. Re-read and report the true status —
	// StillRunning carries the "not done yet" fact.
	after, ok := t.Jobs.Status(jobID)
	if !ok {
		return marshalCancel(cancelEnvelope{
			Cancel: cancelCancelled,
			Notes:  "cancellation was delivered but the job's record was reaped before its state could be read",
		})
	}
	env := cancelEnvelope{BackgroundJobStatus: &after, Cancel: cancelCancelled}
	if after.StillRunning {
		env.Notes = "cancellation requested; the job had not stopped yet — check status and still_running"
	}
	return marshalCancel(env)
}

func (t *SubAgentJobsTool) executeWait(ctx context.Context, jobID string, timeoutSecs int) (string, error) {
	if timeoutSecs <= 0 {
		timeoutSecs = 60
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Bound the wait with this call's timeout_seconds but derive it from the
	// caller's context, so a cancelled turn, a parent deadline or a teardown
	// interrupts the wait immediately instead of blocking for the full
	// timeout (yaah-qwo).
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	st, ok := t.Jobs.Wait(waitCtx, jobID)
	if !ok {
		return "", fmt.Errorf("subagent_jobs: unknown job_id %q", jobID)
	}
	if !st.StillRunning {
		// The job finished: report its real terminal snapshot. A deadline or
		// cancellation that landed at the same instant must not relabel a
		// finished job as a timeout or an interruption.
		data, _ := json.Marshal(st)
		return string(data), nil
	}
	// The job is unfinished, so the wait returned early: either our own
	// timeout_seconds elapsed or the caller's context ended. Report which,
	// leaving status truthful (still running, not a fabricated terminal one).
	env := waitEnvelope{BackgroundJobStatus: st}
	if err := ctx.Err(); err != nil {
		env.Interrupted = true
		env.Notes = fmt.Sprintf("wait interrupted by the caller's context (%v) before the job finished; the job was not asked to stop and is still running", err)
	} else {
		env.TimedOut = true
	}
	data, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("subagent_jobs: encode wait result: %w", err)
	}
	return string(data), nil
}

func marshalCancel(env cancelEnvelope) (string, error) {
	data, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("subagent_jobs: encode cancel result: %w", err)
	}
	return string(data), nil
}
