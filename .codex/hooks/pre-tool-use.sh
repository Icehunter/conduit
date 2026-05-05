#!/bin/bash
# Pre-tool-use hook — enforces conduit coding standards before writes/edits

set -e

INPUT="$1"
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool // empty')
FILE_PATH=$(echo "$INPUT" | jq -r '.parameters.file_path // empty')
COMMAND=$(echo "$INPUT" | jq -r '.parameters.command // empty')

PARTS=()

# ── Write / Edit checks ────────────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "Write" || "$TOOL_NAME" == "Edit" ]]; then

    # New implementation file without a test file
    if [[ "$FILE_PATH" == *.go && "$FILE_PATH" != *_test.go ]]; then
        TEST_FILE="${FILE_PATH%.go}_test.go"
        if [[ ! -f "$FILE_PATH" && ! -f "$TEST_FILE" ]]; then
            PARTS+=("NEW FILE: Have you written the test first? Expected: ${TEST_FILE}")
        fi

        # Tool packages must keep the tool.Tool interface shape
        if [[ "$FILE_PATH" == */tools/*/*.go ]]; then
            PARTS+=("TOOL PACKAGE: Ensure Execute(ctx, json.RawMessage) (tool.Result, error) signature. IsReadOnly and IsConcurrencySafe must be implemented. See internal/tool/tool.go for the interface.")
        fi

        # TUI model — Bubble Tea v2 discipline
        if [[ "$FILE_PATH" == */tui/model.go || "$FILE_PATH" == */tui/*.go ]]; then
            PARTS+=("TUI: No blocking operations in Update(). Side-effects via tea.Cmd only. View() must be pure. Bubble Tea v2 uses tea.KeyPressMsg, not tea.KeyMsg.")
        fi

        # RTK filters — keep in internal/rtk/
        if [[ "$FILE_PATH" == */bashtool/* ]] && grep -q "TrimSpace\|truncate\|[:space:]" "$FILE_PATH" 2>/dev/null; then
            PARTS+=("RTK REMINDER: Output trimming belongs in internal/rtk/ classifiers, not in bashtool directly.")
        fi
    fi

fi

# ── Bash checks ────────────────────────────────────────────────────────────────
if [[ "$TOOL_NAME" == "Bash" ]]; then
    if [[ "$COMMAND" == *"go test"* && "$COMMAND" != *"-race"* ]]; then
        PARTS+=("TESTING: Run with -race flag. Concurrent code must pass: go test -race ./...")
    fi
    if [[ "$COMMAND" == *"golangci-lint"* && "$COMMAND" != *"go tool"* ]]; then
        PARTS+=("LINT: Use 'go tool github.com/golangci/golangci-lint/v2/cmd/golangci-lint run' (pinned) or 'make lint', not a global golangci-lint binary.")
    fi
fi

# ── Output ─────────────────────────────────────────────────────────────────────
if [[ ${#PARTS[@]} -gt 0 ]]; then
    CONTEXT=""
    for part in "${PARTS[@]}"; do
        [[ -n "$CONTEXT" ]] && CONTEXT="$CONTEXT | "
        CONTEXT="$CONTEXT$part"
    done
    jq -n --arg ctx "$CONTEXT" '{
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "additionalContext": $ctx
        }
    }'
fi

exit 0
