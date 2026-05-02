// Package websearchtool implements the WebSearch tool.
//
// Unlike most tools that call external APIs directly, WebSearch delegates to
// Anthropic's native web_search_20250305 server-side tool. It makes a
// secondary streaming API call with only the native search tool declared, then
// assembles the results (text, citations, search hits) into a single text
// block returned to the main agent.
//
// Mirrors src/tools/WebSearchTool/WebSearchTool.ts.
// max_uses=8 is hardcoded to match the TS implementation.
package websearchtool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/icehunter/claude-go/internal/api"
	"github.com/icehunter/claude-go/internal/tool"
)

// searchModel is the small fast model used for web search sub-calls.
// Mirrors getSmallFastModel() in the TS source.
const searchModel = "claude-haiku-4-5-20251001"

// maxSearchUses is the maximum number of searches per tool call.
const maxSearchUses = 8

// Tool implements the WebSearch tool.
type Tool struct {
	client *api.Client
}

// New returns a WebSearch tool backed by the given API client.
func New(client *api.Client) *Tool {
	return &Tool{client: client}
}

func (*Tool) Name() string { return "WebSearch" }

func (*Tool) Description() string {
	return "Search the web for current information. " +
		"Returns text summaries and source URLs. " +
		"Use for questions that require up-to-date information beyond your training data."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "The search query to use",
				"minLength": 2
			},
			"allowed_domains": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Only include results from these domains"
			},
			"blocked_domains": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Never include results from these domains"
			}
		},
		"required": ["query"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool      { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	Query          string   `json:"query"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	BlockedDomains []string `json:"blocked_domains,omitempty"`
}

// Execute runs a web search via Anthropic's native search tool and returns
// a formatted text block with results and sources.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Query) == "" {
		return tool.ErrorResult("`query` is required"), nil
	}

	// Build the native web_search_20250305 tool definition.
	searchToolExtra := map[string]any{
		"max_uses": maxSearchUses,
	}
	if len(in.AllowedDomains) > 0 {
		searchToolExtra["allowed_domains"] = in.AllowedDomains
	}
	if len(in.BlockedDomains) > 0 {
		searchToolExtra["blocked_domains"] = in.BlockedDomains
	}

	nativeSearchTool := api.ToolDef{
		Type:  "web_search_20250305",
		Name:  "web_search",
		Extra: searchToolExtra,
	}

	// Build the search request. We ask the model to answer the query using
	// the web search tool; it will perform searches and return text + citations.
	req := &api.MessageRequest{
		Model:     searchModel,
		MaxTokens: 4096,
		Messages: []api.Message{
			{
				Role: "user",
				Content: []api.ContentBlock{{
					Type: "text",
					Text: fmt.Sprintf("Search the web and answer: %s\n\nInclude URLs/sources for all facts.", in.Query),
				}},
			},
		},
		Stream: true,
		Tools:  []api.ToolDef{nativeSearchTool},
	}

	stream, err := t.client.StreamMessage(ctx, req)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("search request failed: %v", err)), nil
	}
	defer stream.Close()

	result, err := drainSearchStream(ctx, stream, in.Query)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return tool.ErrorResult("search cancelled"), nil
		}
		return tool.ErrorResult(fmt.Sprintf("search failed: %v", err)), nil
	}

	return tool.TextResult(result), nil
}

// drainSearchStream reads all events from the search stream and assembles
// the text response. The model interleaves server_tool_use, web_search_tool_result,
// text, and citation blocks; we collect all text and format source URLs.
func drainSearchStream(ctx context.Context, stream *api.Stream, query string) (string, error) {
	// Track accumulated text per block index.
	type blockInfo struct {
		blockType string
		text      strings.Builder
	}
	blocks := map[int]*blockInfo{}

	var orderedIndexes []int

	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}

		switch ev.Type {
		case "content_block_start":
			cbs, err := ev.AsContentBlockStart()
			if err != nil {
				continue
			}
			var raw struct {
				Type string `json:"type"`
			}
			if e := json.Unmarshal(cbs.ContentBlock, &raw); e == nil {
				bi := &blockInfo{blockType: raw.Type}
				blocks[cbs.Index] = bi
				orderedIndexes = append(orderedIndexes, cbs.Index)
			}

		case "content_block_delta":
			cbd, err := ev.AsContentBlockDelta()
			if err != nil {
				continue
			}
			bi, ok := blocks[cbd.Index]
			if !ok {
				continue
			}
			switch cbd.Delta.Type {
			case "text_delta":
				bi.text.WriteString(cbd.Delta.Text)
			case "input_json_delta":
				bi.text.WriteString(cbd.Delta.PartialJSON)
			}
		}
	}

	// Assemble output: collect text blocks; skip server_tool_use and web_search_tool_result.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Web search results for: %q\n\n", query))

	seenIdx := make(map[int]bool)
	for _, idx := range orderedIndexes {
		if seenIdx[idx] {
			continue
		}
		seenIdx[idx] = true
		bi := blocks[idx]
		if bi == nil {
			continue
		}
		if bi.blockType == "text" && bi.text.Len() > 0 {
			sb.WriteString(bi.text.String())
			sb.WriteByte('\n')
		}
	}

	result := strings.TrimSpace(sb.String())
	if result == fmt.Sprintf("Web search results for: %q", query) {
		return fmt.Sprintf("Web search for %q returned no text results.", query), nil
	}
	return result, nil
}
