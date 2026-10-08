package localimplementtool

import (
	"encoding/json"
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

func TestResolveConfigTraceOnlyAdvertisedBoolean(t *testing.T) {
	for _, tc := range []struct {
		name, property string
		want           bool
	}{
		{"boolean", `{"type":"boolean"}`, true},
		{"string", `{"type":"string"}`, false},
		{"untyped", `{}`, false},
		{"nullable", `{"type":["boolean","null"]}`, false},
		{"invalid", `true`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{"coder": {Name: "coder", Status: mcp.StatusConnected, Tools: []mcp.ToolDef{{Name: defaultImplementTool, InputSchema: json.RawMessage(`{"properties":{"include_trace":` + tc.property + `}}`)}}}})
			for _, p := range []*settings.ActiveProviderSettings{nil, {Kind: settings.ProviderKindMCP, Server: "coder"}} {
				got, ok := ResolveConfig(mgr, p)
				if !ok || got.SupportsTrace != tc.want {
					t.Fatalf("config=%+v ok=%v", got, ok)
				}
			}
		})
	}
	if toolSupportsTrace(&mcp.ConnectedServer{Tools: []mcp.ToolDef{implementToolDef()}}, defaultImplementTool) {
		t.Fatal("legacy advertised trace")
	}
}
