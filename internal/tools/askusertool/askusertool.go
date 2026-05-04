// Package askusertool implements the AskUserQuestion tool.
//
// The model calls this tool to ask the user a multiple-choice or free-form
// question. The TUI blocks the agent loop until the user responds.
// Port of src/tools/AskUserQuestionTool/.
package askusertool

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

const toolName = "AskUserQuestion"

// Option is one choice presented to the user.
type Option struct {
	Label       string `json:"label"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
}

// input is the JSON input for AskUserQuestion.
type input struct {
	Question    string   `json:"question"`
	Options     []Option `json:"options,omitempty"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}

// AskUserQuestion presents a question to the user and returns their answer.
// The Ask callback is installed by the TUI; without it the tool auto-declines.
type AskUserQuestion struct {
	// Ask is called with the question and options. It blocks until the user
	// responds and returns the selected answer(s). If the user provides a
	// free-form answer, it is returned as a single element. Returns nil to
	// indicate no answer / cancellation.
	Ask func(ctx context.Context, question string, options []Option, multiSelect bool) []string
}

func (t *AskUserQuestion) Name() string        { return toolName }
func (t *AskUserQuestion) Description() string { return description }
func (t *AskUserQuestion) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"question": {
			"type": "string",
			"description": "The question to ask the user."
		},
		"options": {
			"type": "array",
			"description": "Possible answers. Users can also type a custom answer.",
			"items": {
				"type": "object",
				"properties": {
					"label":       {"type": "string"},
					"value":       {"type": "string"},
					"description": {"type": "string"}
				},
				"required": ["label"]
			}
		},
		"multiSelect": {
			"type": "boolean",
			"description": "If true, allow selecting multiple options."
		}
	},
	"required": ["question"],
	"additionalProperties": false
}`)
}
func (t *AskUserQuestion) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *AskUserQuestion) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *AskUserQuestion) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var inp input
	if err := json.Unmarshal(raw, &inp); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}
	if inp.Question == "" {
		return tool.ErrorResult("question is required"), nil
	}

	if t.Ask == nil {
		return tool.ErrorResult("interactive questions are not available in this context"), nil
	}

	answers := t.Ask(ctx, inp.Question, inp.Options, inp.MultiSelect)
	if len(answers) == 0 {
		return tool.ErrorResult("No answer provided. The user did not respond."), nil
	}

	return tool.TextResult(strings.Join(answers, "\n")), nil
}

const description = `Asks the user a question with optional multiple-choice answers to gather information, clarify ambiguity, understand preferences, or make decisions. Users can always provide a custom text answer. Use multiSelect:true to allow multiple selections.`
