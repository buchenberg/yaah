package tui

import (
	"testing"
	"time"

	"strings"

	"github.com/buchenberg/yaah/internal/control"
)

func TestHandleControlMsg_Approval(t *testing.T) {
	ui := New("test")

	var approvedName, approvedArgs string
	ui.ShowApprovalFn = func(name, args string, onAnswer func(bool)) {
		approvedName = name
		approvedArgs = args
		onAnswer(true)
	}

	ch := make(chan bool, 1)
	ui.handleControlMsg(&control.Approval{
		Name:      "bash",
		Args:      "git diff",
		ApproveCh: ch,
	})

	if approvedName != "bash" {
		t.Errorf("expected approval name 'bash', got %q", approvedName)
	}
	if approvedArgs != "git diff" {
		t.Errorf("expected approval args 'git diff', got %q", approvedArgs)
	}

	select {
	case approved := <-ch:
		if !approved {
			t.Error("expected approval to be true")
		}
	case <-time.After(time.Second):
		t.Fatal("approval channel never received answer")
	}
}

func TestHandleControlMsg_ApprovalDenied(t *testing.T) {
	ui := New("test")

	ui.ShowApprovalFn = func(name, args string, onAnswer func(bool)) {
		onAnswer(false)
	}

	ch := make(chan bool, 1)
	ui.handleControlMsg(&control.Approval{
		Name:      "bash",
		Args:      "rm -rf /",
		ApproveCh: ch,
	})

	select {
	case approved := <-ch:
		if approved {
			t.Error("expected approval to be false")
		}
	case <-time.After(time.Second):
		t.Fatal("approval channel never received answer")
	}
}

func TestHandleControlMsg_Continue(t *testing.T) {
	ui := New("test")

	var continueCalled bool
	ui.ShowApprovalFn = func(name, args string, onAnswer func(bool)) {
		continueCalled = true
		onAnswer(false)
	}

	ch := make(chan bool, 1)
	ui.handleControlMsg(&control.Continue{
		MaxIter:  10,
		AnswerCh: ch,
	})

	if !continueCalled {
		t.Error("expected ShowApproval to be called for CtrlContinue")
	}

	select {
	case cont := <-ch:
		if cont {
			t.Error("expected continue to be false")
		}
	case <-time.After(time.Second):
		t.Fatal("continue channel never received answer")
	}
}

// TestHandleControlMsg_CompactionSummary pins yaah-v378.5: the produced
// summary is rendered as a DURABLE conversation entry (not ephemeral
// status), with the trim fallback visible as "no summary produced".
func TestHandleControlMsg_CompactionSummary(t *testing.T) {
	ui := New("test")
	before := len(ui.conversationLog)

	ui.handleControlMsg(&control.CompactionSummary{
		Summary: "## Goal\n- address review comments\n\n## Done\n- landed the replay knob",
		Method:  "single",
		Before:  90000,
		After:   6000,
		Window:  200000,
	})

	if len(ui.conversationLog) <= before {
		t.Fatal("compaction summary was not added to the conversation log")
	}
	joined := ""
	for _, item := range ui.conversationLog[before:] {
		joined += item.text + "\n"
	}
	if !strings.Contains(joined, "context compacted (single): 90000 → 6000 tokens") {
		t.Errorf("header missing from rendered entry: %q", joined)
	}
	if !strings.Contains(joined, "address review comments") {
		t.Errorf("summary text missing from rendered entry: %q", joined)
	}

	// Trim fallback: an empty summary renders the honest "no summary
	// produced" line instead of nothing.
	ui.handleControlMsg(&control.CompactionSummary{Method: "trim", Before: 6000, After: 5000, Window: 200000})
	last := ui.conversationLog[len(ui.conversationLog)-1]
	if !strings.Contains(last.text, "no summary produced") {
		t.Errorf("trim fallback rendered %q, want the no-summary notice", last.text)
	}
}
