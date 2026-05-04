//go:build !windows

package tui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func initTUIWinch(winch chan os.Signal) {
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			fmt.Fprint(os.Stdout, clearScreen)
		}
	}()
}
