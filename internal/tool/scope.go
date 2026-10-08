package tool

import "context"

type scopeKey struct{}

// WithScope returns a context carrying the id of the agent a tool call runs
// for. Tools that keep per-agent state (the REPL's interpreter) key it by this
// id so a sub-agent does not share the parent's. The root agent has no scope.
func WithScope(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, scopeKey{}, id)
}

// ScopeFromContext returns the id set by WithScope, or "" for the root agent.
func ScopeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(scopeKey{}).(string)
	return id
}

// ScopeReleaser is implemented by tools that hold per-scope state to free when
// the agent that owned the scope ends.
type ScopeReleaser interface {
	ReleaseScope(id string)
}

// ReleaseScope tells every visible tool holding per-scope state to drop the
// state of scope id. Safe to call when nothing holds any.
func (r *Registry) ReleaseScope(id string) {
	if id == "" {
		return
	}
	for _, t := range r.All() {
		if sr, ok := t.(ScopeReleaser); ok {
			sr.ReleaseScope(id)
		}
	}
}
