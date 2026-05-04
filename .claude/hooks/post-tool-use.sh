#!/bin/bash
# Post-tool-use hook — fires after Write or Edit on Go files

set -e

INPUT="$1"
FILE_PATH=$(echo "$INPUT" | jq -r '.parameters.file_path // empty')

# Only fire for Go files
[[ "$FILE_PATH" == *.go ]] || exit 0

if [[ "$FILE_PATH" == *_test.go ]]; then
    jq -n '{
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": "Test written. Run `make verify` once implementation is complete."
        }
    }'
else
    TEST_FILE="${FILE_PATH%.go}_test.go"
    if [[ ! -f "$TEST_FILE" ]]; then
        jq -n --arg tf "$TEST_FILE" '{
            "hookSpecificOutput": {
                "hookEventName": "PostToolUse",
                "additionalContext": ("Implementation written — no test file found at " + $tf + ". Write tests before marking done.")
            }
        }'
    else
        jq -n '{
            "hookSpecificOutput": {
                "hookEventName": "PostToolUse",
                "additionalContext": "Implementation written. Run `make verify` before marking done."
            }
        }'
    fi
fi

exit 0
