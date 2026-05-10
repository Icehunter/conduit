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
				"enum": ["hover", "definition", "references", "diagnostics", "documentSymbol", "workspaceSymbol", "implementation", "callHierarchyIncoming", "callHierarchyOutgoing"],
				"description": "LSP operation to perform"
			},
			"file": {
				"type": "string",
				"description": "Absolute or relative path to the source file"
			},
			"line": {
				"type": "integer",
				"description": "Zero-based line number (required for hover, definition, references, implementation, callHierarchy*)"
			},
			"character": {
				"type": "integer",
				"description": "Zero-based character offset (required for hover, definition, references, implementation, callHierarchy*)"
			},
			"query": {
				"type": "string",
				"description": "Search query for workspaceSymbol (empty string returns all symbols)"
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
	Query     string `json:"query"`
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
	langID := lspclient.LanguageIDForPath(absFile)

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
	case "documentSymbol":
		return t.documentSymbols(ctx, cl, fileURI)
	case "workspaceSymbol":
		return t.workspaceSymbols(ctx, cl, in.Query)
	case "implementation":
		return t.implementation(ctx, cl, fileURI, in.Line, in.Character)
	case "callHierarchyIncoming":
		return t.callHierarchyIncoming(ctx, cl, fileURI, in.Line, in.Character)
	case "callHierarchyOutgoing":
		return t.callHierarchyOutgoing(ctx, cl, fileURI, in.Line, in.Character)
	default:
		return tool.ErrorResult(fmt.Sprintf("unknown operation %q; must be hover|definition|references|diagnostics|documentSymbol|workspaceSymbol|implementation|callHierarchyIncoming|callHierarchyOutgoing", in.Operation)), nil
	}
}

// hover performs textDocument/hover and returns formatted markdown.
func (t *Tool) hover(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) { //nolint:unparam
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
func (t *Tool) definition(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) { //nolint:unparam
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
func (t *Tool) references(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) { //nolint:unparam
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
func (t *Tool) diagnostics(cl *lspclient.Client, uri string) (tool.Result, error) { //nolint:unparam
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

// documentSymbols performs textDocument/documentSymbol.
func (t *Tool) documentSymbols(ctx context.Context, cl *lspclient.Client, uri string) (tool.Result, error) { //nolint:unparam
	params := lspclient.DocumentSymbolParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	result, err := cl.Request(ctx, "textDocument/documentSymbol", raw)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("textDocument/documentSymbol: %v", err)), nil
	}
	if isNull(result) {
		return tool.TextResult("No symbols found in document."), nil
	}

	// Try tree form (DocumentSymbol) first.
	var tree []lspclient.DocumentSymbol
	if err := json.Unmarshal(result, &tree); err == nil && len(tree) > 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "Document symbols:\n")
		formatDocumentSymbols(&sb, tree, 0)
		return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
	}

	// Fall back to flat SymbolInformation list.
	var flat []lspclient.SymbolInformation
	if err := json.Unmarshal(result, &flat); err != nil {
		return tool.ErrorResult(fmt.Sprintf("documentSymbol decode: %v", err)), nil
	}
	if len(flat) == 0 {
		return tool.TextResult("No symbols found in document."), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Document symbols (%d):\n", len(flat))
	for _, s := range flat {
		container := ""
		if s.ContainerName != "" {
			container = fmt.Sprintf(" [%s]", s.ContainerName)
		}
		fmt.Fprintf(&sb, "  %s %s%s %s:%d\n",
			s.Kind.String(), s.Name, container,
			uriToPath(s.Location.URI), s.Location.Range.Start.Line+1)
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// workspaceSymbols performs workspace/symbol.
func (t *Tool) workspaceSymbols(ctx context.Context, cl *lspclient.Client, query string) (tool.Result, error) { //nolint:unparam
	params := lspclient.WorkspaceSymbolParams{Query: query}
	raw, err := marshalRequest(params)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	result, err := cl.Request(ctx, "workspace/symbol", raw)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("workspace/symbol: %v", err)), nil
	}
	if isNull(result) {
		return tool.TextResult("No workspace symbols found."), nil
	}

	var syms []lspclient.SymbolInformation
	if err := json.Unmarshal(result, &syms); err != nil {
		return tool.ErrorResult(fmt.Sprintf("workspaceSymbol decode: %v", err)), nil
	}
	if len(syms) == 0 {
		return tool.TextResult("No workspace symbols found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Workspace symbols (%d):\n", len(syms))
	for _, s := range syms {
		container := ""
		if s.ContainerName != "" {
			container = fmt.Sprintf(" [%s]", s.ContainerName)
		}
		fmt.Fprintf(&sb, "  %s %s%s %s:%d\n",
			s.Kind.String(), s.Name, container,
			uriToPath(s.Location.URI), s.Location.Range.Start.Line+1)
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// implementation performs textDocument/implementation.
func (t *Tool) implementation(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) { //nolint:unparam
	params := lspclient.TextDocumentPositionParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     lspclient.Position{Line: line, Character: char},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	result, err := cl.Request(ctx, "textDocument/implementation", raw)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("textDocument/implementation: %v", err)), nil
	}
	if isNull(result) {
		return tool.TextResult("No implementation found."), nil
	}
	locs, err := parseLocations(result)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("implementation decode: %v", err)), nil
	}
	if len(locs) == 0 {
		return tool.TextResult("No implementation found."), nil
	}
	return tool.TextResult(formatLocations("Implementation", locs)), nil
}

// callHierarchyIncoming performs callHierarchy/prepare then callHierarchy/incomingCalls.
func (t *Tool) callHierarchyIncoming(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) { //nolint:unparam
	items, err := callHierarchyPrepare(ctx, cl, uri, line, char)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	if len(items) == 0 {
		return tool.TextResult("No call hierarchy item at this position."), nil
	}

	var sb strings.Builder
	for _, item := range items {
		params := lspclient.CallHierarchyIncomingCallsParams{Item: item}
		raw, err := marshalRequest(params)
		if err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		result, err := cl.Request(ctx, "callHierarchy/incomingCalls", raw)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("callHierarchy/incomingCalls: %v", err)), nil
		}
		if isNull(result) {
			continue
		}
		var calls []lspclient.CallHierarchyIncomingCall
		if err := json.Unmarshal(result, &calls); err != nil {
			return tool.ErrorResult(fmt.Sprintf("incomingCalls decode: %v", err)), nil
		}
		fmt.Fprintf(&sb, "Incoming calls to %s (%d):\n", item.Name, len(calls))
		for _, c := range calls {
			fmt.Fprintf(&sb, "  %s %s:%d\n",
				c.From.Name,
				uriToPath(c.From.URI),
				c.From.Range.Start.Line+1)
		}
	}
	if sb.Len() == 0 {
		return tool.TextResult("No incoming calls found."), nil
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// callHierarchyOutgoing performs callHierarchy/prepare then callHierarchy/outgoingCalls.
func (t *Tool) callHierarchyOutgoing(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) (tool.Result, error) { //nolint:unparam
	items, err := callHierarchyPrepare(ctx, cl, uri, line, char)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	if len(items) == 0 {
		return tool.TextResult("No call hierarchy item at this position."), nil
	}

	var sb strings.Builder
	for _, item := range items {
		params := lspclient.CallHierarchyOutgoingCallsParams{Item: item}
		raw, err := marshalRequest(params)
		if err != nil {
			return tool.ErrorResult(err.Error()), nil
		}
		result, err := cl.Request(ctx, "callHierarchy/outgoingCalls", raw)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("callHierarchy/outgoingCalls: %v", err)), nil
		}
		if isNull(result) {
			continue
		}
		var calls []lspclient.CallHierarchyOutgoingCall
		if err := json.Unmarshal(result, &calls); err != nil {
			return tool.ErrorResult(fmt.Sprintf("outgoingCalls decode: %v", err)), nil
		}
		fmt.Fprintf(&sb, "Outgoing calls from %s (%d):\n", item.Name, len(calls))
		for _, c := range calls {
			fmt.Fprintf(&sb, "  %s %s:%d\n",
				c.To.Name,
				uriToPath(c.To.URI),
				c.To.Range.Start.Line+1)
		}
	}
	if sb.Len() == 0 {
		return tool.TextResult("No outgoing calls found."), nil
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// callHierarchyPrepare sends callHierarchy/prepare and returns the items.
func callHierarchyPrepare(ctx context.Context, cl *lspclient.Client, uri string, line, char uint32) ([]lspclient.CallHierarchyItem, error) {
	params := lspclient.CallHierarchyPrepareParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     lspclient.Position{Line: line, Character: char},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return nil, err
	}
	result, err := cl.Request(ctx, "textDocument/prepareCallHierarchy", raw)
	if err != nil {
		return nil, fmt.Errorf("textDocument/prepareCallHierarchy: %w", err)
	}
	if isNull(result) {
		return nil, nil
	}
	var items []lspclient.CallHierarchyItem
	if err := json.Unmarshal(result, &items); err != nil {
		return nil, fmt.Errorf("prepareCallHierarchy decode: %w", err)
	}
	return items, nil
}

// formatDocumentSymbols renders a DocumentSymbol tree with indentation.
func formatDocumentSymbols(sb *strings.Builder, syms []lspclient.DocumentSymbol, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, s := range syms {
		detail := ""
		if s.Detail != "" {
			detail = fmt.Sprintf(" (%s)", s.Detail)
		}
		fmt.Fprintf(sb, "%s%s %s%s :%d\n",
			indent, s.Kind.String(), s.Name, detail, s.Range.Start.Line+1)
		if len(s.Children) > 0 {
			formatDocumentSymbols(sb, s.Children, depth+1)
		}
	}
}

func didOpen(_ context.Context, cl *lspclient.Client, uri, langID, absPath string) error {
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

func didClose(_ context.Context, cl *lspclient.Client, uri string) error {
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
