package tool

import "context"

type sessionKey struct{}

// WithSessionID returns a context carrying the id of the live session a tool
// call runs in. The loop sets it per tool batch, so tools see the current id
// even after the TUI switches sessions (/resume) mid-process.
func WithSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionIDFromContext returns the id set by WithSessionID, or "".
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}
