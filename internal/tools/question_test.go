package tools

import (
	"context"
	"strings"
	"testing"
)

func questionArgs() string {
	return `{"questions":[
		{"question":"Which database?","header":"DB","options":[{"label":"postgres","description":"relational"}]},
		{"question":"Which region?","header":"Region","options":[{"label":"eu","description":"europe"}]}
	]}`
}

func TestQuestionTool_EOFFails(t *testing.T) {
	qt := &QuestionTool{Handler: func([]QuestionEntry) []string { return nil }}

	_, err := qt.Execute(context.Background(), questionArgs())
	if err == nil {
		t.Fatal("EOF (no answers) must be an error, not an empty success")
	}
	for _, want := range []string{"2 of 2", "DB", "Region", "unanswered"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestQuestionTool_PartialAnswerFails(t *testing.T) {
	qt := &QuestionTool{Handler: func(entries []QuestionEntry) []string {
		return []string{"DB: postgres"}
	}}

	_, err := qt.Execute(context.Background(), questionArgs())
	if err == nil {
		t.Fatal("a partial answer set must be an error")
	}
	if !strings.Contains(err.Error(), "Region") || strings.Contains(err.Error(), "DB") {
		t.Errorf("error should name only the unanswered question, got %v", err)
	}
}

func TestQuestionTool_AllAnsweredSucceeds(t *testing.T) {
	qt := &QuestionTool{Handler: func(entries []QuestionEntry) []string {
		answers := make([]string, len(entries))
		for i, e := range entries {
			answers[i] = e.Header + ": answer"
		}
		return answers
	}}

	result, err := qt.Execute(context.Background(), questionArgs())
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "DB: answer") || !strings.Contains(result, "Region: answer") {
		t.Errorf("result lost an answer: %q", result)
	}
}
