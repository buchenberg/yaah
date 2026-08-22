package tui2

import (
	"testing"
	"time"

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
