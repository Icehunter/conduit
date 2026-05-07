package plugins

import (
	"strings"

	"github.com/icehunter/conduit/internal/tools/agenttool"
)

// AgentRegistry maps subagent_type names to their definitions.
// It is constructed from loaded plugins at startup and implements
// agenttool.Registry so it can be passed directly to agenttool.New.
type AgentRegistry struct {
	agents []AgentDef
}

// NewAgentRegistry builds an AgentRegistry from loaded plugins.
func NewAgentRegistry(ps []*Plugin) *AgentRegistry {
	var agents []AgentDef
	for _, p := range ps {
		agents = append(agents, p.Agents...)
	}
	return &AgentRegistry{agents: agents}
}

// FindAgent implements agenttool.Registry. Accepts:
//   - "pluginName:name" (qualified)
//   - "name" (bare — first match wins)
func (r *AgentRegistry) FindAgent(name string) *agenttool.AgentDef {
	for i := range r.agents {
		if strings.EqualFold(r.agents[i].QualifiedName, name) || strings.EqualFold(r.agents[i].Name, name) {
			return toAgentDef(&r.agents[i])
		}
	}
	return nil
}

// ListAgents implements agenttool.Registry.
func (r *AgentRegistry) ListAgents() []agenttool.AgentDef {
	out := make([]agenttool.AgentDef, len(r.agents))
	for i := range r.agents {
		out[i] = *toAgentDef(&r.agents[i])
	}
	return out
}

func toAgentDef(d *AgentDef) *agenttool.AgentDef {
	return &agenttool.AgentDef{
		Name:          d.Name,
		QualifiedName: d.QualifiedName,
		Description:   d.Description,
		SystemPrompt:  d.Body,
		Model:         d.Model,
		Tools:         d.Tools,
	}
}
