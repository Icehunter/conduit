//go:build !windows

package recorder

import (
	"fmt"
	"os/signal"
	"syscall"
)

func initWinch(r *Recorder) {
	signal.Notify(r.winchChan, syscall.SIGWINCH)
	go func() {
		for range r.winchChan {
			w, h := termSize()
			r.mu.Lock()
			if r.recording {
				_ = r.writeEventLocked("r", fmt.Sprintf("%dx%d", w, h))
			}
			r.mu.Unlock()
		}
	}()
}
