// Package filereadtool — URL scheme dispatch for the Read tool.
//
// Supported schemes:
//   - pr://owner/repo/N       — fetch a GitHub PR via `gh pr view`
//   - issue://owner/repo/N    — fetch a GitHub issue via `gh issue view`
//   - http:// / https://      — delegate to webfetchtool.Fetch
//   - (none / file path)      — handled by the existing local-file path
package filereadtool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/icehunter/conduit/internal/rtk"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/webfetchtool"
)

// validGHIdent validates that a GitHub owner or repo name contains only safe
// characters, preventing credential exfiltration via crafted --repo arguments.
var validGHIdent = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// lookPathFunc is the exec.LookPath implementation used by the gh dispatcher.
// Tests replace it to avoid requiring gh to be installed.
var lookPathFunc = exec.LookPath

// runCmdFunc is the function used to run a gh command and capture its output.
// Tests replace it to inject canned responses.
var runCmdFunc = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// httpFetchFunc is the function used by handleHTTP to retrieve URLs.
// Tests replace it with a plain-dialer variant to bypass the SSRF guard for
// httptest servers on 127.0.0.1.
var httpFetchFunc = func(ctx context.Context, rawURL, prompt string) (string, error) {
	return webfetchtool.Fetch(ctx, rawURL, prompt)
}

// dispatch detects a URL scheme in filePath and, if recognised, handles it.
// Returns (result, handled, error):
//   - handled=false  → caller should proceed with local-file logic
//   - handled=true   → result is ready; caller should return it
func dispatch(ctx context.Context, filePath string) (tool.Result, bool) {
	scheme, rest, hasScheme := strings.Cut(filePath, "://")
	if !hasScheme {
		// Plain path — local file.
		return tool.Result{}, false
	}

	switch strings.ToLower(scheme) {
	case "pr":
		return handleGH(ctx, rest, "pr"), true
	case "issue":
		return handleGH(ctx, rest, "issue"), true
	case "http", "https":
		return handleHTTP(ctx, filePath), true
	default:
		// Unknown scheme — fall through to local-file; the OS will give a
		// meaningful error ("no such file") for paths like "foo://bar".
		return tool.Result{}, false
	}
}

// handleHTTP delegates to httpFetchFunc (production: webfetchtool.Fetch).
func handleHTTP(ctx context.Context, rawURL string) tool.Result {
	text, err := httpFetchFunc(ctx, rawURL, "")
	if err != nil {
		if ctx.Err() != nil {
			return tool.ErrorResult("cancelled")
		}
		return tool.ErrorResult(fmt.Sprintf("http fetch failed: %s", err.Error()))
	}
	return tool.TextResult(text)
}

// ghPRJSON is the JSON shape returned by `gh pr view --json …`.
type ghPRJSON struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// ghIssueJSON is the JSON shape returned by `gh issue view --json …`.
type ghIssueJSON struct {
	Number   int              `json:"number"`
	Title    string           `json:"title"`
	State    string           `json:"state"`
	URL      string           `json:"url"`
	Body     string           `json:"body"`
	Comments []ghIssueComment `json:"comments"`
}

type ghIssueComment struct {
	Body   string `json:"body"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

// handleGH dispatches a pr:// or issue:// URL to the gh CLI.
// kind is "pr" or "issue". rest is the path after "://", i.e. "owner/repo/N".
func handleGH(ctx context.Context, rest, kind string) tool.Result {
	if _, err := lookPathFunc("gh"); err != nil {
		return tool.ErrorResult("gh CLI not found: install the GitHub CLI (https://cli.github.com) to use pr:// and issue:// URLs")
	}

	// Parse "owner/repo/N" — must have exactly 3 slash-separated parts.
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return tool.ErrorResult(fmt.Sprintf("invalid %s:// path %q — expected %s://owner/repo/N", kind, rest, kind))
	}
	owner, repo, numStr := parts[0], parts[1], parts[2]

	// Reject owner/repo identifiers with disallowed characters to prevent
	// credential exfiltration via crafted --repo arguments.
	if !validGHIdent.MatchString(owner) || !validGHIdent.MatchString(repo) {
		return tool.ErrorResult(fmt.Sprintf("invalid %s:// path: owner and repo must contain only [a-zA-Z0-9._-]", kind))
	}

	// Reject extra path segments after the number (e.g. pr://owner/repo/42/files).
	if strings.Contains(numStr, "/") {
		return tool.ErrorResult(fmt.Sprintf("invalid %s:// path %q — expected exactly %s://owner/repo/N", kind, rest, kind))
	}

	num, err := strconv.Atoi(numStr)
	if err != nil || num <= 0 {
		return tool.ErrorResult(fmt.Sprintf("invalid %s number %q — must be a positive integer", kind, numStr))
	}
	repoArg := owner + "/" + repo

	var args []string
	var cmdStr string
	if kind == "pr" {
		args = []string{"pr", "view", strconv.Itoa(num), "--repo", repoArg, "--json", "title,body,state,url,number"}
		cmdStr = fmt.Sprintf("gh pr view %d --repo %s --json title,body,state,url,number", num, repoArg)
	} else {
		args = []string{"issue", "view", strconv.Itoa(num), "--repo", repoArg, "--json", "title,body,state,url,number,comments"}
		cmdStr = fmt.Sprintf("gh issue view %d --repo %s --json title,body,state,url,number,comments", num, repoArg)
	}

	out, runErr := runCmdFunc(ctx, "gh", args...)
	if runErr != nil {
		if ctx.Err() != nil {
			return tool.ErrorResult("cancelled")
		}
		return tool.ErrorResult(fmt.Sprintf("gh %s view failed: %s", kind, runErr.Error()))
	}

	// Parse JSON and render to markdown first, then apply RTK to the rendered
	// text. Applying RTK to raw JSON before parsing would corrupt the JSON.
	text, parseErr := renderGHJSON(kind, out)
	if parseErr != nil {
		// JSON parse failed — return the raw output so the model still sees something useful.
		return tool.TextResult(string(out))
	}
	// Apply RTK in-process filter to the rendered markdown output.
	filtered := rtk.Filter(cmdStr, text).Filtered
	return tool.TextResult(filtered)
}

// renderGHJSON parses the gh CLI JSON output and produces a markdown summary.
func renderGHJSON(kind string, data []byte) (string, error) {
	var sb strings.Builder
	if kind == "pr" {
		var pr ghPRJSON
		if err := json.Unmarshal(bytes.TrimSpace(data), &pr); err != nil {
			return "", fmt.Errorf("filereadtool: renderGHJSON: unmarshal pr: %w", err)
		}
		fmt.Fprintf(&sb, "# PR #%d — %s\n\n", pr.Number, pr.Title)
		fmt.Fprintf(&sb, "**State:** %s  \n", pr.State)
		fmt.Fprintf(&sb, "**URL:** %s\n\n", pr.URL)
		if pr.Body != "" {
			sb.WriteString("## Description\n\n")
			sb.WriteString(pr.Body)
			sb.WriteString("\n")
		}
	} else {
		var issue ghIssueJSON
		if err := json.Unmarshal(bytes.TrimSpace(data), &issue); err != nil {
			return "", fmt.Errorf("filereadtool: renderGHJSON: unmarshal issue: %w", err)
		}
		fmt.Fprintf(&sb, "# Issue #%d — %s\n\n", issue.Number, issue.Title)
		fmt.Fprintf(&sb, "**State:** %s  \n", issue.State)
		fmt.Fprintf(&sb, "**URL:** %s\n\n", issue.URL)
		if issue.Body != "" {
			sb.WriteString("## Description\n\n")
			sb.WriteString(issue.Body)
			sb.WriteString("\n")
		}
		if len(issue.Comments) > 0 {
			sb.WriteString("\n## Comments\n\n")
			for i, c := range issue.Comments {
				fmt.Fprintf(&sb, "**Comment %d** (@%s)\n\n%s\n\n", i+1, c.Author.Login, c.Body)
			}
		}
	}
	return sb.String(), nil
}
