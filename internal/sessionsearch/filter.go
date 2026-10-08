package sessionsearch

import (
	"strings"
	"time"
)

// MaxExcludedSessions caps Filter.ExcludeSessions so a runaway list cannot
// grow the query without bound.
const MaxExcludedSessions = 20

// Filter narrows a search or browse. The zero value matches everything.
type Filter struct {
	// Project restricts results to one project slug.
	Project string
	// After is an inclusive lower bound and Before an exclusive upper bound on
	// the session date (the transcript's last-modified time).
	After, Before time.Time
	// ExcludeSessions omits these session ids from the results.
	ExcludeSessions []string
}

// clauses renders the filter as " AND ..." conditions plus bound arguments.
// idCol and projectCol name the session id and project columns in the query.
// The session date lives only in the sessions table, so date bounds go through
// a subselect on it.
func (f Filter) clauses(idCol, projectCol string) (string, []any) {
	var sb strings.Builder
	var args []any
	if f.Project != "" {
		sb.WriteString(" AND " + projectCol + " = ?")
		args = append(args, f.Project)
	}
	if !f.After.IsZero() || !f.Before.IsZero() {
		sb.WriteString(" AND " + idCol + " IN (SELECT id FROM sessions WHERE 1=1")
		if !f.After.IsZero() {
			sb.WriteString(" AND indexed_at >= ?")
			args = append(args, f.After.UnixMilli())
		}
		if !f.Before.IsZero() {
			sb.WriteString(" AND indexed_at < ?")
			args = append(args, f.Before.UnixMilli())
		}
		sb.WriteString(")")
	}
	ids := f.ExcludeSessions
	if len(ids) > MaxExcludedSessions {
		ids = ids[:MaxExcludedSessions]
	}
	if len(ids) > 0 {
		sb.WriteString(" AND " + idCol + " NOT IN (?" + strings.Repeat(",?", len(ids)-1) + ")")
		for _, id := range ids {
			args = append(args, id)
		}
	}
	return sb.String(), args
}
