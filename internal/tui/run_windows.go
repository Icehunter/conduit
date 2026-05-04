//go:build windows

package tui

import "os"

func initTUIWinch(_ chan os.Signal) {}
