// Package commands implements the slash-command registry for conduit.
//
// Mirrors src/commands/ from the TS source. Each command is a function that
// receives the argument string and returns a Result. The TUI dispatches slash
// commands before sending to the agent loop.
package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// Result is what a command returns to the TUI.
type Result struct {
	// Type is "text", "clear", "model", "compact", "prompt", "error", etc.
	Type string
	// Text is the message to display or the prompt to inject (for Type=="prompt").
	Text string
	// Model is the new model name (for Type=="model").
	Model string
	// Provider is the selected provider (for Type=="provider-switch").
	Provider *settings.ActiveProviderSettings
	// Role is the provider role being changed (main, implement, planning,
	// background). Empty means main for compatibility.
	Role string
}

// PickerOption is one row in the small generic picker overlay used by
// /theme, /model, and /output-style. Exported because the TUI parses
// these via JSON (see model.go renderPicker).
type PickerOption struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Section bool   `json:"section,omitempty"`
}

// pickerPayload is the JSON wire format for picker Result.Text.
type pickerPayload struct {
	Title   string         `json:"title"`
	Current string         `json:"current"`
	Items   []PickerOption `json:"items"`
}

// pickerResult builds a Result that opens a generic picker overlay in the
// TUI. On Enter the TUI dispatches `/<kind> <selectedValue>` back through
// the registry, so this command is also responsible for handling the
// post-selection arg-form invocation. labels defaults to values.
func pickerResult(kind, title, current string, values []string, labels ...[]string) Result {
	items := make([]PickerOption, len(values))
	var labelList []string
	if len(labels) > 0 {
		labelList = labels[0]
	}
	for i, v := range values {
		label := v
		if i < len(labelList) {
			label = labelList[i]
		}
		items[i] = PickerOption{Value: v, Label: label}
	}
	return pickerResultItems(kind, title, current, items)
}

func pickerResultItems(kind, title, current string, items []PickerOption) Result {
	data, err := json.Marshal(pickerPayload{Title: title, Current: current, Items: items})
	if err != nil {
		return Result{Type: "error", Text: "picker: " + err.Error()}
	}
	return Result{Type: "picker", Model: kind, Text: string(data)}
}

// Handler is a slash command implementation.
type Handler func(args string) Result

// Command describes one slash command.
type Command struct {
	Name        string
	Description string
	Hidden      bool
	Handler     Handler
}

// Registry holds all registered slash commands.
type Registry struct {
	cmds map[string]Command
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{cmds: make(map[string]Command)}
}

// Register adds a command. Overwrites any existing command with the same name.
func (r *Registry) Register(cmd Command) {
	r.cmds[cmd.Name] = cmd
}

// Dispatch runs the command for input (which may or may not start with "/").
// Returns (result, true) if the command was found, (zero, false) otherwise.
func (r *Registry) Dispatch(input string) (Result, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return Result{}, false
	}
	parts := strings.SplitN(input[1:], " ", 2)
	name := strings.ToLower(parts[0])
	args := ""
	if len(parts) > 1 {
		args = parts[1]
	}
	cmd, ok := r.cmds[name]
	if !ok {
		return Result{Type: "error", Text: fmt.Sprintf("Unknown command: /%s (type /help for list)", name)}, true
	}
	return cmd.Handler(args), true
}

// All returns all commands sorted by name.
func (r *Registry) All() []Command {
	out := make([]Command, 0, len(r.cmds))
	for _, c := range r.cmds {
		if c.Hidden {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
