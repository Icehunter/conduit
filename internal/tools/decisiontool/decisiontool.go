// Package decisiontool implements the RecordDecision tool.
//
// The agent calls this when it makes a load-bearing technical decision that
// future sessions should be aware of: choosing a pattern, ruling out an
// approach, internalising a project constraint, or noting a discovery.
//
// Conduit-original; no Claude Code counterpart.
package decisiontool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/icehunter/conduit/internal/decisionlog"
	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements the RecordDecision tool.
type Tool struct{}

// New returns a fresh RecordDecision tool.
func New() *Tool { return &Tool{} }

func (*Tool) Name() string { return "RecordDecision" }

func (*Tool) Description() string {
	return "Record a load-bearing technical decision for this project. " +
		"Future sessions will read these entries so the agent does not repeat expensive discoveries. " +
		"Call this when you choose a pattern, rule out an approach, learn a project constraint, or note a surprising discovery. " +
		"Do not record trivial steps, narrate diffs, or duplicate what is already in CLAUDE.md."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"kind": {
				"type": "string",
				"enum": ["chose", "ruled_out", "constraint", "discovery"],
				"description": "Category: chose (picked an approach), ruled_out (rejected an alternative), constraint (external requirement), discovery (surprising fact)"
			},
			"scope": {
				"type": "string",
				"description": "Short label for the area — file, symbol, pattern, or subsystem (e.g. 'auth middleware', 'session-tokens', 'API rate-limits')"
			},
			"summary": {
				"type": "string",
				"description": "One-sentence decision in plain English (max 240 chars)"
			},
			"why": {
				"type": "string",
				"description": "Optional: the reason — cite the PR, ticket, conversation, or constraint that drove this (max 600 chars)"
			},
			"files": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Optional: paths most relevant to this decision (max 10)"
			}
		},
		"required": ["kind", "scope", "summary"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	Kind    string   `json:"kind"`
	Scope   string   `json:"scope"`
	Summary string   `json:"summary"`
	Why     string   `json:"why"`
	Files   []string `json:"files"`
}

// Execute appends the decision to today's JSONL log.
func (*Tool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if in.Summary == "" {
		return tool.ErrorResult("summary is required"), nil
	}

	cwd, _ := os.Getwd()
	e := decisionlog.Entry{
		Kind:    decisionlog.Kind(in.Kind),
		Scope:   in.Scope,
		Summary: in.Summary,
		Why:     in.Why,
		Files:   in.Files,
	}
	if err := decisionlog.Append(cwd, e); err != nil {
		return tool.ErrorResult(fmt.Sprintf("failed to record decision: %v", err)), nil
	}
	return tool.TextResult(fmt.Sprintf("Decision recorded: [%s] %s", e.Kind, e.Scope)), nil
}
