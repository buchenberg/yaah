package control

import (
	"fmt"

	"github.com/buchenberg/yaah/internal/tools"
)

// NewQuestion maps one question-tool entry onto the control-plane message a
// surface renders, wiring the channel the answer is returned on.
func NewQuestion(e tools.QuestionEntry, ch chan<- string) *Question {
	opts := make([]Option, len(e.Options))
	for i, o := range e.Options {
		opts[i] = Option{Label: o.Label, Description: o.Description}
	}
	return &Question{
		Header:   e.Header,
		Question: e.Question,
		Options:  opts,
		Multiple: e.Multiple,
		AnswerCh: ch,
	}
}

// FormatAnswer renders one answered entry for the tool result.
func FormatAnswer(header, answer string) string {
	return fmt.Sprintf("%s: %s", header, answer)
}

// FirstOptionAnswer is the fallback for an entry that could not be put to the
// user: the first declared option, or an empty answer when the entry offers
// none. Answering beats blocking the turn on a surface that is gone.
func FirstOptionAnswer(e tools.QuestionEntry) string {
	if len(e.Options) > 0 {
		return FormatAnswer(e.Header, e.Options[0].Label)
	}
	return FormatAnswer(e.Header, "")
}

// AskFunc answers one question-tool entry. answered is false when the surface
// could not put the entry to the user at all, which is when the caller's
// fallback supplies the answer instead.
type AskFunc func(e tools.QuestionEntry) (answer string, answered bool)

// AskEach drives one question-tool call for any surface: every entry goes to
// askEntry, a decision is rendered with FormatAnswer, and an entry that could
// not be presented gets fallback's answer. It always returns one answer per
// entry, so a shorter result can only come from a caller whose input ended.
func AskEach(
	entries []tools.QuestionEntry,
	askEntry AskFunc,
	fallback func(tools.QuestionEntry) string,
) []string {
	answers := make([]string, 0, len(entries))
	for _, e := range entries {
		answer, answered := askEntry(e)
		if !answered {
			answers = append(answers, fallback(e))
			continue
		}
		answers = append(answers, FormatAnswer(e.Header, answer))
	}
	return answers
}

// AskQuestions is the shared question-tool handler for the control-plane
// surfaces (TUI, web): each entry is sent to the surface and the answer is
// waited for under the shared timeout policy. An entry the surface cannot
// present, and one the user does not answer in time, fall back to the first
// option instead of blocking the turn.
func AskQuestions(s Surface, entries []tools.QuestionEntry) []string {
	return AskEach(entries, func(e tools.QuestionEntry) (string, bool) {
		return askSurface(s, e)
	}, FirstOptionAnswer)
}

// askSurface offers one entry to the surface and waits for its answer.
func askSurface(s Surface, e tools.QuestionEntry) (string, bool) {
	ch := make(chan string, 1)
	if !s.Send(NewQuestion(e, ch)) {
		return "", false
	}
	return awaitAnswer(ch, s.Done(), AnswerTimeout)
}
