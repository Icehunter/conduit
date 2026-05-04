package tasktool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

// ── TaskCreate ────────────────────────────────────────────────────────────────

type TaskCreateTool struct{ store *Store }

func NewCreate() *TaskCreateTool     { return &TaskCreateTool{store: globalStore} }
func (*TaskCreateTool) Name() string { return "TaskCreate" }
func (*TaskCreateTool) Description() string {
	return `Create a new task in the task list to track progress on complex multi-step work.

Use proactively when:
- A task requires 3 or more distinct steps
- The user provides a list of things to be done
- After receiving new instructions — capture requirements as tasks immediately
- When starting work on a task — mark it in_progress BEFORE beginning

Do NOT use for single trivial tasks or purely conversational exchanges.`
}
func (*TaskCreateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"subject":     {"type": "string", "description": "Brief title for the task"},
			"description": {"type": "string", "description": "What needs to be done"},
			"activeForm":  {"type": "string", "description": "Present-continuous form shown while in_progress, e.g. \"Running tests\""},
			"metadata":    {"type": "object", "description": "Arbitrary metadata"}
		},
		"required": ["subject", "description"]
	}`)
}
func (*TaskCreateTool) IsReadOnly(json.RawMessage) bool        { return false }
func (*TaskCreateTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *TaskCreateTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		Subject     string         `json:"subject"`
		Description string         `json:"description"`
		ActiveForm  string         `json:"activeForm,omitempty"`
		Metadata    map[string]any `json:"metadata,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}
	task := t.store.Create(in.Subject, in.Description, in.ActiveForm, in.Metadata)
	out, _ := json.Marshal(map[string]any{"task": map[string]string{"id": task.ID, "subject": task.Subject}})
	return tool.TextResult(string(out)), nil
}

// ── TaskGet ───────────────────────────────────────────────────────────────────

type TaskGetTool struct{ store *Store }

func NewGet() *TaskGetTool        { return &TaskGetTool{store: globalStore} }
func (*TaskGetTool) Name() string { return "TaskGet" }
func (*TaskGetTool) Description() string {
	return "Retrieve a task by ID to check its status, description, and output."
}
func (*TaskGetTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"taskId":{"type":"string"}},"required":["taskId"]}`)
}
func (*TaskGetTool) IsReadOnly(json.RawMessage) bool        { return true }
func (*TaskGetTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *TaskGetTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input"), nil
	}
	task, ok := t.store.Get(in.TaskID)
	if !ok {
		out, _ := json.Marshal(map[string]any{"task": nil})
		return tool.TextResult(string(out)), nil
	}
	out, _ := json.Marshal(map[string]any{"task": map[string]any{
		"id":          task.ID,
		"subject":     task.Subject,
		"description": task.Description,
		"status":      task.Status,
		"output":      task.Output,
	}})
	return tool.TextResult(string(out)), nil
}

// ── TaskList ──────────────────────────────────────────────────────────────────

type TaskListTool struct{ store *Store }

func NewList() *TaskListTool       { return &TaskListTool{store: globalStore} }
func (*TaskListTool) Name() string { return "TaskList" }
func (*TaskListTool) Description() string {
	return "List all tasks in the current session with their statuses."
}
func (*TaskListTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","description":"Filter by status: pending, in_progress, completed, cancelled"}}}`)
}
func (*TaskListTool) IsReadOnly(json.RawMessage) bool        { return true }
func (*TaskListTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *TaskListTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		Status string `json:"status,omitempty"`
	}
	_ = json.Unmarshal(raw, &in)

	tasks := t.store.List()
	var filtered []map[string]any
	for _, task := range tasks {
		if in.Status != "" && string(task.Status) != in.Status {
			continue
		}
		filtered = append(filtered, map[string]any{
			"id":      task.ID,
			"subject": task.Subject,
			"status":  task.Status,
		})
	}
	if filtered == nil {
		filtered = []map[string]any{}
	}
	out, _ := json.Marshal(map[string]any{"tasks": filtered})
	return tool.TextResult(string(out)), nil
}

// ── TaskUpdate ────────────────────────────────────────────────────────────────

type TaskUpdateTool struct{ store *Store }

func NewUpdate() *TaskUpdateTool     { return &TaskUpdateTool{store: globalStore} }
func (*TaskUpdateTool) Name() string { return "TaskUpdate" }
func (*TaskUpdateTool) Description() string {
	return `Update a task's status, subject, description, or output.

Status lifecycle: pending → in_progress → completed | cancelled
- Set status to in_progress BEFORE starting work on a task
- Set status to completed AFTER finishing
- Set status to deleted to remove the task`
}
func (*TaskUpdateTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"taskId":      {"type": "string"},
			"subject":     {"type": "string"},
			"description": {"type": "string"},
			"status":      {"type": "string", "enum": ["pending","in_progress","completed","cancelled","deleted"]},
			"output":      {"type": "string", "description": "Append output/notes to the task"}
		},
		"required": ["taskId"]
	}`)
}
func (*TaskUpdateTool) IsReadOnly(json.RawMessage) bool        { return false }
func (*TaskUpdateTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *TaskUpdateTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		TaskID      string `json:"taskId"`
		Subject     string `json:"subject,omitempty"`
		Description string `json:"description,omitempty"`
		Status      string `json:"status,omitempty"`
		Output      string `json:"output,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input"), nil
	}
	if in.Status == "deleted" {
		t.store.Delete(in.TaskID)
		return tool.TextResult(`{"success":true}`), nil
	}
	err := t.store.Update(in.TaskID, func(task *Task) {
		if in.Subject != "" {
			task.Subject = in.Subject
		}
		if in.Description != "" {
			task.Description = in.Description
		}
		if in.Status != "" {
			task.Status = Status(in.Status)
		}
		if in.Output != "" {
			if task.Output != "" {
				task.Output += "\n"
			}
			task.Output += in.Output
		}
	})
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return tool.TextResult(`{"success":true}`), nil
}

// ── TaskOutput ────────────────────────────────────────────────────────────────

type TaskOutputTool struct{ store *Store }

func NewOutput() *TaskOutputTool     { return &TaskOutputTool{store: globalStore} }
func (*TaskOutputTool) Name() string { return "TaskOutput" }
func (*TaskOutputTool) Description() string {
	return "Append output or notes to a task without changing its status."
}
func (*TaskOutputTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"taskId":{"type":"string"},"output":{"type":"string"}},"required":["taskId","output"]}`)
}
func (*TaskOutputTool) IsReadOnly(json.RawMessage) bool        { return false }
func (*TaskOutputTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *TaskOutputTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		TaskID string `json:"taskId"`
		Output string `json:"output"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input"), nil
	}
	err := t.store.Update(in.TaskID, func(task *Task) {
		if task.Output != "" {
			task.Output += "\n"
		}
		task.Output += in.Output
	})
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return tool.TextResult(`{"success":true}`), nil
}

// ── TaskStop ──────────────────────────────────────────────────────────────────

type TaskStopTool struct{ store *Store }

func NewStop() *TaskStopTool       { return &TaskStopTool{store: globalStore} }
func (*TaskStopTool) Name() string { return "TaskStop" }
func (*TaskStopTool) Description() string {
	return "Cancel a running task and mark it as cancelled."
}
func (*TaskStopTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"taskId":{"type":"string"},"reason":{"type":"string"}},"required":["taskId"]}`)
}
func (*TaskStopTool) IsReadOnly(json.RawMessage) bool        { return false }
func (*TaskStopTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *TaskStopTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		TaskID string `json:"taskId"`
		Reason string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult("invalid input"), nil
	}
	err := t.store.Update(in.TaskID, func(task *Task) {
		task.Status = StatusCancelled
		if in.Reason != "" {
			if task.Output != "" {
				task.Output += "\n"
			}
			task.Output += "Stopped: " + in.Reason
		}
	})
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	return tool.TextResult(`{"success":true}`), nil
}

// RenderTaskList returns a compact text summary of all tasks (for TUI display).
func RenderTaskList(store *Store) string {
	tasks := store.List()
	if len(tasks) == 0 {
		return "No tasks."
	}
	var sb strings.Builder
	icons := map[Status]string{
		StatusPending:    "○",
		StatusInProgress: "◉",
		StatusCompleted:  "✓",
		StatusCancelled:  "✗",
	}
	for _, t := range tasks {
		icon := icons[t.Status]
		if icon == "" {
			icon = "?"
		}
		fmt.Fprintf(&sb, "%s [%s] %s\n", icon, t.ID, t.Subject)
	}
	return strings.TrimRight(sb.String(), "\n")
}
