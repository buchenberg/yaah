package tools

import (
	"errors"
	"testing"
)

func TestParseSubAgentOutput_NoEscalation(t *testing.T) {
	out := ParseSubAgentOutput("task completed successfully", nil)
	if out.Escalation != nil {
		t.Errorf("expected no escalation, got %+v", out.Escalation)
	}
	if out.RawOutput != "task completed successfully" {
		t.Errorf("raw output mismatch: %q", out.RawOutput)
	}
	if out.Error != "" {
		t.Errorf("expected no error, got %q", out.Error)
	}
}

func TestParseSubAgentOutput_WithEscalation(t *testing.T) {
	output := `I tried to read the file but it doesn't exist.

` + "```escalation\n" +
		`{"severity":"blocker","summary":"file not found","detail":"src/main.go does not exist","suggestion":"check the path"}` + "\n```"

	out := ParseSubAgentOutput(output, nil)
	if out.Escalation == nil {
		t.Fatal("expected escalation, got nil")
	}
	if out.Escalation.Severity != EscalationBlocker {
		t.Errorf("severity = %q, want blocker", out.Escalation.Severity)
	}
	if out.Escalation.Summary != "file not found" {
		t.Errorf("summary = %q", out.Escalation.Summary)
	}
	if out.Escalation.Detail != "src/main.go does not exist" {
		t.Errorf("detail = %q", out.Escalation.Detail)
	}
	if out.Escalation.Suggestion != "check the path" {
		t.Errorf("suggestion = %q", out.Escalation.Suggestion)
	}
}

func TestParseSubAgentOutput_MalformedEscalation(t *testing.T) {
	output := "result\n\n```escalation\nnot json\n```"
	out := ParseSubAgentOutput(output, nil)
	if out.Escalation != nil {
		t.Errorf("malformed escalation should be ignored, got %+v", out.Escalation)
	}
}

func TestParseSubAgentOutput_WithError(t *testing.T) {
	out := ParseSubAgentOutput("", errors.New("timeout"))
	if out.Error != "timeout" {
		t.Errorf("error = %q, want timeout", out.Error)
	}
}
