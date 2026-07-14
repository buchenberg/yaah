package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// QuestionEntry represents a single question to ask the user.
type QuestionEntry struct {
	Question string
	Header   string
	Options  []QuestionOption
	Multiple bool
}

// QuestionOption is a single choice in a question.
type QuestionOption struct {
	Label       string
	Description string
}

// QuestionHandler receives questions and returns answers. If nil, the
// default stdin/stderr handler is used.
type QuestionHandler func(questions []QuestionEntry) []string

// QuestionTool asks the user structured questions with multiple-choice options.
type QuestionTool struct {
	Handler QuestionHandler
}

func (t *QuestionTool) Name() string { return "question" }
func (t *QuestionTool) Description() string {
	return "Asks the user structured questions with multiple-choice options for interactive clarification."
}

func (t *QuestionTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"questions": {
				"type": "array",
				"description": "Array of questions to ask the user",
				"items": {
					"type": "object",
					"properties": {
						"question": {"type": "string", "description": "The question text"},
						"header": {"type": "string", "description": "Short label for the question (max 30 chars)"},
						"options": {
							"type": "array",
							"items": {
								"type": "object",
								"properties": {
									"label": {"type": "string", "description": "Display text for the option"},
									"description": {"type": "string", "description": "Explanation of the choice"}
								},
								"required": ["label", "description"]
							}
						},
						"multiple": {"type": "boolean", "description": "Allow selecting multiple choices (default false)"}
					},
					"required": ["question", "header", "options"]
				}
			}
		},
		"required": ["questions"]
	}`)
}

func (t *QuestionTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Questions []struct {
			Question string `json:"question"`
			Header   string `json:"header"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
			Multiple bool `json:"multiple"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("question: invalid arguments: %w", err)
	}
	if len(params.Questions) == 0 {
		return "", fmt.Errorf("question: at least one question is required")
	}

	entries := make([]QuestionEntry, len(params.Questions))
	for i, q := range params.Questions {
		opts := make([]QuestionOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = QuestionOption{Label: o.Label, Description: o.Description}
		}
		entries[i] = QuestionEntry{
			Question: q.Question,
			Header:   q.Header,
			Options:  opts,
			Multiple: q.Multiple,
		}
	}

	var answers []string
	if t.Handler != nil {
		answers = t.Handler(entries)
	} else {
		answers = defaultQuestionHandler(entries)
	}

	return strings.Join(answers, "\n"), nil
}

func defaultQuestionHandler(entries []QuestionEntry) []string {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var answers []string

	for i, q := range entries {
		if i > 0 {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "\n═══ %s ═══\n", q.Header)
		fmt.Fprintf(os.Stderr, "%s\n\n", q.Question)

		for j, opt := range q.Options {
			fmt.Fprintf(os.Stderr, "  [%d] %s — %s\n", j+1, opt.Label, opt.Description)
		}

		plural := ""
		if q.Multiple {
			plural = "s"
		}
		fmt.Fprintf(os.Stderr, "\n→ Enter choice%s (number%s", plural, plural)
		if q.Multiple {
			fmt.Fprintf(os.Stderr, ", comma-separated")
		}
		fmt.Fprintf(os.Stderr, "): ")
		os.Stderr.Sync()

		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" && len(q.Options) > 0 {
			input = "1"
		}
		answers = append(answers, fmt.Sprintf("%s: %s", q.Header, input))
	}

	fmt.Fprintln(os.Stderr)
	return answers
}
