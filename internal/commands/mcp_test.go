package commands

import (
	"reflect"
	"testing"
)

func TestSplitShellArgs(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"simple", "a b c", []string{"a", "b", "c"}, false},
		{"double-quoted value", `--header "Authorization: Bearer xxx"`,
			[]string{"--header", "Authorization: Bearer xxx"}, false},
		{"single-quoted value", `--env 'KEY=hello world'`,
			[]string{"--env", "KEY=hello world"}, false},
		{"double-dash separator", "name -- npx -y server",
			[]string{"name", "--", "npx", "-y", "server"}, false},
		{"mixed quotes", `add foo "https://x.example/mcp" --transport http`,
			[]string{"add", "foo", "https://x.example/mcp", "--transport", "http"}, false},
		{"unterminated quote", `--header "broken`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := splitShellArgs(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseAddTokens(t *testing.T) {
	tests := []struct {
		name        string
		tokens      []string
		wantName    string
		wantScope   string
		wantType    string
		wantURL     string
		wantCmd     string
		wantArgs    []string
		wantHeaders map[string]string
		wantEnv     map[string]string
		wantErr     bool
	}{
		{
			name:      "http with explicit transport",
			tokens:    []string{"atlassian", "https://mcp.atlassian.com/v1/mcp", "--transport", "http"},
			wantName:  "atlassian",
			wantScope: "project",
			wantType:  "http",
			wantURL:   "https://mcp.atlassian.com/v1/mcp",
		},
		{
			name:      "http auto-detected from URL",
			tokens:    []string{"thing", "https://example.com/mcp"},
			wantName:  "thing",
			wantScope: "project",
			wantType:  "http",
			wantURL:   "https://example.com/mcp",
		},
		{
			name:      "stdio via double-dash",
			tokens:    []string{"airtable", "--", "npx", "-y", "airtable-mcp-server"},
			wantName:  "airtable",
			wantScope: "project",
			wantType:  "",
			wantCmd:   "npx",
			wantArgs:  []string{"-y", "airtable-mcp-server"},
		},
		{
			name:      "user scope override",
			tokens:    []string{"--scope", "user", "github", "https://api.example.com/mcp", "--transport", "http"},
			wantName:  "github",
			wantScope: "user",
			wantType:  "http",
			wantURL:   "https://api.example.com/mcp",
		},
		{
			name:        "header parsing",
			tokens:      []string{"x", "https://example.com/mcp", "--transport", "http", "--header", "Authorization: Bearer xyz"},
			wantName:    "x",
			wantScope:   "project",
			wantType:    "http",
			wantURL:     "https://example.com/mcp",
			wantHeaders: map[string]string{"Authorization": "Bearer xyz"},
		},
		{
			name:      "env parsing for stdio",
			tokens:    []string{"--env", "KEY=value", "x", "--", "node", "server.js"},
			wantName:  "x",
			wantScope: "project",
			wantCmd:   "node",
			wantArgs:  []string{"server.js"},
			wantEnv:   map[string]string{"KEY": "value"},
		},
		{
			name:    "missing name",
			tokens:  []string{"--scope", "user"},
			wantErr: true,
		},
		{
			name:    "header on stdio rejected",
			tokens:  []string{"x", "--header", "X: y", "--", "node"},
			wantErr: true,
		},
		{
			name:    "env on http rejected",
			tokens:  []string{"x", "https://example.com/mcp", "--transport", "http", "--env", "K=V"},
			wantErr: true,
		},
		{
			name:    "invalid --env without =",
			tokens:  []string{"--env", "BARE", "x", "--", "node"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAddTokens(tt.tokens)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.name != tt.wantName {
				t.Errorf("name = %q, want %q", got.name, tt.wantName)
			}
			if got.scope != tt.wantScope {
				t.Errorf("scope = %q, want %q", got.scope, tt.wantScope)
			}
			if got.cfg.Type != tt.wantType {
				t.Errorf("type = %q, want %q", got.cfg.Type, tt.wantType)
			}
			if got.cfg.URL != tt.wantURL {
				t.Errorf("url = %q, want %q", got.cfg.URL, tt.wantURL)
			}
			if got.cfg.Command != tt.wantCmd {
				t.Errorf("cmd = %q, want %q", got.cfg.Command, tt.wantCmd)
			}
			if !reflect.DeepEqual(got.cfg.Args, tt.wantArgs) {
				t.Errorf("args = %#v, want %#v", got.cfg.Args, tt.wantArgs)
			}
			if tt.wantHeaders != nil && !reflect.DeepEqual(got.cfg.Headers, tt.wantHeaders) {
				t.Errorf("headers = %#v, want %#v", got.cfg.Headers, tt.wantHeaders)
			}
			if tt.wantEnv != nil && !reflect.DeepEqual(got.cfg.Env, tt.wantEnv) {
				t.Errorf("env = %#v, want %#v", got.cfg.Env, tt.wantEnv)
			}
		})
	}
}
