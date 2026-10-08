package main

import (
	"github.com/icehunter/conduit/internal/bgjobs"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/workflow"
)

// codingHasActiveWriters conservatively blocks certification while session
// work can still mutate the repository. Teammates remain potential writers
// while registered, even when they are temporarily waiting for a message.
func codingHasActiveWriters() bool {
	if bgjobs.Default.Running() > 0 || workflow.Default.Running() > 0 || subagent.Default.Running() > 0 || len(team.Default.Names()) > 1 {
		return true
	}
	group := hooks.GetDefaultAsyncGroup()
	return group != nil && group.Running() > 0
}
