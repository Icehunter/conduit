package bgjobs

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/coordinator"
)

// notifyTailBytes is how much of a job's output the notification carries.
const notifyTailBytes = 4 << 10

// Notification builds the <task-notification> message that tells the model a
// background command ended: its status, a one-line summary, and the tail of
// its output with the path of the full file.
func (r *Registry) Notification(j Job) string {
	tail, _ := r.Tail(j.ID, notifyTailBytes)
	return Notification(j, tail)
}

// Notification is the pure form of Registry.Notification for a given tail.
func Notification(j Job, tail string) string {
	label := j.Description
	if label == "" {
		label = j.Command
	}
	label = oneLine(label, 80)

	var summary string
	switch {
	case j.TimedOut:
		summary = fmt.Sprintf("Background command %q timed out and was stopped", label)
	case j.Status == StatusKilled:
		summary = fmt.Sprintf("Background command %q was stopped", label)
	default:
		verb := "completed"
		if j.Status == StatusFailed {
			verb = "failed"
		}
		summary = fmt.Sprintf("Background command %q %s (exit %d)", label, verb, j.ExitCode)
	}

	var result strings.Builder
	if t := strings.TrimRight(tail, "\n"); t != "" {
		result.WriteString(t)
		result.WriteByte('\n')
	}
	fmt.Fprintf(&result, "Full output: %s", j.OutputPath)
	return coordinator.TaskNotification(j.ID, string(j.Status), summary, result.String(), 0, 0, 0)
}

// oneLine collapses s to a single line of at most n runes.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
