package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunConfigCmd(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantOutHas string
	}{
		{"settings", []string{"schema", "settings"}, "", `"Conduit settings.json"`},
		{"conduit", []string{"schema", "conduit"}, "", `"Conduit conduit.json"`},
		{"mcp", []string{"schema", "mcp"}, "", `"Conduit .mcp.json"`},
		{"default kind is conduit", []string{"schema"}, "", `"Conduit conduit.json"`},
		{"unknown kind", []string{"schema", "bogus"}, `unknown kind "bogus"`, ""},
		{"too many args", []string{"schema", "mcp", "extra"}, "too many arguments", ""},
		{"missing subcommand", nil, "missing subcommand", ""},
		{"unknown subcommand", []string{"frob"}, `unknown subcommand "frob"`, ""},
		{"help", []string{"help"}, "", "conduit config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			err := runConfigCmd(tt.args, &out, &errb)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				if errb.Len() == 0 {
					t.Error("expected usage on stderr")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), tt.wantOutHas) {
				t.Errorf("stdout missing %q", tt.wantOutHas)
			}
			if tt.args[0] == "schema" && !json.Valid(out.Bytes()) {
				t.Error("schema output is not valid JSON")
			}
		})
	}
}
