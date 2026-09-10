package commands

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/workflow"
)

// RegisterWorkflowCommands registers /workflows (opens the run panel) and one
// slash command per saved workflow. A saved-workflow command does not run
// the script itself: like CC, it asks the model to invoke the Workflow tool
// so the model stays in the loop to present the result. Plugin and bundled
// commands registered earlier win on name collision.
func RegisterWorkflowCommands(r *Registry, cwd string) {
	r.Register(Command{
		Name:        "workflows",
		Description: "Watch running workflows, kill one, or save a run's script",
		Handler: func(string) Result {
			return Result{Type: "workflow-panel"}
		},
	})
	for _, s := range workflow.Discover(cwd) {
		if s.Err != "" {
			continue
		}
		if _, exists := r.cmds[s.Name]; exists {
			continue
		}
		name, desc, when := s.Name, s.Meta.Description, s.Meta.WhenToUse
		if when != "" {
			desc = desc + " — " + when
		}
		r.Register(Command{
			Name:        name,
			Description: desc,
			Handler: func(args string) Result {
				return Result{Type: "prompt", Text: workflowPrompt(name, args)}
			},
		})
	}
}

func workflowPrompt(name, args string) string {
	args = strings.TrimSpace(args)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Run the saved workflow %q with the Workflow tool: Workflow({name: %q", name, name)
	if args != "" {
		if strings.HasPrefix(args, "{") || strings.HasPrefix(args, "[") {
			fmt.Fprintf(&sb, ", args: %s", args)
		} else {
			fmt.Fprintf(&sb, ", args: %q", args)
		}
	}
	sb.WriteString("}). The user invoked it as a slash command, which is an explicit opt-in. When the <task-notification> arrives, present its result.")
	return sb.String()
}

// UltracodeKeyword is the opt-in keyword that turns on workflow authoring
// for a turn (and, with /ultracode on, for the session).
const UltracodeKeyword = "ultracode"

// HasUltracode reports whether a user message carries the opt-in keyword.
func HasUltracode(text string) bool {
	for _, f := range strings.Fields(strings.ToLower(text)) {
		if strings.Trim(f, ".,;:!?\"'()[]") == UltracodeKeyword {
			return true
		}
	}
	return false
}

// UltracodeReminder is the system-reminder block appended to a user turn
// that opted in, so the model knows the Workflow gate is open.
const UltracodeReminder = "<system-reminder>The user included \"ultracode\" in this message: multi-agent orchestration is explicitly opted in for this turn. Author and run a Workflow for the substantive work instead of doing it solo.</system-reminder>"

// UltracodeSessionReminder is injected on every turn while /ultracode is on.
const UltracodeSessionReminder = "<system-reminder>Ultracode is ON for this session. That opt-in is standing: author and run a Workflow for every substantive task by default; solo only on conversational turns or trivial edits.</system-reminder>"

// RegisterUltracodeCommand registers /ultracode [on|off] which toggles the
// standing opt-in. get/set are provided by the TUI, which owns the flag.
func RegisterUltracodeCommand(r *Registry, get func() bool, set func(bool)) {
	r.Register(Command{
		Name:        "ultracode",
		Description: "Toggle standing opt-in to workflow orchestration for every substantive task",
		Handler: func(args string) Result {
			switch strings.ToLower(strings.TrimSpace(args)) {
			case "on":
				set(true)
			case "off":
				set(false)
			case "":
				set(!get())
			default:
				return Result{Type: "error", Text: "usage: /ultracode [on|off]"}
			}
			if get() {
				return Result{Type: "flash", Text: "ultracode ON — the model will author workflows by default"}
			}
			return Result{Type: "flash", Text: "ultracode off"}
		},
	})
}
