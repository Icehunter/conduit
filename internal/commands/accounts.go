package commands

// RegisterAccountCommand adds the /account slash command.
func RegisterAccountCommand(r *Registry) {
	handler := func(string) Result {
		return Result{Type: "settings-panel", Text: "accounts"}
	}
	r.Register(Command{
		Name:        "account",
		Description: "Manage accounts — switch, add, logout, delete",
		Handler:     handler,
	})
	r.Register(Command{
		Name:        "accounts",
		Description: "Manage accounts — switch, add, logout, delete",
		Handler:     handler,
	})
	r.Register(Command{
		Name:        "providers",
		Description: "Manage model providers",
		Handler: func(string) Result {
			return Result{Type: "settings-panel", Text: "providers"}
		},
	})
}
