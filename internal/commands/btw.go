package commands

import "strings"

// registerBtwCommand adds /btw, a side question answered from the conversation
// without becoming part of it. The TUI runs the request when it sees a result
// of Type "btw" carrying the question.
func registerBtwCommand(r *Registry) {
	r.Register(Command{
		Name:        "btw",
		Description: "Ask a quick side question without adding it to the conversation",
		Handler: func(args string) Result {
			q := strings.TrimSpace(args)
			if q == "" {
				return Result{Type: "error", Text: "Usage: /btw <question>"}
			}
			return Result{Type: "btw", Text: q}
		},
	})
}
