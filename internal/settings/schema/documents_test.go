package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

func compile(t *testing.T, kind string) *jsonschema.Schema {
	t.Helper()
	raw, err := Document(kind)
	if err != nil {
		t.Fatalf("Document(%q): %v", kind, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	url := idBase + kind + ".schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatalf("AddResource: %v", err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return sch
}

func validate(sch *jsonschema.Schema, doc string) error {
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

func TestCommittedSchemasUpToDate(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(kind, func(t *testing.T) {
			want, err := Document(kind)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", kind+".schema.json"))
			if err != nil {
				t.Fatalf("read committed schema: %v (run make schema)", err)
			}
			// Windows checkouts with autocrlf turn LF into CRLF.
			got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(got, want) {
				t.Errorf("schemas/%s.schema.json is stale: run make schema", kind)
			}
		})
	}
}

func TestDocumentUnknownKind(t *testing.T) {
	if _, err := Document("nope"); err == nil || !strings.Contains(err.Error(), "settings, conduit, mcp") {
		t.Fatalf("want error listing kinds, got %v", err)
	}
}

const goodSettings = `{
  "$schema": "https://raw.githubusercontent.com/icehunter/conduit/main/schemas/settings.schema.json",
  "model": "claude-opus-5-5",
  "env": {"FOO": "bar"},
  "permissions": {
    "allow": ["Bash(git status:*)"],
    "deny": ["Read(./.env)"],
    "ask": [],
    "defaultMode": "acceptEdits",
    "additionalDirectories": ["../shared"]
  },
  "hooks": {
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi", "timeout": 5}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "http", "url": "https://example.test/hook", "headers": {"X": "y"}}]}],
    "Stop": [{"matcher": "", "hooks": [{"type": "prompt", "prompt": "check $ARGUMENTS", "model": "claude-haiku-5-5"}]}]
  },
  "enabledPlugins": {"foo@market": true},
  "claudeCodeOnlyKey": {"anything": 1}
}`

const goodConduit = `{
  "$schema": "https://raw.githubusercontent.com/icehunter/conduit/main/schemas/conduit.schema.json",
  "schemaVersion": 1,
  "model": "qwen3",
  "providers": {
    "openai-compatible.local": {
      "kind": "openai-compatible",
      "model": "qwen3",
      "credential": "local",
      "baseURL": "http://192.168.0.71:8080/v1",
      "contextWindow": 32768,
      "reasoningReplay": "auto",
      "toolCallFormat": "hermes",
      "extraBody": {"chat_template_kwargs": {"enable_thinking": false}},
      "responseHeaderTimeout": 120
    }
  },
  "roles": {"default": "openai-compatible.local"},
  "providerChains": {"default": ["openai-compatible.local"]},
  "lspServers": {"go": {"cmd": "gopls", "args": ["serve"], "env": ["GOFLAGS=-mod=mod"], "disabled": false}},
  "compaction": {"keepRecent": 5, "auto": true, "maxTriggerTokens": 200000},
  "toolOutput": {"maxLines": 2000},
  "redactSecrets": true,
  "ttsr": {"repetitionGuard": true},
  "webSearch": {"keyless": "never"},
  "enabledMcpjsonServers": ["atlassian"],
  "accounts": {"active": "a", "accounts": {"a": {"email": "a@example.test", "added_at": "2026-01-02T03:04:05Z"}}},
  "projects": {"/work/x": {"trusted": true}},
  "hooks": {"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "true"}]}]}
}`

const goodMCP = `{
  "$schema": "https://raw.githubusercontent.com/icehunter/conduit/main/schemas/mcp.schema.json",
  "mcpServers": {
    "fs": {"command": "npx", "args": ["-y", "server-fs"], "env": {"ROOT": "/"}},
    "atlassian": {"type": "http", "url": "https://mcp.atlassian.com/v1/mcp", "headers": {"A": "b"}},
    "legacy": {"type": "sse", "url": "https://example.test/sse"}
  }
}`

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		doc     string
		wantErr bool
	}{
		{"good settings", "settings", goodSettings, false},
		{"good conduit", "conduit", goodConduit, false},
		{"good mcp", "mcp", goodMCP, false},
		{"empty objects", "conduit", `{}`, false},
		{"unknown root keys allowed", "conduit", `{"somethingNew": [1,2]}`, false},

		{"CC dontAsk mode", "settings", `{"permissions":{"defaultMode":"dontAsk"}}`, false},
		{"CC mcp_tool hook", "settings", `{"hooks":{"Stop":[{"hooks":[{"type":"mcp_tool"}]}]}}`, false},
		{"empty defaultMode as saved", "settings", `{"permissions":{"defaultMode":""}}`, false},
		{"CC internal mcp transport", "mcp", `{"mcpServers":{"x":{"type":"sse-ide","url":"u"}}}`, false},
		{"mcp type any case", "mcp", `{"mcpServers":{"x":{"type":"HTTP","url":"u"}}}`, false},
		{"mcp type empty", "mcp", `{"mcpServers":{"x":{"type":"","command":"c"}}}`, false},
		{"reasoningReplay any case", "conduit", `{"providers":{"p":{"kind":"openai-compatible","reasoningReplay":"AUTO"}}}`, false},
		{"reasoningReplay empty", "conduit", `{"providers":{"p":{"kind":"openai-compatible","reasoningReplay":""}}}`, false},
		{"toolCallFormat any case", "conduit", `{"providers":{"p":{"kind":"openai-compatible","toolCallFormat":"Hermes"}}}`, false},
		{"mcp type prefix not enough", "mcp", `{"mcpServers":{"x":{"type":"httpx"}}}`, true},
		{"bad reasoningReplay", "conduit", `{"providers":{"p":{"kind":"openai-compatible","reasoningReplay":"sometimes"}}}`, true},
		{"bad toolCallFormat", "conduit", `{"providers":{"p":{"kind":"openai-compatible","toolCallFormat":"xml"}}}`, true},
		{"bad provider kind", "conduit", `{"providers":{"p":{"kind":"carrier-pigeon"}}}`, true},
		{"bad webSearch keyless", "conduit", `{"webSearch":{"keyless":"sometimes"}}`, true},
		{"bad defaultMode", "settings", `{"permissions":{"defaultMode":"yolo"}}`, true},
		{"bad hook type", "settings", `{"hooks":{"Stop":[{"hooks":[{"type":"shell"}]}]}}`, true},
		{"hooks as string", "settings", `{"hooks":"echo hi"}`, true},
		{"hooks as string in conduit", "conduit", `{"hooks":"echo hi"}`, true},
		{"allow as string", "settings", `{"permissions":{"allow":"Bash"}}`, true},
		{"compaction wrong type", "conduit", `{"compaction":{"keepRecent":"five"}}`, true},
		{"lsp args wrong type", "conduit", `{"lspServers":{"go":{"args":"serve"}}}`, true},
		{"bad mcp type", "mcp", `{"mcpServers":{"x":{"type":"carrier-pigeon","url":"u"}}}`, true},
		{"mcpServers as array", "mcp", `{"mcpServers":[]}`, true},
		{"mcp args wrong type", "mcp", `{"mcpServers":{"x":{"command":"c","args":"a"}}}`, true},
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, k := range Kinds {
		schemas[k] = compile(t, k)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(schemas[tt.kind], tt.doc)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestOwnOutputValidates marshals zero-valued config structs the way
// conduit's own saves do (nil slices as null, empty strings) and requires the
// result to validate; a schema that rejects conduit's own files is wrong.
func TestOwnOutputValidates(t *testing.T) {
	tests := []struct {
		name string
		kind string
		v    any
	}{
		{"zero Settings", "settings", settings.Settings{}},
		{"zero ConduitConfig", "conduit", settings.ConduitConfig{}},
		{"populated ConduitConfig", "conduit", settings.ConduitConfig{
			SchemaURL: "https://example.test/s.json",
			Providers: map[string]settings.ActiveProviderSettings{"p": {Kind: settings.ProviderKindMCP, Server: "s"}},
			WebSearch: &settings.WebSearchConfig{Keyless: settings.WebSearchKeylessAlways},
		}},
		{"McpJSON", "mcp", mcp.McpJSON{McpServers: map[string]mcp.ServerConfig{"a": {Command: "c"}, "b": {Type: "http", URL: "u"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatal(err)
			}
			if err := validate(compile(t, tt.kind), string(b)); err != nil {
				t.Errorf("own output rejected: %v\n%s", err, b)
			}
		})
	}
}

func TestRepoConfigFilesValidate(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	tests := []struct{ kind, file string }{
		{"settings", ".claude/settings.json"},
		{"mcp", ".mcp.json"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(root, tt.file))
			if err != nil {
				t.Skipf("not present: %v", err)
			}
			if err := validate(compile(t, tt.kind), string(b)); err != nil {
				t.Errorf("%s does not validate: %v", tt.file, err)
			}
		})
	}
}

func TestHooksIncludeUserPromptSubmit(t *testing.T) {
	for _, kind := range []string{"settings", "conduit"} {
		b, _ := Document(kind)
		if !bytes.Contains(b, []byte(`"UserPromptSubmit"`)) {
			t.Errorf("%s schema is missing UserPromptSubmit", kind)
		}
	}
}
