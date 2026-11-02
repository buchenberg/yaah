package control

import (
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/tools"
)

// fakeSurface is the transport double: it records the prompts it is handed and
// answers them through the callback.
type fakeSurface struct {
	onSend func(Msg) bool
	gone   chan struct{}
}

// Send implements Surface. The gone channel stands in for the lane's teardown;
// it is only reportable once the prompt was delivered, which is the property
// under test in the bridge.
func (s fakeSurface) Send(msg Msg) (<-chan struct{}, bool) {
	if !s.onSend(msg) {
		return nil, false
	}
	return s.gone, true
}

// TestAskQuestionsAnswered pins the answered path: the entry reaches the
// surface as a control.Question, the answer is rendered "Header: answer", and
// one answer comes back per entry.
func TestAskQuestionsAnswered(t *testing.T) {
	surface := fakeSurface{onSend: func(msg Msg) bool {
		q, ok := msg.(*Question)
		if !ok {
			t.Errorf("surface received %T, want *Question", msg)
			return false
		}
		q.AnswerCh <- "yes"
		return true
	}}
	entries := []tools.QuestionEntry{
		{Header: "Deploy", Question: "Ship it?", Options: []tools.QuestionOption{{Label: "yes"}}},
		{Header: "Notify", Question: "Who?", Options: []tools.QuestionOption{{Label: "team"}}},
	}

	answers := AskQuestions(surface, entries)
	want := []string{"Deploy: yes", "Notify: yes"}
	if len(answers) != len(want) {
		t.Fatalf("answers = %v, want %v", answers, want)
	}
	for i := range want {
		if answers[i] != want[i] {
			t.Errorf("answer %d = %q, want %q", i, answers[i], want[i])
		}
	}
}

// TestAskQuestionsUndeliverable pins the fallback: a prompt the surface cannot
// show is answered with the first option instead of blocking the turn.
func TestAskQuestionsUndeliverable(t *testing.T) {
	surface := fakeSurface{onSend: func(Msg) bool { return false }}
	entries := []tools.QuestionEntry{{
		Header:  "Deploy",
		Options: []tools.QuestionOption{{Label: "first"}, {Label: "second"}},
	}}
	if got := AskQuestions(surface, entries); len(got) != 1 || got[0] != "Deploy: first" {
		t.Fatalf("answers = %v, want the first option", got)
	}
}

// TestAskQuestionsSurfaceGoneIsBounded pins that a surface which goes away
// ends the wait promptly: the answer cannot arrive, so the fallback does.
func TestAskQuestionsSurfaceGoneIsBounded(t *testing.T) {
	done := make(chan struct{})
	surface := fakeSurface{gone: done, onSend: func(Msg) bool { return true }}
	close(done)

	begin := time.Now()
	got := AskQuestions(surface, []tools.QuestionEntry{{Header: "Solo"}})
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("AskQuestions waited %v on a surface that was gone", elapsed)
	}
	if len(got) != 1 || got[0] != "Solo: " {
		t.Fatalf("answers = %v, want the empty fallback", got)
	}
}

// TestApproveCarriesCallID pins the approval contract every surface relies on:
// the provider tool_call id travels with the prompt and the decision is
// returned unchanged.
func TestApproveCarriesCallID(t *testing.T) {
	var seen *Approval
	surface := fakeSurface{onSend: func(msg Msg) bool {
		approval, ok := msg.(*Approval)
		if !ok {
			t.Errorf("surface received %T, want *Approval", msg)
			return false
		}
		seen = approval
		approval.ApproveCh <- true
		return true
	}}

	if !Approve(surface, "call-42", "bash", `{"command":"ls"}`) {
		t.Fatal("Approve = false, want the surface's approval")
	}
	if seen == nil {
		t.Fatal("surface received no approval")
	}
	if seen.CallID != "call-42" {
		t.Errorf("CallID = %q, want call-42", seen.CallID)
	}
	if seen.Name != "bash" || seen.Args != `{"command":"ls"}` {
		t.Errorf("approval = %+v", seen)
	}
}

// TestApproveWithinTimeout pins the bounded decision wait: a surface that takes
// the prompt and never answers cannot park the tool goroutine forever.
func TestApproveWithinTimeout(t *testing.T) {
	surface := fakeSurface{onSend: func(Msg) bool { return true }}

	begin := time.Now()
	if ApproveWithin(surface, 30*time.Millisecond, "call-1", "bash", "{}") {
		t.Fatal("Approve = true with no answer, want a denial")
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("Approve waited %v past its timeout", elapsed)
	}
}

// TestApproveUndeliverableAndGone pins the two other denial paths: a prompt the
// surface cannot show, and a surface that vanishes while the decision is
// pending.
func TestApproveUndeliverableAndGone(t *testing.T) {
	t.Run("cannot deliver", func(t *testing.T) {
		surface := fakeSurface{onSend: func(Msg) bool { return false }}
		begin := time.Now()
		if Approve(surface, "c", "bash", "{}") {
			t.Fatal("Approve = true for a prompt that was never delivered")
		}
		if elapsed := time.Since(begin); elapsed > 2*time.Second {
			t.Fatalf("Approve waited %v on an undeliverable prompt", elapsed)
		}
	})

	t.Run("surface gone", func(t *testing.T) {
		done := make(chan struct{})
		surface := fakeSurface{gone: done, onSend: func(Msg) bool { return true }}
		close(done)
		begin := time.Now()
		if Approve(surface, "c", "bash", "{}") {
			t.Fatal("Approve = true after the surface went away")
		}
		if elapsed := time.Since(begin); elapsed > 2*time.Second {
			t.Fatalf("Approve waited %v after the surface went away", elapsed)
		}
	})
}

// TestTimeoutPolicy pins the shared constants: one delivery bound and one
// decision bound for every surface, both bounded.
func TestTimeoutPolicy(t *testing.T) {
	if SendTimeout > time.Minute {
		t.Errorf("SendTimeout = %v — prompt delivery must stay bounded", SendTimeout)
	}
	if AnswerTimeout != 5*time.Minute {
		t.Errorf("AnswerTimeout = %v, want the 5m decision bound every surface uses", AnswerTimeout)
	}
	if AnswerTimeout <= SendTimeout {
		t.Errorf("AnswerTimeout (%v) must exceed SendTimeout (%v)", AnswerTimeout, SendTimeout)
	}
}

// TestAnswerFormatting pins the answer wire format ("Header: answer") and the
// fallback choice.
func TestAnswerFormatting(t *testing.T) {
	if got := FormatAnswer("H", "a"); !strings.HasPrefix(got, "H: ") {
		t.Errorf("FormatAnswer = %q, want \"H: a\"", got)
	}
	withOption := tools.QuestionEntry{Header: "Deploy", Options: []tools.QuestionOption{{Label: "first"}}}
	if got := FirstOptionAnswer(withOption); got != "Deploy: first" {
		t.Errorf("FirstOptionAnswer = %q, want \"Deploy: first\"", got)
	}
	empty := tools.QuestionEntry{Header: "Solo"}
	if got := FirstOptionAnswer(empty); got != "Solo: " {
		t.Errorf("FirstOptionAnswer = %q, want \"Solo: \"", got)
	}
}

// TestAskEachFallbackKeepsOneAnswerPerEntry pins the shared loop's contract: a
// surface that cannot present an entry still produces an answer for it, so a
// short list can only come from input that ended.
func TestAskEachFallbackKeepsOneAnswerPerEntry(t *testing.T) {
	entries := []tools.QuestionEntry{
		{Header: "one", Options: []tools.QuestionOption{{Label: "a"}}},
		{Header: "two"},
	}
	answers := AskEach(entries,
		func(e tools.QuestionEntry) (string, bool) {
			if e.Header == "one" {
				return "picked", true
			}
			return "", false
		},
		FirstOptionAnswer,
	)
	want := []string{"one: picked", "two: "}
	if len(answers) != len(want) {
		t.Fatalf("answers = %v, want %v", answers, want)
	}
	for i := range want {
		if answers[i] != want[i] {
			t.Errorf("answer %d = %q, want %q", i, answers[i], want[i])
		}
	}
}
