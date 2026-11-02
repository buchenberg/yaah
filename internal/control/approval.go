package control

import "time"

// Approve asks the surface to allow one tool call and waits AnswerTimeout for
// the decision, which is the same bound the surfaces give a question.
//
// Every failure — a prompt that cannot be delivered, a surface that goes away,
// a decision that never arrives — denies. Denying is the safe direction for a
// call that may mutate the workspace, and it keeps the turn moving instead of
// parking it on a surface nobody is answering.
//
// callID is the provider's tool_call id when the loop has one (empty for asks
// that carry no call, such as an out-of-workspace path decision); it travels
// with the prompt so a surface can correlate the decision with the tool row
// it belongs to.
func Approve(s Surface, callID, name, args string) bool {
	return ApproveWithin(s, AnswerTimeout, callID, name, args)
}

// ApproveWithin is Approve with an explicit decision timeout. Production
// surfaces use Approve and therefore AnswerTimeout; the parameter exists so a
// test can exercise the timeout branch without waiting minutes for it.
func ApproveWithin(s Surface, timeout time.Duration, callID, name, args string) bool {
	decision := make(chan bool, 1)
	if !s.Send(&Approval{CallID: callID, Name: name, Args: args, ApproveCh: decision}) {
		return false
	}
	approved, answered := awaitAnswer(decision, s.Done(), timeout)
	return answered && approved
}
