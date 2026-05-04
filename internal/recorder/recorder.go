// Package recorder implements asciinema v2 terminal recording.
//
// It wraps os.Stdout with a tee writer so that all terminal output is captured
// into a .cast file in asciicast v2 format. SIGWINCH events are captured and
// written as resize events.
//
// Usage:
//
//	err := recorder.DefaultRecorder.Start("/path/to/output.cast")
//	defer recorder.DefaultRecorder.Stop()
package recorder

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	term "github.com/charmbracelet/x/term"
)

// castHeader is the asciicast v2 JSON header (line 1 of the .cast file).
type castHeader struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Env       map[string]string `json:"env"`
}

// Recorder captures terminal output into an asciicast v2 file.
type Recorder struct {
	mu         sync.Mutex
	file       *os.File
	origStdout *os.File
	startTime  time.Time
	winchChan  chan os.Signal
	winchDone  chan struct{}
	recording  bool
}

// DefaultRecorder is the package-level recorder instance used by /record.
var DefaultRecorder = &Recorder{}

// Start begins recording to the given path.
// It wraps os.Stdout with a tee so subsequent writes are captured.
// Note: if Bubble Tea has already started writing, only output after this call
// is captured. Call before tea.NewProgram for a full session recording.
func (r *Recorder) Start(path string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("recorder: already recording to %s", r.file.Name())
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("recorder: mkdir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("recorder: create file: %w", err)
	}

	// Determine terminal dimensions.
	width, height := termSize()

	// Write asciicast v2 header.
	hdr := castHeader{
		Version:   2,
		Width:     width,
		Height:    height,
		Timestamp: time.Now().Unix(),
		Env: map[string]string{
			"SHELL": envOrDefault("SHELL", "/bin/sh"),
			"TERM":  envOrDefault("TERM", "xterm-256color"),
		},
	}
	enc, err := json.Marshal(hdr)
	if err != nil {
		f.Close()
		return fmt.Errorf("recorder: marshal header: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", enc); err != nil {
		f.Close()
		return fmt.Errorf("recorder: write header: %w", err)
	}

	r.file = f
	r.origStdout = os.Stdout
	r.startTime = time.Now()
	r.recording = true

	// Replace os.Stdout with a tee so all writes go to both the real terminal
	// and the .cast file.
	pr, pw, err := os.Pipe()
	if err != nil {
		// Fallback: don't tee, but still track events via a raw writer swap.
		// Simpler: just wrap via a tee writer on the fd level isn't possible
		// without a pipe. Use the pipe approach exclusively.
		r.file.Close()
		r.recording = false
		return fmt.Errorf("recorder: pipe: %w", err)
	}

	// pw becomes the new os.Stdout so the TUI writes into the pipe.
	// pr is drained by a goroutine that forwards to the real terminal + file.
	os.Stdout = pw
	r.winchChan = make(chan os.Signal, 4)
	r.winchDone = make(chan struct{})

	// Goroutine: drain the pipe, tee to real terminal + .cast file.
	go func() {
		defer close(r.winchDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				// Write to real terminal.
				_, _ = r.origStdout.Write(data)
				// Write event to .cast file (best-effort, under lock).
				r.mu.Lock()
				if r.recording {
					_ = r.writeEventLocked("o", string(data))
				}
				r.mu.Unlock()
			}
			if err != nil {
				// EOF or closed pipe — normal shutdown.
				break
			}
		}
		pr.Close()
	}()

	initWinch(r)
	return nil
}

// Stop finalises the recording and restores os.Stdout.
func (r *Recorder) Stop() error {
	r.mu.Lock()

	if !r.recording {
		r.mu.Unlock()
		return fmt.Errorf("recorder: not recording")
	}

	// Mark stopped so the drain goroutine stops writing events.
	r.recording = false
	pw := os.Stdout
	r.mu.Unlock()

	// Stop SIGWINCH notifications.
	signal.Stop(r.winchChan)
	close(r.winchChan)

	// Closing pw (the write-end of the pipe) causes the drain goroutine to
	// see EOF and exit cleanly.
	pw.Close()

	// Wait for drain goroutine to finish.
	<-r.winchDone

	// Restore os.Stdout.
	r.mu.Lock()
	defer r.mu.Unlock()
	os.Stdout = r.origStdout
	err := r.file.Close()
	r.file = nil
	return err
}

// IsRecording returns true if a recording is in progress.
func (r *Recorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recording
}

// FilePath returns the path of the current recording file, or empty string.
func (r *Recorder) FilePath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return ""
	}
	return r.file.Name()
}

// writeEventLocked writes an asciicast event — caller must hold r.mu.
func (r *Recorder) writeEventLocked(eventType, data string) error {
	if r.file == nil {
		return nil
	}
	elapsed := time.Since(r.startTime).Seconds()
	// Encode as JSON array: [elapsed, type, data]
	line, err := json.Marshal([]interface{}{elapsed, eventType, data})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(r.file, "%s\n", line)
	return err
}

// RecordingPath returns the default path for a new recording using the
// current time as a stable, unique suffix.
func RecordingPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	claudeHome := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
	name := ts + ".cast"
	return filepath.Join(claudeHome, "recordings", name), nil
}

// termSize returns the current terminal width and height, falling back to
// COLUMNS/LINES env vars and then to 80x24.
func termSize() (width, height int) {
	w, h, err := term.GetSize(os.Stdout.Fd())
	if err == nil && w > 0 && h > 0 {
		return w, h
	}
	// Fallback: environment variables.
	if v := os.Getenv("COLUMNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			width = n
		}
	}
	if v := os.Getenv("LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			height = n
		}
	}
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	return width, height
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
