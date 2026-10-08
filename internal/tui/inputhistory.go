package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
)

const (
	// inputHistoryMax bounds both the in-memory list and the file, which is
	// rewritten down to this many entries whenever it is loaded.
	inputHistoryMax = 200
	// inputHistoryEntryMax skips pasted blobs; they are not worth recalling.
	inputHistoryEntryMax = 8 << 10
)

// inputHistoryPath is the per-project prompt history file under ~/.conduit.
func inputHistoryPath(cwd string) string {
	name := filepath.Base(session.ProjectDirInConfig(cwd, ""))
	return filepath.Join(settings.ConduitDir(), "history", name+".jsonl")
}

// inputHistoryStore persists submitted prompts. Writes go through a goroutine
// so Update never touches the disk.
type inputHistoryStore struct {
	path string
	ch   chan string
	done chan struct{}
	once sync.Once
}

// openInputHistory loads the saved history (trimming the file to
// inputHistoryMax) and starts the writer. A missing or unreadable file yields
// an empty history; persistence is best-effort.
func openInputHistory(path string) (*inputHistoryStore, []string) {
	entries, total := loadInputHistory(path)
	if total > len(entries) {
		_ = rewriteInputHistory(path, entries)
	}
	s := &inputHistoryStore{path: path, ch: make(chan string, 64), done: make(chan struct{})}
	go s.run()
	return s, entries
}

func loadInputHistory(path string) (entries []string, total int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2*inputHistoryEntryMax+1024)
	for sc.Scan() {
		var text string
		if json.Unmarshal(sc.Bytes(), &text) != nil || text == "" {
			continue
		}
		total++
		if n := len(entries); n > 0 && entries[n-1] == text {
			continue
		}
		entries = append(entries, text)
	}
	if len(entries) > inputHistoryMax {
		entries = entries[len(entries)-inputHistoryMax:]
	}
	return entries, total
}

func rewriteInputHistory(path string, entries []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		line, _ := json.Marshal(e)
		_, _ = w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *inputHistoryStore) run() {
	defer close(s.done)
	for text := range s.ch {
		if err := appendInputHistory(s.path, text); err != nil {
			fmt.Fprintf(os.Stderr, "conduit: input history: %v\n", err)
			// Keep draining so Add never blocks, but stop retrying noisily.
			for range s.ch {
			}
			return
		}
	}
}

func appendInputHistory(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	line, _ := json.Marshal(text)
	_, werr := f.Write(append(line, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// Add queues text for persistence. It never blocks: if the writer is behind,
// the entry is dropped.
func (s *inputHistoryStore) Add(text string) {
	if s == nil || text == "" || len(text) > inputHistoryEntryMax {
		return
	}
	select {
	case s.ch <- text:
	default:
	}
}

// Close flushes queued entries and stops the writer.
func (s *inputHistoryStore) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.ch) })
	<-s.done
}
