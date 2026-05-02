// Package todowritetool implements the TodoWrite tool — manages the session
// task checklist the model uses to track multi-step work.
//
// Mirrors src/tools/TodoWriteTool/TodoWriteTool.ts. The todo list is stored
// in-memory per session; it resets when the process exits. Persistence lands
// in M8 alongside session storage.
//
// The model sends the *complete* new todo list each call (not a delta).
// We store it and return a success message that nudges the model to continue.
package todowritetool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/icehunter/claude-go/internal/tool"
)

// Status values mirror the TS TodoListSchema.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
)

// Todo is one item in the task list.
type Todo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
	// Priority is optional: high | medium | low
	Priority string `json:"priority,omitempty"`
}

// store holds the in-memory todo list, shared across tool calls in a session.
var store struct {
	mu    sync.Mutex
	todos []Todo
}

// Tool implements the TodoWrite tool.
type Tool struct{}

// New returns a fresh TodoWrite tool.
func New() *Tool { return &Tool{} }

func (*Tool) Name() string { return "TodoWrite" }

func (*Tool) Description() string {
	return "Create and manage a structured task list for the current session. " +
		"Use this tool to track multi-step tasks, mark progress, and ensure nothing is forgotten. " +
		"Send the complete updated todo list each call (not just changes)."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"todos": {
				"type": "array",
				"description": "The complete updated todo list",
				"items": {
					"type": "object",
					"properties": {
						"id":       {"type": "string", "description": "Unique identifier for the todo item"},
						"content":  {"type": "string", "description": "The task description"},
						"status":   {"type": "string", "enum": ["pending", "in_progress", "completed"]},
						"priority": {"type": "string", "enum": ["high", "medium", "low"]}
					},
					"required": ["id", "content", "status"]
				}
			}
		},
		"required": ["todos"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool      { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	Todos []Todo `json:"todos"`
}

// Execute replaces the session todo list and returns a summary.
func (t *Tool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}

	store.mu.Lock()
	old := store.todos
	// If all items are completed, clear the list (matches TS behaviour).
	allDone := len(in.Todos) > 0
	for _, td := range in.Todos {
		if td.Status != StatusCompleted {
			allDone = false
			break
		}
	}
	if allDone {
		store.todos = nil
	} else {
		store.todos = in.Todos
	}
	store.mu.Unlock()

	_ = old // available for future diffing / UI display

	return tool.TextResult(
		"Todos have been modified successfully. " +
			"Ensure that you continue to use the todo list to track your progress. " +
			"Please proceed with the current tasks if applicable.",
	), nil
}

// GetTodos returns the current in-memory todo list.
func GetTodos() []Todo {
	store.mu.Lock()
	defer store.mu.Unlock()
	out := make([]Todo, len(store.todos))
	copy(out, store.todos)
	return out
}

// FormatTodos renders the todo list as a compact human-readable string
// suitable for display in the TUI status or a /todos slash command.
func FormatTodos(todos []Todo) string {
	if len(todos) == 0 {
		return "(no active todos)"
	}
	var sb strings.Builder
	for _, td := range todos {
		icon := "○"
		switch td.Status {
		case StatusInProgress:
			icon = "◉"
		case StatusCompleted:
			icon = "✓"
		}
		sb.WriteString(fmt.Sprintf("%s %s\n", icon, td.Content))
	}
	return strings.TrimRight(sb.String(), "\n")
}
