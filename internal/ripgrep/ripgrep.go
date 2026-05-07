// Package ripgrep is a shared wrapper around the rg binary.
// It mirrors utils/ripgrep.ts from Claude Code: shared utility used by
// GrepTool, GlobalSearchDialog, and any other consumer that needs
// fast text search over files.
package ripgrep

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
)

var rgLinePattern = regexp.MustCompile(`^(.*):([0-9]+):(.*)$`)

// Find locates the rg binary, checking PATH then common Homebrew locations.
// Returns "" if not found.
func Find() string {
	if p, err := exec.LookPath("rg"); err == nil {
		return p
	}
	for _, c := range []string{
		"/opt/homebrew/bin/rg",
		"/usr/local/bin/rg",
		"/usr/bin/rg",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// Available reports whether rg is on PATH.
func Available() bool { return Find() != "" }

// Result is one match from a ripgrep run.
type Result struct {
	File    string
	Line    int
	Content string // matched line text (trimmed)
}

// Search runs rg with the given pattern in dir and returns matches.
// Returns nil results (not an error) if rg exits 1 (no matches).
// maxResults caps output; 0 = no limit.
func Search(pattern, dir string, maxResults int, extraArgs ...string) ([]Result, error) {
	rg := Find()
	if rg == "" {
		return nil, nil // caller falls back to stdlib grep
	}

	args := []string{
		"--line-number",
		"--no-heading",
		"--with-filename",
		"--color=never",
		"--smart-case",
	}
	if maxResults > 0 {
		args = append(args, "--max-count", "1") // per-file limit handled below
	}
	args = append(args, extraArgs...)
	args = append(args, "--", pattern)
	if dir != "" {
		args = append(args, dir)
	}

	var out bytes.Buffer
	cmd := exec.CommandContext(context.Background(), rg, args...)
	cmd.Stdout = &out
	cmd.Stderr = &bytes.Buffer{}
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil // no matches
		}
		return nil, err
	}

	var results []Result
	for _, line := range bytes.Split(bytes.TrimRight(out.Bytes(), "\n"), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		m := rgLinePattern.FindSubmatch(line)
		if len(m) != 4 {
			continue
		}
		var lineNum int
		for _, b := range m[2] {
			if b >= '0' && b <= '9' {
				lineNum = lineNum*10 + int(b-'0')
			}
		}
		results = append(results, Result{
			File:    string(m[1]),
			Line:    lineNum,
			Content: string(bytes.TrimSpace(m[3])),
		})
		if maxResults > 0 && len(results) >= maxResults {
			break
		}
	}
	return results, nil
}
