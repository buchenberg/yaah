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
// sub-agent jobs dispatched via spawn_subagent with background:true.
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
				"description": "Job identifier (from spawn_subagent background dispatch result). Required for status, cancel, and wait."
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

func (t *SubAgentJobsTool) executeCancel(jobID string) (string, error) {
	if !t.Jobs.Cancel(jobID) {
		return "", fmt.Errorf("subagent_jobs: unknown job_id %q", jobID)
	}
	// Read back the status after cancellation for a clean response.
	st, _ := t.Jobs.Status(jobID)
	if st.Status == jobs.BGStatusRunning {
		// The job hasn't observed cancellation yet; report it as
		// cancelled with the running status.
		st.Status = jobs.BGStatusCancelled
	}
	data, _ := json.Marshal(st)
	return string(data), nil
}

func (t *SubAgentJobsTool) executeWait(_ context.Context, jobID string, timeoutSecs int) (string, error) {
	if timeoutSecs <= 0 {
		timeoutSecs = 60
	}
	// Wait uses the manager's Wait which blocks on the job's done channel.
	// We derive a fresh context with a timeout so the call is bounded.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSecs)*time.Second)
	defer cancel()

	st, ok := t.Jobs.Wait(ctx, jobID)
	if !ok {
		return "", fmt.Errorf("subagent_jobs: unknown job_id %q", jobID)
	}
	if st.Status == jobs.BGStatusRunning {
		st.Status = "timed_out"
	}
	data, _ := json.Marshal(st)
	return string(data), nil
}
