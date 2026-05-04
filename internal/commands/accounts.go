package commands

// RegisterAccountCommand adds the /account slash command.
func RegisterAccountCommand(r *Registry) {
	r.Register(Command{
		Name:        "account",
		Description: "Manage accounts — switch, add, logout, delete",
		Handler: func(string) Result {
			return Result{Type: "settings-panel", Text: "accounts"}
		},
	})
}
