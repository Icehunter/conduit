package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/buddy"
)

// RegisterBuddyCommand adds /buddy to display and manage the companion.
func RegisterBuddyCommand(r *Registry, getUserID func() string) {
	r.Register(Command{
		Name:        "buddy",
		Description: "Meet your companion. /buddy name <name> to name them.",
		Handler: func(args string) Result {
			args = strings.TrimSpace(args)

			// /buddy name <name>
			if strings.HasPrefix(args, "name ") {
				name := strings.TrimSpace(strings.TrimPrefix(args, "name "))
				if name == "" {
					return Result{Type: "error", Text: "Usage: /buddy name <name>"}
				}
				userID := ""
				if getUserID != nil {
					userID = getUserID()
				}
				sc := &buddy.StoredCompanion{
					Name:      name,
					UserID:    userID,
					HatchedAt: time.Now().Format(time.RFC3339),
				}
				if err := buddy.Save(sc); err != nil {
					return Result{Type: "error", Text: fmt.Sprintf("buddy: save: %v", err)}
				}
				bones := buddy.GenerateBones(userID)
				return Result{Type: "text", Text: fmt.Sprintf("Your companion has been named!\n\n%s", buddy.Summary(bones, name))}
			}

			// /buddy — show companion or hatch notice
			sc, err := buddy.Load()
			if err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("buddy: %v", err)}
			}
			if sc == nil {
				return Result{Type: "text", Text: "You don't have a companion yet!\n\nUse /buddy name <name> to hatch one.\n\nYour companion is unique to you — generated from your account ID."}
			}

			userID := sc.UserID
			if userID == "" && getUserID != nil {
				userID = getUserID()
			}
			bones := buddy.GenerateBones(userID)
			return Result{Type: "text", Text: buddy.Summary(bones, sc.Name)}
		},
	})
}
