// Package ccrtool implements the CCRRetrieve tool — lets the agent retrieve
// the original content of a compressed output by its CCR handle.
package ccrtool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/ccr"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/truncate"
)

// store is the interface the tool needs from the CCR store.
// Satisfied by *ccr.Store; the indirection keeps Execute unit-testable.
type store interface {
	Get(handle string) (string, error)
	Slice(handle string, offset, limit int) (string, error)
}

// Package-level singleton store, mirroring the pattern in internal/rtk.
var (
	defaultOnce  sync.Once
	defaultStore *ccr.Store
)

func getStore() *ccr.Store {
	defaultOnce.Do(func() { defaultStore = ccr.DefaultStore() })
	return defaultStore
}

// Tool implements the CCRRetrieve tool.
type Tool struct{}

// New returns a CCRRetrieve tool.
func New() *Tool { return &Tool{} }

// Name implements tool.Tool.
func (*Tool) Name() string { return "CCRRetrieve" }

// Description is the prompt text the model sees.
func (*Tool) Description() string {
	return "Retrieve the original content of a compressed output by its CCR handle. " +
		"Use when you need the full detail that was compressed away. " +
		"Supports offset/limit for line ranges and pattern for case-insensitive substring filtering. " +
		"When both are specified, offset/limit is applied first, then pattern filters the result."
}

// InputSchema is the JSON Schema sent to the model.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"handle":  {"type": "string", "description": "CCR handle returned with the compressed output (format: ccr:<key>)"},
			"offset":  {"type": "number", "description": "0-based line index to start from (optional)"},
			"limit":   {"type": "number", "description": "Number of lines to return; 0 means all lines from offset (optional)"},
			"pattern": {"type": "string", "description": "Case-insensitive substring filter applied to retrieved lines (optional)"}
		},
		"required": ["handle"]
	}`)
}

// IsReadOnly: retrieval only reads the CCR store.
func (*Tool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe: reads are safe to run concurrently.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// input is the typed view of the JSON input.
type input struct {
	Handle  string `json:"handle"`
	Offset  int    `json:"offset,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

// Execute retrieves content from the CCR store and returns it.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	return executeWithStore(ctx, getStore(), raw)
}

// executeWithStore is the testable core — accepts any store implementation.
func executeWithStore(ctx context.Context, s store, raw json.RawMessage) (tool.Result, error) {
	_ = ctx // reserved for future cancellation propagation

	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("ccr retrieve: invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Handle) == "" {
		return tool.ErrorResult("ccr retrieve: `handle` is required"), nil
	}

	var (
		text string
		err  error
	)

	// Apply offset/limit first (if requested), then pattern-filter — they compose.
	if in.Offset != 0 || in.Limit != 0 {
		text, err = s.Slice(in.Handle, in.Offset, in.Limit)
	} else {
		text, err = s.Get(in.Handle)
	}
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("ccr retrieve: %v", err)), nil
	}
	if in.Pattern != "" {
		text = filterLines(text, in.Pattern)
	}

	// Re-run through truncate so a full retrieval can't blow up the context.
	maxLines, maxBytes := truncate.Limits()
	tr, _ := truncate.Apply(text, truncate.Options{
		MaxLines:  maxLines,
		MaxBytes:  maxBytes,
		Direction: "head",
	})
	return tool.TextResult(tr.Content), nil
}

// filterLines returns lines from text that contain pattern (case-insensitive).
func filterLines(text, pattern string) string {
	lower := strings.ToLower(pattern)
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(strings.ToLower(line), lower) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
