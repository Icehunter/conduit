package main

import (
	"context"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
)

func TestCodingHasActiveWriters(t *testing.T) {
	if codingHasActiveWriters() {
		t.Fatal("unexpected active writer at start")
	}
	subagent.Default.Add(subagent.Entry{ID: "coding-writer-test", Background: true})
	if !codingHasActiveWriters() {
		t.Error("hidden background agent was ignored")
	}
	subagent.Default.Remove("coding-writer-test")
	if _, err := team.Default.Register("coding-writer-test", nil); err != nil {
		t.Fatal(err)
	}
	if !codingHasActiveWriters() {
		t.Error("registered teammate was ignored")
	}
	team.Default.Unregister("coding-writer-test")
	original := hooks.GetDefaultAsyncGroup()
	grp := hooks.NewAsyncGroup(context.Background())
	hooks.SetDefaultAsyncGroup(grp)
	t.Cleanup(func() { grp.Shutdown(time.Second); hooks.SetDefaultAsyncGroup(original) })
	grp.Go(func(ctx context.Context) { <-ctx.Done() })
	if !codingHasActiveWriters() {
		t.Error("async hook was ignored")
	}
	grp.Shutdown(time.Second)
	if codingHasActiveWriters() {
		t.Fatal("drained writers still block completion")
	}
}
