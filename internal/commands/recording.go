package commands

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/recorder"
)

// RegisterRecordingCommand registers the /record slash command.
//
// Usage:
//
//	/record            — toggle: start if not recording, stop if recording
//	/record start      — start recording
//	/record stop       — stop recording
//
// The recording is written to ~/.claude/recordings/<timestamp>.cast in
// asciicast v2 format, readable by asciinema play.
func RegisterRecordingCommand(r *Registry) {
	r.Register(Command{
		Name:        "record",
		Description: "Record terminal session to asciicast v2 (.cast) file",
		Handler:     recordHandler,
	})
}

func recordHandler(args string) Result {
	sub := strings.ToLower(strings.TrimSpace(args))

	switch sub {
	case "start":
		return startRecording()
	case "stop":
		return stopRecording()
	case "":
		// Toggle.
		if recorder.DefaultRecorder.IsRecording() {
			return stopRecording()
		}
		return startRecording()
	default:
		return Result{
			Type: "error",
			Text: fmt.Sprintf("Unknown /record sub-command %q. Usage: /record [start|stop]", sub),
		}
	}
}

func startRecording() Result {
	if recorder.DefaultRecorder.IsRecording() {
		return Result{
			Type: "text",
			Text: fmt.Sprintf("Already recording to %s", recorder.DefaultRecorder.FilePath()),
		}
	}

	path, err := recorder.RecordingPath()
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("recording: path error: %v", err)}
	}

	if err := recorder.DefaultRecorder.Start(path); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("recording: start error: %v", err)}
	}

	return Result{
		Type: "text",
		Text: fmt.Sprintf("Recording started. Output is being captured to:\n  %s\n\nRun /record stop (or /record) to finish.", path),
	}
}

func stopRecording() Result {
	if !recorder.DefaultRecorder.IsRecording() {
		return Result{Type: "text", Text: "Not currently recording."}
	}

	path := recorder.DefaultRecorder.FilePath()
	if err := recorder.DefaultRecorder.Stop(); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("recording: stop error: %v", err)}
	}

	return Result{
		Type: "text",
		Text: fmt.Sprintf("Recording saved to:\n  %s\n\nPlay with: asciinema play %s", path, path),
	}
}
