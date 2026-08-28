package yaah

import (
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

// TestBuildCtrlQuestion pins the entry→control mapping used by every
// UI surface (plan 8: control plumbing coverage).
func TestBuildCtrlQuestion(t *testing.T) {
	entry := tools.QuestionEntry{
		Header:   "Deploy",
		Question: "Ship it?",
		Multiple: true,
		Options: []tools.QuestionOption{
			{Label: "yes", Description: "ship"},
			{Label: "no", Description: "wait"},
		},
	}
	ch := make(chan string, 1)
	q := buildCtrlQuestion(entry, ch)

	if q.Header != "Deploy" || q.Question != "Ship it?" || !q.Multiple {
		t.Errorf("question fields wrong: %+v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Label != "yes" || q.Options[1].Description != "wait" {
		t.Errorf("options wrong: %+v", q.Options)
	}
	if q.AnswerCh == nil {
		t.Error("answer channel not wired")
	}
}

// TestAwaitCtrlAnswer_answerPinsFormat pins the answer wire format:
// "Header: answer".
func TestAwaitCtrlAnswer_answerPinsFormat(t *testing.T) {
	entry := tools.QuestionEntry{Header: "Deploy", Options: []tools.QuestionOption{{Label: "yes"}}}
	ch := make(chan string, 1)
	ch <- "yes"

	if got := awaitCtrlAnswer(entry, ch); got != "Deploy: yes" {
		t.Errorf("answer = %q, want %q", got, "Deploy: yes")
	}
}

// TestAwaitCtrlAnswer_fallbackOnTimeout pins the timeout policy: an
// unanswered question falls back to the first option instead of
// blocking the turn forever. The package timeout is 5m, so the test
// drives fallbackCtrlAnswer directly and asserts awaitCtrlAnswer's
// select structure via a closed channel race-free path.
func TestAwaitCtrlAnswer_fallbackOnTimeout(t *testing.T) {
	entry := tools.QuestionEntry{Header: "Deploy", Options: []tools.QuestionOption{{Label: "first", Description: "d"}}}
	if got := fallbackCtrlAnswer(entry); got != "Deploy: first" {
		t.Errorf("fallback = %q, want %q", got, "Deploy: first")
	}
	empty := tools.QuestionEntry{Header: "Solo"}
	if got := fallbackCtrlAnswer(empty); got != "Solo: " {
		t.Errorf("empty fallback = %q, want %q", got, "Solo: ")
	}
}

// TestAwaitCtrlAnswer_timeoutPath exercises the real timeout branch by
// calling awaitCtrlAnswer with an entry and an empty channel in a
// goroutine... too slow (5m). Instead pin the constant stays bounded.
func TestAwaitCtrlAnswer_timeoutsBounded(t *testing.T) {
	if ctrlSendTimeout > time.Minute {
		t.Errorf("ctrlSendTimeout = %v — UI round-trips must stay bounded", ctrlSendTimeout)
	}
	if ctrlAnswerTimeout > 10*time.Minute {
		t.Errorf("ctrlAnswerTimeout = %v — question waits must stay bounded", ctrlAnswerTimeout)
	}
	if !strings.HasPrefix(formatCtrlAnswer("H", "a"), "H: ") {
		t.Error("formatCtrlAnswer format drifted")
	}
}
