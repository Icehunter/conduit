package commands

// RegisterPromptCommands registers slash commands that inject a canned prompt
// into the agent as a user turn. Mirrors the "type: prompt" commands in
// src/commands/ of the TS source.
func RegisterPromptCommands(r *Registry) {
	r.Register(Command{
		Name:        "init",
		Description: "Initialize a CLAUDE.md file for this project",
		Handler: func(args string) Result {
			return Result{Type: "prompt", Text: initPrompt}
		},
	})

	r.Register(Command{
		Name:        "review",
		Description: "Review a pull request (provide PR number or leave blank)",
		Handler: func(args string) Result {
			return Result{Type: "prompt", Text: reviewPrompt(args)}
		},
	})

	r.Register(Command{
		Name:        "commit",
		Description: "Create a git commit with a generated message",
		Handler: func(args string) Result {
			return Result{Type: "prompt", Text: commitPrompt}
		},
	})

	r.Register(Command{
		Name:        "pr-comments",
		Description: "Address comments on the current pull request",
		Handler: func(args string) Result {
			return Result{Type: "prompt", Text: prCommentsPrompt}
		},
	})

	r.Register(Command{
		Name:        "fix",
		Description: "Fix an issue, test failure, or lint error",
		Handler: func(args string) Result {
			prompt := "Please fix the following issue:\n\n" + args
			if args == "" {
				prompt = "Please look at the current state of the codebase, identify any issues (failing tests, lint errors, type errors, broken builds), and fix them."
			}
			return Result{Type: "prompt", Text: prompt}
		},
	})
}

const initPrompt = `Set up a minimal CLAUDE.md for this repo. CLAUDE.md is loaded into every Claude Code session, so it must be concise — only include what Claude would get wrong without it.

Steps:
1. Check if a CLAUDE.md already exists. If it does, suggest improvements to it.
2. Read the README.md and any existing documentation.
3. Identify the build system, test runner, and common workflows.
4. Create or update CLAUDE.md with:
   - How to build, test, and lint the project (exact commands)
   - High-level architecture overview (only what requires reading multiple files to understand)
   - Any non-obvious conventions or gotchas

Keep it concise. Do not include obvious advice or generic best practices. Do not list every file or component.`

func reviewPrompt(args string) string {
	if args == "" {
		return `You are an expert code reviewer. Follow these steps:

1. Run ` + "`gh pr list`" + ` to show open PRs, then ask which one to review
2. Run ` + "`gh pr view <number>`" + ` to get PR details
3. Run ` + "`gh pr diff <number>`" + ` to get the diff
4. Provide a thorough code review covering:
   - Overview of what the PR does
   - Code quality and style
   - Specific suggestions for improvements
   - Potential issues or risks
   - Test coverage
   - Security considerations`
	}
	return `You are an expert code reviewer. Review PR #` + args + `:

1. Run ` + "`gh pr view " + args + "`" + ` to get PR details
2. Run ` + "`gh pr diff " + args + "`" + ` to get the diff
3. Provide a thorough code review covering:
   - Overview of what the PR does
   - Code quality and style
   - Specific suggestions for improvements
   - Potential issues or risks
   - Test coverage
   - Security considerations

Format your review with clear sections and bullet points.`
}

const commitPrompt = `Create a git commit for the current changes.

Context:
- Run ` + "`git status`" + ` to see changed files
- Run ` + "`git diff HEAD`" + ` to see all changes
- Run ` + "`git log --oneline -10`" + ` to see recent commits and follow the commit message style

Git Safety Protocol:
- NEVER update the git config
- NEVER skip hooks (--no-verify) unless explicitly requested
- ALWAYS create NEW commits, never amend
- Do not commit files that likely contain secrets (.env, credentials.json, etc)
- If there are no changes to commit, say so and stop

Steps:
1. Check current git status and diff
2. Stage relevant files
3. Write a concise commit message (1-2 sentences, focus on WHY not WHAT)
4. Create the commit`

const prCommentsPrompt = `Address the review comments on the current pull request.

Steps:
1. Run ` + "`gh pr view --comments`" + ` to see all review comments
2. Run ` + "`gh pr diff`" + ` to understand the current state
3. For each comment:
   - Understand what the reviewer is asking for
   - Make the requested change
   - Note any comments you disagree with and explain why
4. After addressing all comments, summarize what was changed`
