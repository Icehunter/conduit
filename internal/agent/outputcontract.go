package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// An output contract turns a sub-agent from something that returns prose into a
// node another step can consume without a human in between. The parent supplies
// a JSON Schema; the child is told to satisfy it, and the result is validated
// here rather than taken on trust. A model claiming it returned the right shape
// is not evidence that it did.

// compileOutputSchema parses and compiles a JSON Schema. A malformed schema is
// rejected up front so the caller learns immediately, rather than after paying
// for a sub-agent run that could never have been accepted.
func compileOutputSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("output_schema is not valid JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("output.json", doc); err != nil {
		return nil, fmt.Errorf("output_schema rejected: %w", err)
	}
	sch, err := c.Compile("output.json")
	if err != nil {
		return nil, fmt.Errorf("output_schema is not a valid JSON Schema: %w", err)
	}
	return sch, nil
}

// outputContractPrompt is the instruction block appended to the child's system
// prompt. It is deliberately blunt: partial compliance is the common failure,
// and prose wrapped around correct JSON is still a contract violation because
// the caller parses the whole response.
func outputContractPrompt(raw json.RawMessage) string {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		pretty.Write(raw)
	}
	return "# Output contract\n\n" +
		"Your final message must be a single JSON value satisfying this schema, and nothing else.\n\n" +
		"```json\n" + pretty.String() + "\n```\n\n" +
		"No prose before or after it. No markdown fence around it. No explanation of what you did.\n" +
		"If you cannot satisfy the schema, still return JSON of the required shape and put the reason " +
		"in whichever field best carries it — a malformed response is discarded and the work is wasted."
}

// extractOutputJSON pulls the JSON value out of a final assistant message.
// Models frequently wrap the answer in a fence or a sentence even when told not
// to; recovering that is cheaper than a retry, so it is tried first.
func extractOutputJSON(text string) (json.RawMessage, bool) {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil, false
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s), true
	}
	// Fenced block, with or without a language tag.
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if j := strings.Index(rest, "```"); j >= 0 {
			candidate := strings.TrimSpace(rest[:j])
			if json.Valid([]byte(candidate)) {
				return json.RawMessage(candidate), true
			}
		}
	}
	// Widest balanced object or array in the text.
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		start := strings.IndexByte(s, pair[0])
		end := strings.LastIndexByte(s, pair[1])
		if start >= 0 && end > start {
			candidate := strings.TrimSpace(s[start : end+1])
			if json.Valid([]byte(candidate)) {
				return json.RawMessage(candidate), true
			}
		}
	}
	return nil, false
}

// validateOutput checks raw against the compiled schema, returning an error
// phrased for a model to act on.
func validateOutput(sch *jsonschema.Schema, raw json.RawMessage) error {
	if sch == nil {
		return nil
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("response was not valid JSON: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("response did not satisfy output_schema: %w", err)
	}
	return nil
}

// outputRetryPrompt is the single follow-up sent when the first attempt misses.
// One retry, not a loop: if a second attempt with the exact error still fails,
// the schema or the task is wrong and burning more turns will not fix it.
func outputRetryPrompt(reason string) string {
	return "Your previous response was rejected.\n\n" +
		reason + "\n\n" +
		"Reply again with only the JSON value required by the output contract. " +
		"No prose, no fence, no commentary."
}
