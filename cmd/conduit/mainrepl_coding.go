package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tui"
)

// configuredCodingMain preserves an explicit provider selection even when it
// is invalid; execution-client construction reports the validation error.
func configuredCodingMain(s *settings.Merged) (*settings.ActiveProviderSettings, error) {
	for _, role := range []string{settings.RoleMain, settings.RoleDefault} {
		if ref := s.Roles[role]; ref != "" {
			p, exists := s.Providers[ref]
			if !exists {
				return nil, fmt.Errorf("role %s references missing provider %s", role, ref)
			}
			return &p, nil
		}
	}
	return nil, nil
}

// prepareVerifiedEdits obtains explicit UI decisions and flushes before tests.
// A dismissed/cancelled overlay keeps its proposal staged; it never certifies
// or applies edits as a side effect of returning control to the caller.
func prepareVerifiedEdits(ctx context.Context, table *pendingedits.Table, hook *tui.DiffReviewHook, sess *session.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if table.Len() == 0 {
		return nil
	}
	if hook == nil || hook.AskReview == nil {
		return errors.New("pending edits require review before verification")
	}
	result := hook.AskReview(ctx)
	if ctx.Err() != nil || result.Cancelled || !result.ExplicitDecision {
		for _, entry := range result.Original {
			if err := table.Stage(entry); err != nil {
				return fmt.Errorf("restore pending edit: %w", err)
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("edit review was dismissed; pending edits remain unverified")
	}
	flushed := pendingedits.Flush(result.Approved)
	for i, fr := range flushed {
		if sess != nil {
			status := "flush-failed"
			if fr.Applied {
				status = "approved"
				_ = sess.AppendFileAccess("write", fr.Path)
			}
			_ = sess.AppendPendingEditResult(fr.Path, result.Approved[i].Op.String(), status, result.Approved[i].ToolName)
		}
		if !fr.Applied {
			if err := table.Stage(result.Approved[i]); err != nil {
				return fmt.Errorf("restore unapplied edit: %w", err)
			}
		}
	}
	for _, entry := range result.Requested {
		if sess != nil {
			_ = sess.AppendPendingEditResult(entry.Path, entry.Op.String(), "requested", entry.ToolName)
		}
	}
	if followup := diffReviewFollowupText(result, flushed); followup != "" {
		if hook.EnqueueFollowup != nil {
			hook.EnqueueFollowup(followup)
		}
		return errors.New("edit review requires follow-up: " + followup)
	}
	if result.Rejected {
		return errors.New("candidate edits were rejected; produce a revised completion candidate")
	}
	if failures := diffReviewFlushFailures(flushed); len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
