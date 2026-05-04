// Package lsp implements the LSPTool — lets Claude query a Language Server
// for hover info, definitions, references, and diagnostics.
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	lspclient "github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/tool"
)

// managerIface is the subset of *lspclient.Manager the tool needs.
// It is also satisfied by the test stub.
type managerIface interface {
	ServerFor(ctx context.Context, filePath string) (*lspclient.Client, error)
}

// Tool is the LSP tool that exposes hover, definition, references, and
// diagnostics operations to the agent.
type Tool struct {
	Manager managerIface
}

// New creates a new LSPTool backed by the given Manager.
func New(mgr managerIface) *Tool {
	return &Tool{Manager: mgr}
}

// Name implements tool.Tool.
func (*Tool) Name() string { return "LSP" }

// Description implements tool.Tool.
func (*Tool) Description() string {
	return "Query a Language Server for code intelligence: hover documentation, " +
		"go-to-definition locations, find-all-references, and file diagnostics. " +
		"Requires gopls / typescript-language-server / pylsp / rust-analyzer on PATH " +
		"depending on the file's language."
}

// InputSchema implements tool.Tool.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"operation": {
				"type": "string",
				"enum": ["hover", "definition", "references", "diagnostics"],
				"description": "LSP operation to perform"
			},
			"file": {
				"type": "string",
				"description": "Absolute or relative path to the source file"
			},
			"line": {
				"type": "integer",
				"description": "Zero-based line number (required for hover, definition, references)"
			},
			"character": {
				"type": "integer",
				"description": "Zero-based character offset (required for hover, definition, references)"
			}
		},
		"required": ["operation", "file"]
	}`)
}

// IsReadOnly: LSP queries do not modify files.
func (*Tool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe: LSP requests are safe to issue concurrently from Claude's
// perspective (the server serialises them internally).
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// input is the decoded JSON payload from the model.
type input struct {
	Operation string `json:"operation"`
	File      string `json:"file"`
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// Execute implements tool.Tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}

	// Resolve file to an absolute path.
	absFile, err := filepath.Abs(in.File)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot resolve path %q: %v", in.File, err)), nil
	}

	// Obtain the language server for this file.
	cl, err := t.Manager.ServerFor(ctx, absFile)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("no LSP server available: %v", err)), nil
	}

	fileURI := fileToURI(absFile)
	ext := strings.ToLower(filepath.Ext(absFile))
	langID := lspclient.LanguageID(ext)

	// Open the document so the server knows about it.
	if err := didOpen(ctx, cl, fileURI, langID, absFile); err != nil {
		return tool.ErrorResult(fmt.Sprintf("textDocument/didOpen failed: %v", err)), nil
	}
	defer func() { _ = didClose(ctx, cl, fileURI) }()

	switch in.Operation {
	case "hover":
		return t.hover(ctx, cl, fileURI, in.Line, in.Character)
	case "definition":
		return t.definition(ctx, cl, fileURI, in.Line, in.Character)
	case "references":
		return t.references(ctx, cl, fileURI, in.Line, in.Character)
	case "diagnostics":
		return t.diagnostics(cl, fileURI)
	default:
		return tool.ErrorResult(fmt.Sprintf("unknown operation %q; must be hover|definition|references|diagnostics", in.Operation)), nil
	}
}

// hover performs textDocument/hover and returns formatted markdown.
func (t *Tool) hover(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) {
	params := lspclient.TextDocumentPositionParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     lspclient.Position{Line: line, Character: char},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	result, err := cl.Request(ctx, "textDocument/hover", raw)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("textDocument/hover: %v", err)), nil
	}

	if isNull(result) {
		return tool.TextResult("No hover information available at this position."), nil
	}

	var h lspclient.Hover
	if err := json.Unmarshal(result, &h); err != nil {
		return tool.ErrorResult(fmt.Sprintf("hover decode: %v", err)), nil
	}

	text := strings.TrimSpace(h.Contents.Value)
	if text == "" {
		return tool.TextResult("No hover information available at this position."), nil
	}
	return tool.TextResult(text), nil
}

// definition performs textDocument/definition.
func (t *Tool) definition(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) {
	params := lspclient.TextDocumentPositionParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     lspclient.Position{Line: line, Character: char},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	result, err := cl.Request(ctx, "textDocument/definition", raw)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("textDocument/definition: %v", err)), nil
	}

	if isNull(result) {
		return tool.TextResult("No definition found."), nil
	}

	locs, err := parseLocations(result)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("definition decode: %v", err)), nil
	}
	if len(locs) == 0 {
		return tool.TextResult("No definition found."), nil
	}

	return tool.TextResult(formatLocations("Definition", locs)), nil
}

// references performs textDocument/references.
func (t *Tool) references(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) {
	params := lspclient.ReferenceParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     lspclient.Position{Line: line, Character: char},
		Context:      lspclient.ReferenceContext{IncludeDeclaration: true},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	result, err := cl.Request(ctx, "textDocument/references", raw)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("textDocument/references: %v", err)), nil
	}

	if isNull(result) {
		return tool.TextResult("No references found."), nil
	}

	locs, err := parseLocations(result)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("references decode: %v", err)), nil
	}
	if len(locs) == 0 {
		return tool.TextResult("No references found."), nil
	}

	return tool.TextResult(formatLocations("References", locs)), nil
}

// diagnostics returns cached diagnostics pushed by the server.
func (t *Tool) diagnostics(cl *lspclient.Client, uri string) (tool.Result, error) {
	diags := cl.Diagnostics(uri)
	if len(diags) == 0 {
		return tool.TextResult("No diagnostics for this file."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Diagnostics (%d):\n", len(diags))
	for _, d := range diags {
		sev := d.Severity.String()
		src := ""
		if d.Source != "" {
			src = fmt.Sprintf(" [%s]", d.Source)
		}
		fmt.Fprintf(&sb, "  %s:%d:%d %s%s: %s\n",
			uriToPath(uri),
			d.Range.Start.Line+1,
			d.Range.Start.Character+1,
			sev, src, d.Message,
		)
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// --- helpers ---

func didOpen(ctx context.Context, cl *lspclient.Client, uri, langID, absPath string) error {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	params := lspclient.DidOpenTextDocumentParams{
		TextDocument: lspclient.TextDocumentItem{
			URI:        uri,
			LanguageID: langID,
			Version:    1,
			Text:       string(content),
		},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return cl.Notify("textDocument/didOpen", raw)
}

func didClose(ctx context.Context, cl *lspclient.Client, uri string) error {
	params := lspclient.DidCloseTextDocumentParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return cl.Notify("textDocument/didClose", raw)
}

func marshalRequest(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	return b, nil
}

func isNull(r json.RawMessage) bool {
	return len(r) == 0 || string(r) == "null"
}

// parseLocations handles both Location and []Location responses.
func parseLocations(raw json.RawMessage) ([]lspclient.Location, error) {
	// Try array first.
	var locs []lspclient.Location
	if err := json.Unmarshal(raw, &locs); err == nil {
		return locs, nil
	}
	// Single location.
	var loc lspclient.Location
	if err := json.Unmarshal(raw, &loc); err != nil {
		return nil, err
	}
	if loc.URI == "" {
		return nil, nil
	}
	return []lspclient.Location{loc}, nil
}

func formatLocations(label string, locs []lspclient.Location) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%d):\n", label, len(locs))
	for _, l := range locs {
		path := uriToPath(l.URI)
		fmt.Fprintf(&sb, "  %s:%d:%d\n", path, l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// fileToURI converts an absolute path to a file:// URI.
func fileToURI(absPath string) string {
	return "file://" + filepath.ToSlash(absPath)
}

// uriToPath converts a file:// URI back to a path, unescaping percent-encoding.
func uriToPath(uri string) string {
	uri = strings.TrimPrefix(uri, "file://")
	if p, err := url.PathUnescape(uri); err == nil {
		return p
	}
	return uri
}
