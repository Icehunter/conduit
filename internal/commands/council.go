package commands

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
)

// RegisterCouncilCommands adds the /council and /council-history commands.
func RegisterCouncilCommands(r *Registry) {
	r.Register(Command{
		Name:        "council",
		Description: "Run a council debate: /council <question>",
		Handler: func(args string) Result {
			question := strings.TrimSpace(args)
			if question == "" {
				return Result{Type: "error", Text: "Usage: /council <question>"}
			}
			return Result{Type: "council-chat", Text: question}
		},
	})

	r.Register(Command{
		Name:        "council-history",
		Description: "List past council debate transcripts for this project",
		Handler: func(_ string) Result {
			paths, err := listCouncilTranscripts()
			if err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("council-history: %v", err)}
			}
			if len(paths) == 0 {
				return Result{Type: "text", Text: "No council transcripts found for this project."}
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Council transcripts (newest first):\n")
			for i, p := range paths {
				fmt.Fprintf(&sb, "  %d. %s\n", i+1, p)
			}
			return Result{Type: "text", Text: strings.TrimSpace(sb.String())}
		},
	})
}

// projectHash returns a short hex string derived from cwd for namespacing storage.
func projectHash(cwd string) string {
	h := sha256.Sum256([]byte(cwd))
	return fmt.Sprintf("%x", h[:6])
}

// listCouncilTranscripts returns transcript file paths for the current
// working directory's project hash, newest first.
func listCouncilTranscripts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cwd, _ := os.Getwd()
	h := projectHash(cwd)
	dir := home + "/.conduit/projects/" + h + "/council"
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			paths = append(paths, dir+"/"+e.Name())
		}
	}
	return paths, nil
}
