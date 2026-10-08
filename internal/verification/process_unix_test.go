//go:build !windows

package verification

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunnerDoesNotLeaveBackgroundChild(t *testing.T) {
	r, err := NewRunner(fixtureRepo(t), testPolicy("sleep 30 >/dev/null 2>&1 & printf '%s\\n' \"$!\""), Options{Authorize: allow})
	if err != nil {
		t.Fatal(err)
	}
	report, err := r.Run(context.Background())
	if err != nil || report.Status != Passed {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(report.Checks[0].Output))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Kill(); _ = process.Release() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		output, psErr := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if ctx.Err() != nil {
			t.Fatal("check returned while its background child remained alive")
		}
		// A zombie has exited and awaits the OS reaper; it cannot write files.
		if psErr != nil || strings.HasPrefix(strings.TrimSpace(string(output)), "Z") {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("check returned while its background child remained alive")
		}
	}
}
