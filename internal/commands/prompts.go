package commands

// RegisterPromptCommands registers slash commands that inject a canned prompt
// into the agent as a user turn. Mirrors the "type: prompt" commands in
// src/commands/ of the TS source.
func RegisterPromptCommands(r *Registry) {
	r.Register(Command{
		Name:        "init",
		Description: "Initialize an AGENTS.md file for this project",
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

// initPrompt is the body the agent receives when the user runs `/init`.
const initPrompt = `Analyze this codebase and create/update **AGENTS.md** to help future agents work effectively in this repository.

**First**: Check if the directory is empty or contains only config files. If so, stop and say "Directory appears empty or only contains config. Add source code first, then run /init to generate AGENTS.md."

**Goal**: Document what an agent needs to know to work in this codebase — commands, patterns, conventions, gotchas, overall architecture, how components fit together.

**Discovery process**:

1. Check directory contents
2. Look for existing rule files (` + "`AGENTS.md`" + `, ` + "`CLAUDE.md`" + `, ` + "`.cursor/rules/*.md`" + `, ` + "`.cursorrules`" + `, ` + "`.github/copilot-instructions.md`" + `) — read them if they exist, then improve on their content
3. Identify project type from config files and directory structure
4. Find build/test/lint commands from config files, scripts, Makefiles, or CI configs
5. Read representative source files to understand code patterns, architecture, and control/data flow
6. If AGENTS.md already exists, read it and improve it rather than replacing wholesale

**Content to include**:

- Essential commands (build, test, run, lint, deploy) — whatever is relevant for this project
- Code organisation and structure, application architecture, control/data flow
- Naming conventions and style patterns
- Testing approach and patterns
- Important gotchas or non-obvious patterns
- Any project-specific context from existing rule files

**Note**: LLM agents learn and adapt as they read files, so documenting obvious details they would immediately pick up from reading a file or two is actively detrimental. Focus on non-obvious knowledge that saves the agent from trial-and-error discovery: gotchas, implicit conventions, commands with surprising flags, and context that is not self-evident from a single file.

**Format**: Clear markdown sections. Use your judgment on structure based on what you find. Aim for completeness over brevity — include everything an agent would need to know.

**Critical**: Only document what you actually observe. Never invent commands, patterns, or conventions. If you cannot find something, do not include it.`

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
