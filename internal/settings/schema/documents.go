package schema

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

const idBase = "https://raw.githubusercontent.com/icehunter/conduit/main/schemas/"

// Kinds lists the schema names Document accepts, in a stable order.
var Kinds = []string{"settings", "conduit", "mcp"}

// Document returns the generated schema for one config file kind.
func Document(kind string) ([]byte, error) {
	switch kind {
	case "settings":
		b, err := Generate(reflect.TypeFor[settings.Settings](), SettingsAnnotations(), Meta{
			ID:          idBase + "settings.schema.json",
			Title:       "Conduit settings.json",
			Description: "Claude-compatible settings file: ~/.claude/settings.json, .claude/settings.json and .claude/settings.local.json. Keys conduit does not model are allowed.",
		})
		return b, err
	case "conduit":
		b, err := Generate(reflect.TypeFor[settings.ConduitConfig](), ConduitAnnotations(), Meta{
			ID:          idBase + "conduit.schema.json",
			Title:       "Conduit conduit.json",
			Description: "Conduit-owned user config at ~/.conduit/conduit.json. Keys conduit does not model are allowed.",
		})
		return b, err
	case "mcp":
		b, err := Generate(reflect.TypeFor[mcp.McpJSON](), MCPAnnotations(), Meta{
			ID:          idBase + "mcp.schema.json",
			Title:       "Conduit .mcp.json",
			Description: "MCP server definitions in .mcp.json.",
		})
		return b, err
	}
	return nil, fmt.Errorf("schema: unknown kind %q (want one of %s)", kind, strings.Join(Kinds, ", "))
}
