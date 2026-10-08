package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/bgjobs"
)

// bashesTailBytes and bashesTailLines bound the output /bashes shows.
const (
	bashesTailBytes = 8 << 10
	bashesTailLines = 40
)

// RegisterBashesCommands registers /bashes and its alias /jobs: pick a
// background Bash job, then view its output or kill it. The picker dispatches
// `/bashes <id>` for the job and `/bashes view|kill <id>` for the action.
func RegisterBashesCommands(r *Registry) {
	handler := func(args string) Result { return bashesCommand(bgjobs.Default, args) }
	r.Register(Command{
		Name:        "bashes",
		Description: "List background shells, view their output, or kill one",
		Handler:     handler,
	})
	r.Register(Command{
		Name:        "jobs",
		Description: "Alias of /bashes",
		Hidden:      true,
		Handler:     handler,
	})
}

func bashesCommand(reg *bgjobs.Registry, args string) Result {
	f := strings.Fields(args)
	switch {
	case len(f) == 0:
		return bashesList(reg)
	case len(f) == 1:
		return bashesActions(reg, f[0])
	case f[0] == "view":
		if _, ok := reg.Get(f[1]); !ok {
			return Result{Type: "error", Text: fmt.Sprintf("No background shell %q.", f[1])}
		}
		// The TUI reads the output file off the update loop.
		return Result{Type: "bashes-view", Text: f[1]}
	case f[0] == "kill":
		// Signal only: waiting for the process to be reaped would block the
		// caller, and the footer shows the result.
		if err := reg.Stop(f[1]); err != nil {
			return Result{Type: "error", Text: err.Error()}
		}
		return Result{Type: "text", Text: fmt.Sprintf("%s: stopping…", f[1])}
	}
	return Result{Type: "error", Text: "Usage: /bashes [view|kill] [id]"}
}

func bashesList(reg *bgjobs.Registry) Result {
	jobs := reg.Snapshot()
	if len(jobs) == 0 {
		return Result{Type: "text", Text: "No background shells."}
	}
	items := make([]PickerOption, len(jobs))
	for i, j := range jobs {
		items[i] = PickerOption{Value: j.ID, Label: jobLabel(j)}
	}
	return pickerResultItems("bashes", "Background shells", "", items)
}

func bashesActions(reg *bgjobs.Registry, id string) Result {
	j, ok := reg.Get(id)
	if !ok {
		return Result{Type: "error", Text: fmt.Sprintf("No background shell %q.", id)}
	}
	items := []PickerOption{{Value: "view " + id, Label: "View output"}}
	if j.Running() {
		items = append(items, PickerOption{Value: "kill " + id, Label: "Kill"})
	}
	return pickerResultItems("bashes", jobLabel(j), "", items)
}

// BashesView renders a job's output tail for `/bashes view`. It reads the
// output file, so callers run it off the UI goroutine.
func BashesView(reg *bgjobs.Registry, id string) Result {
	j, ok := reg.Get(id)
	if !ok {
		return Result{Type: "error", Text: fmt.Sprintf("No background shell %q.", id)}
	}
	tail, err := reg.Tail(id, bashesTailBytes)
	if err != nil {
		return Result{Type: "error", Text: err.Error()}
	}
	lines := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	if len(lines) > bashesTailLines {
		lines = lines[len(lines)-bashesTailLines:]
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n", jobLabel(j), j.OutputPath)
	if out := strings.Join(lines, "\n"); strings.TrimSpace(out) != "" {
		sb.WriteString(out)
	} else {
		sb.WriteString("(no output yet)")
	}
	return Result{Type: "text", Text: sb.String()}
}

// jobLabel is the one-line description of a job in the picker.
func jobLabel(j bgjobs.Job) string {
	desc := j.Description
	if desc == "" {
		desc = j.Command
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if r := []rune(desc); len(r) > 60 {
		desc = string(r[:59]) + "…"
	}
	end := j.DoneAt
	if j.Running() {
		end = time.Now()
	}
	age := end.Sub(j.StartedAt).Round(time.Second)
	return fmt.Sprintf("%s  %s  %s  %s", j.ID, j.Status, age, desc)
}
