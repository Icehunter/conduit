// Package lsp implements the LSPTool — lets Claude query a Language Server
// for code intelligence (definitions, references, symbols, hover, call
// hierarchy). It mirrors the Claude Code LSP tool: the wire input uses
// 1-based editor coordinates, the canonical operation names, and a
// formatted-result string placed in the tool_result content.
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	lspclient "github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/tool"
)

// managerIface is the subset of *lspclient.Manager the tool needs. It is also
// satisfied by the test stub.
type managerIface interface {
	ServerFor(ctx context.Context, filePath string) (*lspclient.Client, error)
	HasEverConnected() bool
}

// Tool is the LSP tool that exposes hover, definition, references, symbol, and
// call-hierarchy operations to the agent.
type Tool struct {
	Manager managerIface
}

// New creates a new LSPTool backed by the given Manager.
func New(mgr managerIface) *Tool {
	return &Tool{Manager: mgr}
}

// Name implements tool.Tool. The canonical tool name on the wire is "LSP"
// (Claude Code uses SB = "LSP"); there is no alias.
func (*Tool) Name() string { return "LSP" }

// Description implements tool.Tool. Mirrors Claude Code's cHo description.
func (*Tool) Description() string {
	return `Interact with Language Server Protocol (LSP) servers to get code intelligence features.

Supported operations:
- goToDefinition: Find where a symbol is defined
- findReferences: Find all references to a symbol
- hover: Get hover information (documentation, type info) for a symbol
- documentSymbol: Get all symbols (functions, classes, variables) in a document
- workspaceSymbol: Search for symbols matching a query across the entire workspace
- goToImplementation: Find where a symbol is implemented
- prepareCallHierarchy: Find call hierarchy items at a position
- incomingCalls: Find callers of the function at a position
- outgoingCalls: Find functions called by the function at a position

The filePath must be an absolute path. Coordinates are 1-based.`
}

// operations enumerates the LSP operations the tool supports. The wire names
// match Claude Code exactly.
type operations string

const (
	opHover                operations = "hover"
	opGoToDefinition       operations = "goToDefinition"
	opFindReferences       operations = "findReferences"
	opDocumentSymbol       operations = "documentSymbol"
	opWorkspaceSymbol      operations = "workspaceSymbol"
	opGoToImplementation   operations = "goToImplementation"
	opPrepareCallHierarchy operations = "prepareCallHierarchy"
	opIncomingCalls        operations = "incomingCalls"
	opOutgoingCalls        operations = "outgoingCalls"
)

// input is the wire schema for the LSP tool. Coordinates are 1-based and
// mirror Claude Code's gi input schema.
type input struct {
	FilePath  string     `json:"filePath"`
	Line      int        `json:"line"`
	Character int        `json:"character"`
	Query     string     `json:"query,omitempty"`
	Operation operations `json:"operation"`
}

// InputSchema implements tool.Tool, mirroring Claude Code's gi input schema.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"filePath":{"type":"string","description":"The absolute path to the file to query."},"line":{"type":"integer","description":"The 1-based line number of the symbol to query."},"character":{"type":"integer","description":"The 1-based character (column) number of the symbol to query."},"query":{"type":"string","description":"The symbol name to search for in the workspace."},"operation":{"type":"string","enum":["goToDefinition","findReferences","hover","documentSymbol","workspaceSymbol","goToImplementation","prepareCallHierarchy","incomingCalls","outgoingCalls"],"description":"The LSP operation to perform."}},"required":["operation","filePath"]}`)
}

// OutputSchema implements tool.OutputSchemaProvider, mirroring Claude Code's yi
// output schema. It is the envelope (operation, result, filePath, resultCount,
// fileCount); the tool_result content is the formatted `result` string only.
func (*Tool) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"operation":{"type":"string","enum":["goToDefinition","findReferences","hover","documentSymbol","workspaceSymbol","goToImplementation","prepareCallHierarchy","incomingCalls","outgoingCalls"],"description":"The LSP operation that was performed"},"result":{"type":"string","description":"The formatted result of the LSP operation"},"filePath":{"type":"string","description":"The file path the operation was performed on"},"resultCount":{"type":"integer","description":"Number of results (definitions, references, symbols)"},"fileCount":{"type":"integer","description":"Number of files containing results"}},"required":["operation","result","filePath"]}`)
}

// IsReadOnly implements tool.Tool. The LSP tool only reads code intelligence;
// it never mutates the workspace.
func (*Tool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe implements tool.Tool. Multiple LSP queries can run in
// parallel without sharing mutable state.
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Deferrable implements tool.Tool. LSP queries are not deferred.
func (*Tool) Deferrable() bool { return false }

// Enabled implements tool.Tool. The tool is only offered once a language
// server has connected at least once, mirroring Claude Code's
// isEnabled = manager.hasEverConnected(). WarmUp starts servers at session
// start so the gate is satisfied without waiting for the first tool call.
func (t *Tool) Enabled() bool { return t.Manager.HasEverConnected() }

// maxFileSize is the Claude Code gate: files larger than 10 decimal MB are not
// analysed. CC uses fi = 1e7 (10,000,000 bytes), so the limit is decimal MB,
// not MiB; the "File too large" message reports the same value via ceil(bytes/1e6).
const maxFileSize = 10_000_000

// Execute implements tool.Tool. It runs a single LSP operation and returns the
// formatted result string as a plain TextResult (IsError=false) — Claude Code
// never returns a tool-level error for the LSP tool; every failure is the
// structured envelope's result text.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if in.FilePath == "" {
		return tool.ErrorResult("missing required field: filePath"), nil
	}

	absFile, err := filepath.Abs(tool.ResolvePath(ctx, in.FilePath))
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("cannot resolve path %q: %v", in.FilePath, err)), nil
	}

	// Obtain the language server for this file before validating input.
	// Claude Code never returns a tool-level error for a missing server;
	// it returns the structured envelope's result text, so mirror that
	// with a plain TextResult.  Checking ServerFor first also means that
	// files with unknown extensions (no server) get the graceful message
	// rather than a file-existence error if the path is synthetic.
	cl, err := t.Manager.ServerFor(ctx, absFile)
	if err != nil {
		return tool.TextResult(fmt.Sprintf("No LSP server available for file type: %s", filepath.Ext(absFile))), nil
	}

	// validateInput mirrors Claude Code's create().validateInput: check the
	// file exists and is a regular file before making the LSP call.
	info, err := os.Stat(absFile)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.ErrorResult(fmt.Sprintf("File does not exist: %s", absFile)), nil
		}
		return tool.ErrorResult(fmt.Sprintf("Cannot access file: %s. %v", absFile, err)), nil
	}
	if !info.Mode().IsRegular() {
		return tool.ErrorResult(fmt.Sprintf("Path is not a file: %s", absFile)), nil
	}

	// Validate the operation is known before dispatching to the LSP.
	switch in.Operation {
	case opHover, opGoToDefinition, opFindReferences, opDocumentSymbol,
		opWorkspaceSymbol, opGoToImplementation, opPrepareCallHierarchy,
		opIncomingCalls, opOutgoingCalls:
		// known operation
	default:
		return tool.ErrorResult(fmt.Sprintf(
			"invalid input: unknown operation %q; must be one of hover|goToDefinition|findReferences|documentSymbol|workspaceSymbol|goToImplementation|prepareCallHierarchy|incomingCalls|outgoingCalls",
			in.Operation)), nil
	}

	cwd, _ := tool.Cwd(ctx)

	formatted, err := t.perform(ctx, cl, absFile, in, cwd)
	if err != nil {
		return tool.TextResult(fmt.Sprintf("Error performing %s: %v", in.Operation, err)), nil
	}

	return tool.TextResult(formatted), nil
}

// perform runs the operation against the given client after gating on file
// size and opening the document.
func (t *Tool) perform(ctx context.Context, cl *lspclient.Client, absFile string, in input, cwd string) (string, error) {
	fileURI := fileToURI(absFile)

	// File-size gate: Claude Code refuses to analyse files larger than 10MB.
	info, err := os.Stat(absFile)
	if err != nil {
		return "", err
	}
	if info.Size() > maxFileSize {
		sizeMB := int(math.Ceil(float64(info.Size()) / 1e6))
		return fmt.Sprintf("File too large for LSP analysis (%dMB exceeds 10MB limit)", sizeMB), nil
	}

	// Open the document so the server has it in its workspace.
	if err := didOpen(ctx, cl, fileURI, lspclient.LanguageIDForPath(absFile), absFile); err != nil {
		return "", err
	}
	defer func() { _ = didClose(ctx, cl, fileURI) }()

	switch in.Operation {
	case opHover:
		return t.hover(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	case opGoToDefinition:
		return t.goToDefinition(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	case opFindReferences:
		return t.findReferences(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	case opDocumentSymbol:
		return t.documentSymbols(ctx, cl, fileURI, cwd)
	case opWorkspaceSymbol:
		return t.workspaceSymbols(ctx, cl, in.Query, cwd)
	case opGoToImplementation:
		return t.goToImplementation(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	case opPrepareCallHierarchy:
		return t.prepareCallHierarchy(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	case opIncomingCalls:
		return t.incomingCalls(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	case opOutgoingCalls:
		return t.outgoingCalls(ctx, cl, fileURI, in.Line-1, in.Character-1, cwd)
	default:
		return "", fmt.Errorf("unknown operation %q; must be one of hover|goToDefinition|findReferences|documentSymbol|workspaceSymbol|goToImplementation|prepareCallHierarchy|incomingCalls|outgoingCalls", in.Operation)
	}
}

// hoverResponse captures what the formatter needs from a hover result: the raw
// contents (array/string/markup, decoded leniently) and the optional range.
type hoverResponse struct {
	Contents json.RawMessage  `json:"contents"`
	Range    *lspclient.Range `json:"range,omitempty"`
}

func (t *Tool) hover(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	params := positionParams(uri, line, char)
	raw, err := marshalRequest(params)
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "textDocument/hover", raw)
	if err != nil {
		return "", fmt.Errorf("textDocument/hover: %w", err)
	}
	var h hoverResponse
	// Lenient: mi extracts content from the raw bytes, so a lenient unmarshal
	// keeps the range without failing on an unusual contents shape.
	if !isNull(result) {
		_ = json.Unmarshal(result, &h)
	}
	return formatHover(result, h, cwd), nil
}

func formatHover(result json.RawMessage, h hoverResponse, cwd string) string {
	if isNull(result) {
		return "No hover information available. This may occur if the cursor is not on a symbol, or if the LSP server has not fully indexed the file."
	}
	text := hoverContent(h.Contents)
	if h.Range != nil {
		return fmt.Sprintf("Hover info at %d:%d:\n\n%s", h.Range.Start.Line+1, h.Range.Start.Character+1, text)
	}
	return text
}

// hoverContent mirrors Claude Code's mi: an array of parts is joined, and each
// part is a string or a {value:"..."} markup object.
func hoverContent(contents json.RawMessage) string {
	var arr []json.RawMessage
	if json.Unmarshal(contents, &arr) == nil && len(arr) > 0 {
		segs := make([]string, 0, len(arr))
		for _, seg := range arr {
			segs = append(segs, hoverSegmentValue(seg))
		}
		return strings.Join(segs, "\n\n")
	}
	return hoverSegmentValue(contents)
}

func hoverSegmentValue(seg json.RawMessage) string {
	var s string
	if json.Unmarshal(seg, &s) == nil {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(seg, &m) == nil {
		if v, ok := m["value"]; ok {
			var val string
			if json.Unmarshal(v, &val) == nil {
				return val
			}
		}
	}
	return ""
}

func (t *Tool) goToDefinition(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	raw, err := marshalRequest(positionParams(uri, line, char))
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "textDocument/definition", raw)
	if err != nil {
		return "", fmt.Errorf("textDocument/definition: %w", err)
	}
	locs, err := parseLocations(result)
	if err != nil {
		return "", err
	}
	locs = filterGitIgnored(ctx, locs, cwd)
	return formatGoToDefinition(locs, cwd), nil
}

func (t *Tool) findReferences(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	params := lspclient.ReferenceParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     positionParams(uri, line, char).Position,
		Context:      lspclient.ReferenceContext{IncludeDeclaration: true},
	}
	raw, err := marshalRequest(params)
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "textDocument/references", raw)
	if err != nil {
		return "", fmt.Errorf("textDocument/references: %w", err)
	}
	locs, err := parseLocations(result)
	if err != nil {
		return "", err
	}
	locs = filterGitIgnored(ctx, locs, cwd)
	return formatFindReferences(locs, cwd), nil
}

func (t *Tool) goToImplementation(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	raw, err := marshalRequest(positionParams(uri, line, char))
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "textDocument/implementation", raw)
	if err != nil {
		return "", fmt.Errorf("textDocument/implementation: %w", err)
	}
	locs, err := parseLocations(result)
	if err != nil {
		return "", err
	}
	locs = filterGitIgnored(ctx, locs, cwd)
	return formatGoToDefinition(locs, cwd), nil
}

// noDocumentSymbols returns the Claude Code message for an empty documentSymbol
// response.
func noDocumentSymbols() string {
	return "No symbols found in document. This may occur if the file is empty, not supported by the LSP server, or if the server has not fully indexed the file."
}

// parseFlatSymbols reports whether a documentSymbol response is the flat
// SymbolInformation form (first element carries a "location" key).
func parseFlatSymbols(result json.RawMessage) ([]lspclient.SymbolInformation, bool) {
	var elems []json.RawMessage
	if json.Unmarshal(result, &elems) != nil {
		return nil, false
	}
	if len(elems) == 0 {
		return nil, false
	}
	var first map[string]json.RawMessage
	if json.Unmarshal(elems[0], &first) != nil {
		return nil, false
	}
	if _, hasLoc := first["location"]; !hasLoc {
		return nil, false
	}
	var flat []lspclient.SymbolInformation
	if json.Unmarshal(result, &flat) == nil {
		return flat, true
	}
	return nil, false
}

func (t *Tool) documentSymbols(ctx context.Context, cl *lspclient.Client, uri string, cwd string) (string, error) {
	params := lspclient.DocumentSymbolParams{TextDocument: lspclient.TextDocumentIdentifier{URI: uri}}
	raw, err := marshalRequest(params)
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "textDocument/documentSymbol", raw)
	if err != nil {
		return "", fmt.Errorf("textDocument/documentSymbol: %w", err)
	}

	if flat, isFlat := parseFlatSymbols(result); isFlat {
		if len(flat) == 0 {
			return noDocumentSymbols(), nil
		}
		return formatWorkspaceSymbol(flat, cwd), nil
	}

	var tree []lspclient.DocumentSymbol
	if !isNull(result) {
		if err := json.Unmarshal(result, &tree); err != nil {
			return "", fmt.Errorf("documentSymbol decode: %w", err)
		}
	}
	if len(tree) == 0 {
		return noDocumentSymbols(), nil
	}
	return formatDocumentSymbolsTree(tree, cwd), nil
}

func formatDocumentSymbolsTree(tree []lspclient.DocumentSymbol, cwd string) string {
	lines := make([]string, 0, len(tree)+1)
	lines = append(lines, "Document symbols:")
	for _, s := range tree {
		lines = append(lines, crLine(s, 0)...)
	}
	return strings.Join(lines, "\n")
}

// crLine mirrors Claude Code's cr: one line per symbol, then its children.
func crLine(s lspclient.DocumentSymbol, depth int) []string {
	indent := strings.Repeat("  ", depth)
	line := fmt.Sprintf("%s%s (%s) - Line %d", indent, s.Name, s.Kind.String(), s.Range.Start.Line+1)
	if s.Detail != "" {
		line += " " + s.Detail
	}
	out := make([]string, 0, len(s.Children)+1)
	out = append(out, line)
	for _, c := range s.Children {
		out = append(out, crLine(c, depth+1)...)
	}
	return out
}

func (t *Tool) workspaceSymbols(ctx context.Context, cl *lspclient.Client, query, cwd string) (string, error) {
	params := lspclient.WorkspaceSymbolParams{Query: query}
	raw, err := marshalRequest(params)
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "workspace/symbol", raw)
	if err != nil {
		return "", fmt.Errorf("workspace/symbol: %w", err)
	}
	var syms []lspclient.SymbolInformation
	if !isNull(result) {
		if err := json.Unmarshal(result, &syms); err != nil {
			return "", fmt.Errorf("workspaceSymbol decode: %w", err)
		}
	}
	syms = filterGitIgnoredSymbols(ctx, syms, cwd)
	return formatWorkspaceSymbol(syms, cwd), nil
}

func callHierarchyPrepare(ctx context.Context, cl *lspclient.Client, uri string, line, char int) ([]lspclient.CallHierarchyItem, error) {
	params := lspclient.CallHierarchyPrepareParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     positionParams(uri, line, char).Position,
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

func (t *Tool) prepareCallHierarchy(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	items, err := callHierarchyPrepare(ctx, cl, uri, line, char)
	if err != nil {
		return "", err
	}
	return formatPrepareCallHierarchy(items, cwd), nil
}

func (t *Tool) incomingCalls(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	items, err := callHierarchyPrepare(ctx, cl, uri, line, char)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "No call hierarchy item found at this position", nil
	}
	params := lspclient.CallHierarchyIncomingCallsParams{Item: items[0]}
	raw, err := marshalRequest(params)
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "callHierarchy/incomingCalls", raw)
	if err != nil {
		return "", fmt.Errorf("callHierarchy/incomingCalls: %w", err)
	}
	var calls []lspclient.CallHierarchyIncomingCall
	if !isNull(result) {
		if err := json.Unmarshal(result, &calls); err != nil {
			return "", fmt.Errorf("incomingCalls decode: %w", err)
		}
	}
	return formatIncomingCalls(calls, cwd), nil
}

func (t *Tool) outgoingCalls(ctx context.Context, cl *lspclient.Client, uri string, line, char int, cwd string) (string, error) {
	items, err := callHierarchyPrepare(ctx, cl, uri, line, char)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "No call hierarchy item found at this position", nil
	}
	params := lspclient.CallHierarchyOutgoingCallsParams{Item: items[0]}
	raw, err := marshalRequest(params)
	if err != nil {
		return "", err
	}
	result, err := cl.Request(ctx, "callHierarchy/outgoingCalls", raw)
	if err != nil {
		return "", fmt.Errorf("callHierarchy/outgoingCalls: %w", err)
	}
	var calls []lspclient.CallHierarchyOutgoingCall
	if !isNull(result) {
		if err := json.Unmarshal(result, &calls); err != nil {
			return "", fmt.Errorf("outgoingCalls decode: %w", err)
		}
	}
	return formatOutgoingCalls(calls, cwd), nil
}

// formatGoToDefinition mirrors Claude Code's Et. A definition may be a single
// Location, a Location[] (with possible nulls), or null.
func formatGoToDefinition(locs []lspclient.Location, cwd string) string {
	empty := "No definition found. This may occur if the cursor is not on a symbol, or if the definition is in an external library not indexed by the LSP server."
	if len(locs) == 0 {
		return empty
	}
	valid := make([]lspclient.Location, 0, len(locs))
	for _, l := range locs {
		if l.URI != "" {
			valid = append(valid, l)
		}
	}
	if len(valid) == 0 {
		return empty
	}
	if len(valid) == 1 {
		return "Defined in " + formatLocation(valid[0], cwd)
	}
	var lines []string
	for _, l := range valid {
		lines = append(lines, "  "+formatLocation(l, cwd))
	}
	return fmt.Sprintf("Found %d definitions:\n%s", len(valid), strings.Join(lines, "\n"))
}

// formatFindReferences mirrors Claude Code's lr.
func formatFindReferences(locs []lspclient.Location, cwd string) string {
	empty := "No references found. This may occur if the symbol has no usages, or if the LSP server has not fully indexed the workspace."
	if len(locs) == 0 {
		return empty
	}
	valid := make([]lspclient.Location, 0, len(locs))
	for _, l := range locs {
		if l.URI != "" {
			valid = append(valid, l)
		}
	}
	if len(valid) == 0 {
		return empty
	}
	if len(valid) == 1 {
		return fmt.Sprintf("Found 1 reference:\n  %s", formatLocation(valid[0], cwd))
	}
	grouped := groupByURI(valid, cwd, uriOfLocation)
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d references across %d files:", len(valid), len(grouped)))
	for _, g := range grouped {
		lines = append(lines, "", g.path+":")
		for _, l := range g.items {
			lines = append(lines, fmt.Sprintf("  Line %d:%d", l.Range.Start.Line+1, l.Range.Start.Character+1))
		}
	}
	return strings.Join(lines, "\n")
}

// formatWorkspaceSymbol mirrors Claude Code's Ot.
func formatWorkspaceSymbol(syms []lspclient.SymbolInformation, cwd string) string {
	empty := "No symbols found in workspace. This may occur if the workspace is empty, or if the LSP server has not finished indexing the project."
	valid := make([]lspclient.SymbolInformation, 0, len(syms))
	for _, s := range syms {
		if s.Location.URI != "" {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return empty
	}
	grouped := groupByURI(valid, cwd, func(s lspclient.SymbolInformation) string { return s.Location.URI })
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d %s in workspace:", len(valid), pluralize(len(valid), "symbol")))
	for _, g := range grouped {
		lines = append(lines, "", g.path+":")
		for _, s := range g.items {
			line := fmt.Sprintf("  %s (%s) - Line %d", s.Name, s.Kind.String(), s.Location.Range.Start.Line+1)
			if s.ContainerName != "" {
				line += " in " + s.ContainerName
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// formatPrepareCallHierarchy mirrors Claude Code's pr.
func formatPrepareCallHierarchy(items []lspclient.CallHierarchyItem, cwd string) string {
	if len(items) == 0 {
		return "No call hierarchy item found at this position"
	}
	if len(items) == 1 {
		return "Call hierarchy item: " + formatCallHierarchyItem(items[0], cwd)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d call hierarchy items:", len(items)))
	for _, it := range items {
		lines = append(lines, "  "+formatCallHierarchyItem(it, cwd))
	}
	return strings.Join(lines, "\n")
}

// formatCallHierarchyItem mirrors Claude Code's ir.
func formatCallHierarchyItem(item lspclient.CallHierarchyItem, cwd string) string {
	if item.URI == "" {
		return fmt.Sprintf("%s (%s) - <unknown location>", item.Name, item.Kind.String())
	}
	line := fmt.Sprintf("%s (%s) - %s:%d", item.Name, item.Kind.String(), formatURI(item.URI, cwd), item.Range.Start.Line+1)
	if item.Detail != "" {
		line += fmt.Sprintf(" [%s]", item.Detail)
	}
	return line
}

// formatIncomingCalls mirrors Claude Code's fr.
func formatIncomingCalls(calls []lspclient.CallHierarchyIncomingCall, cwd string) string {
	if len(calls) == 0 {
		return "No incoming calls found (nothing calls this function)"
	}
	var order []string
	grouped := make(map[string][]lspclient.CallHierarchyIncomingCall)
	for _, c := range calls {
		if c.From == (lspclient.CallHierarchyItem{}) {
			continue
		}
		path := formatURI(c.From.URI, cwd)
		if _, ok := grouped[path]; !ok {
			order = append(order, path)
		}
		grouped[path] = append(grouped[path], c)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d incoming %s:", len(calls), pluralize(len(calls), "call")))
	for _, path := range order {
		lines = append(lines, "", path+":")
		for _, c := range grouped[path] {
			line := fmt.Sprintf("  %s (%s) - Line %d", c.From.Name, c.From.Kind.String(), c.From.Range.Start.Line+1)
			if len(c.FromRanges) > 0 {
				var ranges []string
				for _, r := range c.FromRanges {
					ranges = append(ranges, fmt.Sprintf("%d:%d", r.Start.Line+1, r.Start.Character+1))
				}
				line += " [calls at: " + strings.Join(ranges, ", ") + "]"
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// formatOutgoingCalls mirrors Claude Code's gr.
func formatOutgoingCalls(calls []lspclient.CallHierarchyOutgoingCall, cwd string) string {
	if len(calls) == 0 {
		return "No outgoing calls found (this function calls nothing)"
	}
	var order []string
	grouped := make(map[string][]lspclient.CallHierarchyOutgoingCall)
	for _, c := range calls {
		if c.To == (lspclient.CallHierarchyItem{}) {
			continue
		}
		path := formatURI(c.To.URI, cwd)
		if _, ok := grouped[path]; !ok {
			order = append(order, path)
		}
		grouped[path] = append(grouped[path], c)
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("Found %d outgoing %s:", len(calls), pluralize(len(calls), "call")))
	for _, path := range order {
		lines = append(lines, "", path+":")
		for _, c := range grouped[path] {
			line := fmt.Sprintf("  %s (%s) - Line %d", c.To.Name, c.To.Kind.String(), c.To.Range.Start.Line+1)
			if len(c.FromRanges) > 0 {
				var ranges []string
				for _, r := range c.FromRanges {
					ranges = append(ranges, fmt.Sprintf("%d:%d", r.Start.Line+1, r.Start.Character+1))
				}
				line += " [called from: " + strings.Join(ranges, ", ") + "]"
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// fileToURI converts an absolute filesystem path to a file:// URI.
func fileToURI(absPath string) string {
	return "file://" + filepath.ToSlash(absPath)
}

// didOpen opens the document in the server, mirroring Claude Code's didOpen.
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

// didClose closes the document in the server.
func didClose(_ context.Context, cl *lspclient.Client, uri string) error {
	params := lspclient.DidCloseTextDocumentParams{TextDocument: lspclient.TextDocumentIdentifier{URI: uri}}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return cl.Notify("textDocument/didClose", raw)
}

// marshalRequest marshals request params into JSON-RPC params.
func marshalRequest(params any) (json.RawMessage, error) {
	return json.Marshal(params)
}

// positionParams builds the common textDocument/position params.
func positionParams(uri string, line, char int) lspclient.TextDocumentPositionParams {
	return lspclient.TextDocumentPositionParams{
		TextDocument: lspclient.TextDocumentIdentifier{URI: uri},
		Position:     lspclient.Position{Line: uint32(line), Character: uint32(char)},
	}
}

// isNull reports whether a raw JSON result is JSON null or empty.
func isNull(result json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(result))
	return trimmed == "" || trimmed == "null"
}

// parseLocations decodes a definition/reference/implementation result, which is
// either a single Location, a Location[], or null. Each element is normalized
// (Claude Code's nt) so LocationLink targetUri/targetRange become uri/range.
func parseLocations(result json.RawMessage) ([]lspclient.Location, error) {
	if isNull(result) {
		return nil, nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(result, &arr) == nil && len(arr) > 0 {
		locs := make([]lspclient.Location, 0, len(arr))
		for _, seg := range arr {
			locs = append(locs, normalizeLocation(seg))
		}
		return locs, nil
	}
	return []lspclient.Location{normalizeLocation(result)}, nil
}

// normalizeLocation mirrors Claude Code's nt: a LocationLink (targetUri) is
// rewritten to a Location; anything else is left as-is.
func normalizeLocation(seg json.RawMessage) lspclient.Location {
	var loc lspclient.Location
	if json.Unmarshal(seg, &loc) == nil && loc.URI != "" {
		return loc
	}
	var link struct {
		TargetURI       string           `json:"targetUri"`
		TargetSelection *lspclient.Range `json:"targetSelectionRange"`
		TargetRange     *lspclient.Range `json:"targetRange"`
	}
	if json.Unmarshal(seg, &link) == nil && link.TargetURI != "" {
		nt := lspclient.Location{URI: link.TargetURI}
		if link.TargetSelection != nil {
			nt.Range = *link.TargetSelection
		} else if link.TargetRange != nil {
			nt.Range = *link.TargetRange
		}
		return nt
	}
	return loc
}

// formatURI mirrors Claude Code's Ie: decode a file:// URI and, when a cwd is
// given, return a shorter relative path if one exists and stays inside cwd.
func formatURI(uri, cwd string) string {
	if uri == "" {
		return "<unknown location>"
	}
	n := strings.TrimPrefix(uri, "file://")
	if len(n) >= 3 && n[0] == '/' && ((n[1] >= 'a' && n[1] <= 'z') || (n[1] >= 'A' && n[1] <= 'Z')) && n[2] == ':' {
		n = n[1:]
	}
	if decoded, err := url.PathUnescape(n); err == nil {
		n = decoded
	}
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, n); err == nil {
			relSlash := filepath.ToSlash(rel)
			if len(relSlash) < len(n) && !strings.HasPrefix(relSlash, "../../") {
				return relSlash
			}
		}
	}
	return filepath.ToSlash(n)
}

// decodeURI mirrors Claude Code's bi: strip the file:// prefix and decode, with
// no cwd/relative logic. Used by the git-ignore filter.
func decodeURI(uri string) string {
	n := strings.TrimPrefix(uri, "file://")
	if len(n) >= 3 && n[0] == '/' && ((n[1] >= 'a' && n[1] <= 'z') || (n[1] >= 'A' && n[1] <= 'Z')) && n[2] == ':' {
		n = n[1:]
	}
	if decoded, err := url.PathUnescape(n); err == nil {
		return decoded
	}
	return n
}

// formatLocation mirrors Claude Code's Je: path:line:char (1-based line/char).
func formatLocation(l lspclient.Location, cwd string) string {
	return fmt.Sprintf("%s:%d:%d", formatURI(l.URI, cwd), l.Range.Start.Line+1, l.Range.Start.Character+1)
}

func uriOfLocation(l lspclient.Location) string { return l.URI }

// pluralize mirrors Claude Code's I: singular for n==1, else word+"s".
func pluralize(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// grouped is a path bucket used by ordered grouping.
type grouped[T any] struct {
	path  string
	items []T
}

// groupByURI mirrors Claude Code's ar: bucket items by their formatted URI,
// preserving first-seen order (Go maps are unordered, so order is tracked).
func groupByURI[T any](items []T, cwd string, uriOf func(T) string) []grouped[T] {
	var order []string
	m := make(map[string][]T)
	for _, it := range items {
		p := formatURI(uriOf(it), cwd)
		if _, ok := m[p]; !ok {
			order = append(order, p)
		}
		m[p] = append(m[p], it)
	}
	out := make([]grouped[T], 0, len(order))
	for _, p := range order {
		out = append(out, grouped[T]{path: p, items: m[p]})
	}
	return out
}

// filterGitIgnoredURI mirrors Claude Code's Sr: drop items whose decoded URI is
// git-ignored, running `git check-ignore` in batches of 50 under cwd.
func filterGitIgnoredURI[T any](ctx context.Context, items []T, cwd string, uriOf func(T) string) []T {
	if len(items) == 0 {
		return items
	}
	uriToPath := make(map[string]string)
	for _, it := range items {
		if uri := uriOf(it); uri != "" {
			if _, seen := uriToPath[uri]; !seen {
				uriToPath[uri] = decodeURI(uri)
			}
		}
	}
	paths := make([]string, 0, len(uriToPath))
	for _, p := range uriToPath {
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return items
	}
	ignored := make(map[string]struct{})
	const batchSize = 50
	for i := 0; i < len(paths); i += batchSize {
		end := i + batchSize
		if end > len(paths) {
			end = len(paths)
		}
		args := append([]string{"check-ignore"}, paths[i:end]...)
		cmd := exec.CommandContext(ctx, "git", args...)
		if cwd != "" {
			cmd.Dir = cwd
		}
		out, err := cmd.Output()
		if err != nil || len(out) == 0 {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if t := strings.TrimSpace(line); t != "" {
				ignored[t] = struct{}{}
			}
		}
	}
	if len(ignored) == 0 {
		return items
	}
	kept := make([]T, 0, len(items))
	for _, it := range items {
		p := uriToPath[uriOf(it)]
		if p == "" {
			kept = append(kept, it)
			continue
		}
		if _, isIgnored := ignored[p]; !isIgnored {
			kept = append(kept, it)
		}
	}
	return kept
}

// filterGitIgnored applies the git-ignore filter to definition locations.
func filterGitIgnored(ctx context.Context, locations []lspclient.Location, cwd string) []lspclient.Location {
	return filterGitIgnoredURI(ctx, locations, cwd, uriOfLocation)
}

// filterGitIgnoredSymbols applies the git-ignore filter to workspace symbols.
func filterGitIgnoredSymbols(ctx context.Context, syms []lspclient.SymbolInformation, cwd string) []lspclient.SymbolInformation {
	return filterGitIgnoredURI(ctx, syms, cwd, func(s lspclient.SymbolInformation) string { return s.Location.URI })
}
