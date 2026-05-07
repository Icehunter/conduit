package subagent_test

import (
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/subagent"
)

func TestTracker_AddRemove(t *testing.T) {
	tr := &subagent.Tracker{}
	tr.Add(subagent.Entry{ID: "a", Label: "planning", Mode: permissions.ModePlan, StartedAt: time.Now()})
	tr.Add(subagent.Entry{ID: "b", Label: "memory", Mode: permissions.ModeBypassPermissions, StartedAt: time.Now()})
	if s := tr.Snapshot(); len(s) != 2 {
		t.Fatalf("want 2 entries, got %d", len(s))
	}
	tr.Remove("a")
	if s := tr.Snapshot(); len(s) != 1 || s[0].ID != "b" {
		t.Fatalf("after remove, want [b], got %v", s)
	}
}

func TestTracker_UpdateMode(t *testing.T) {
	tr := &subagent.Tracker{}
	tr.Add(subagent.Entry{ID: "x", Mode: permissions.ModeDefault, StartedAt: time.Now()})
	tr.UpdateMode("x", permissions.ModePlan)
	if s := tr.Snapshot(); s[0].Mode != permissions.ModePlan {
		t.Fatalf("want ModePlan after update, got %v", s[0].Mode)
	}
}

func TestTracker_RaceCondition(t *testing.T) {
	tr := &subagent.Tracker{}
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func(n int) {
			id := string(rune('a' + n%26))
			tr.Add(subagent.Entry{ID: id, StartedAt: time.Now()})
			tr.UpdateMode(id, permissions.ModePlan)
			tr.Remove(id)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 50; i++ {
		<-done
	}
}
