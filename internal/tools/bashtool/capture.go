package bashtool

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/truncate"
)

// Output capture limits. They are variables so tests can reach the spill and
// kill paths without producing hundreds of megabytes.
var (
	// spillThreshold is how much output stays in memory. Output up to it is
	// filtered whole by RTK; past it, the full stream goes to a spill file
	// and only the head and tail stay in memory.
	spillThreshold int64 = 1 << 20
	// outputDiskCap bounds a spill file. A command that writes more is
	// killed: this is a disk safety net, not an output limit.
	outputDiskCap int64 = 256 << 20
	// waitDelay is how long Execute waits for output after bash exits while
	// a background child still holds the pipe.
	waitDelay = 2 * time.Second
)

// headKeep and tailKeep are how much of each end of the output is kept in
// memory for the excerpt the model sees.
const (
	headKeep = MaxOutputBytes / 2
	tailKeep = MaxOutputBytes / 2
)

// outputCapture receives a command's combined stdout and stderr.
//
// Nothing below the disk cap is dropped. Output up to spillThreshold is
// buffered; once it grows past that, the buffer and every later write stream
// to a private spill file from truncate.NewFile. The first headKeep and last
// tailKeep bytes are always kept in memory, so both ends can be shown
// without reading the file back.
type outputCapture struct {
	mu        sync.Mutex
	threshold int64
	diskCap   int64
	// onKill is called once, when the output passes diskCap.
	onKill func()

	head     []byte
	tail     []byte // the most recent output; up to 2*tailKeep before trimming
	buf      []byte // the whole output, until it passes threshold
	total    int64
	newlines int64
	last     byte

	file    *os.File
	path    string
	fileErr error // why the spill file is missing or incomplete
	killed  bool
}

func newOutputCapture(onKill func()) *outputCapture {
	return &outputCapture{threshold: spillThreshold, diskCap: outputDiskCap, onKill: onKill}
}

// Write implements io.Writer. It never reports an error: output past the
// disk cap is discarded while the kill takes effect, rather than failing
// the pipe and leaving the command to die of SIGPIPE on its own schedule.
func (c *outputCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	if c.killed {
		return n, nil
	}
	if room := c.diskCap - c.total; int64(len(p)) > room {
		p = p[:max(room, 0)]
		c.killed = true
	}
	c.record(p)
	if c.killed {
		// Keep what arrived before the kill as the partial output.
		if c.file == nil && c.fileErr == nil {
			c.spill()
		}
		if c.onKill != nil {
			c.onKill()
		}
	}
	return n, nil
}

func (c *outputCapture) record(p []byte) {
	if len(p) == 0 {
		return
	}
	c.total += int64(len(p))
	c.newlines += int64(bytes.Count(p, []byte{'\n'}))
	c.last = p[len(p)-1]
	if room := headKeep - len(c.head); room > 0 {
		c.head = append(c.head, p[:min(room, len(p))]...)
	}
	c.tail = append(c.tail, p...)
	if len(c.tail) > 2*tailKeep {
		c.tail = append(c.tail[:0], c.tail[len(c.tail)-tailKeep:]...)
	}
	switch {
	case c.file != nil:
		c.writeFile(p)
	case c.fileErr != nil:
		// The spill file could not be made; only the head and tail survive.
	case c.total <= c.threshold:
		c.buf = append(c.buf, p...)
	default:
		c.buf = append(c.buf, p...)
		c.spill()
	}
}

// spill moves the buffered output into a new spill file. Later writes go
// straight to the file.
func (c *outputCapture) spill() {
	buf := c.buf
	c.buf = nil
	f, err := truncate.NewFile()
	if err != nil {
		c.fileErr = err
		return
	}
	c.file, c.path = f, f.Name()
	c.writeFile(buf)
}

func (c *outputCapture) writeFile(p []byte) {
	if _, err := c.file.Write(p); err != nil {
		c.fileErr = fmt.Errorf("write spill file: %w", err)
		_ = c.file.Close()
		_ = os.Remove(c.path) // best-effort; a partial file is not the full output
		c.file, c.path = nil, ""
	}
}

// finish closes the spill file once the command has exited.
func (c *outputCapture) finish() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return
	}
	if err := c.file.Close(); err != nil {
		c.fileErr = fmt.Errorf("close spill file: %w", err)
		_ = os.Remove(c.path) // best-effort; a partial file is not the full output
		c.path = ""
	}
	c.file = nil
}

// discard removes the spill file of a run whose output nobody will see.
func (c *outputCapture) discard() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != "" {
		_ = os.Remove(c.path) // best-effort; Cleanup sweeps leftovers by age
		c.path = ""
	}
}

// wasKilled reports whether the command was killed for passing the disk cap.
func (c *outputCapture) wasKilled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killed
}

// inMemory returns the whole output and true when it never left memory.
func (c *outputCapture) inMemory() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total > c.threshold || c.killed || c.fileErr != nil {
		return "", false
	}
	return string(c.buf), true
}

// excerpt renders output that did not stay in memory: the complete lines
// that fit the budget from each end, around a marker saying how much was
// left out. withPath names the spill file in the marker.
func (c *outputCapture) excerpt(maxBytes, maxLines int, withPath bool) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	halfLines := max(maxLines/2, 1)
	head := headLines(c.head, maxBytes/2, halfLines)
	tail := tailLines(c.tail, maxBytes/2, halfLines)

	totalLines := c.newlines
	if c.last != '\n' {
		totalLines++
	}
	omittedLines := max(totalLines-int64(lineCount(head)+lineCount(tail)), 0)
	omittedBytes := max(c.total-int64(len(head)+len(tail)), 0)

	path, err := c.path, c.fileErr
	if !withPath {
		path, err = "", nil
	}
	return ansi.Strip(head) + "\n\n" + omissionMarker(omittedLines, omittedBytes, path, err) + "\n\n" + ansi.Strip(tail)
}

// spillPath returns the spill file's path, or the error that kept the
// output from being saved in full.
func (c *outputCapture) spillPath() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.fileErr
}

// fitOutput returns text unchanged when it fits the budget. Otherwise it
// keeps the head and tail around a marker, and saves the full text to a
// spill file the marker names, so the omitted middle can be read back.
func fitOutput(text string, maxBytes, maxLines int) string {
	if len(text) <= maxBytes && lineCount(text) <= maxLines {
		return text
	}
	head, tail := headAndTail(text, maxBytes/2, max(maxLines/2, 1))
	omittedLines := max(lineCount(text)-lineCount(head)-lineCount(tail), 0)
	omittedBytes := max(len(text)-len(head)-len(tail), 0)
	path, err := truncate.Save(text)
	return ansi.Strip(head) + "\n\n" +
		omissionMarker(int64(omittedLines), int64(omittedBytes), path, err) +
		"\n\n" + ansi.Strip(tail)
}

// omissionMarker says how much output was left out and where all of it is.
func omissionMarker(lines, size int64, path string, err error) string {
	switch {
	case path != "":
		return fmt.Sprintf("... [%d lines / %d bytes omitted; full output: %s] ...", lines, size, path)
	case err != nil:
		return fmt.Sprintf("... [%d lines / %d bytes omitted; full output unavailable: %v] ...", lines, size, err)
	}
	return fmt.Sprintf("... [%d lines / %d bytes omitted] ...", lines, size)
}

// headAndTail splits text into a leading and a trailing part, each bounded
// by both the line and the byte budget.
//
// Lines are claimed from the front until the head budget runs out, then
// from the back until the tail budget does. Spending the budgets from both
// ends is what keeps the end of the output alive when it is only a few lines
// long but very wide: a plain lines[:n] / lines[len-n:] split hands every
// line to the head and leaves the tail empty.
func headAndTail(text string, halfBytes, halfLines int) (head, tail string) {
	lines := strings.Split(text, "\n")
	first, last := 0, len(lines)
	var headBytes, tailBytes int
	for first < last {
		if first < halfLines && headBytes+len(lines[first])+1 <= halfBytes {
			headBytes += len(lines[first]) + 1
			first++
			continue
		}
		if len(lines)-last < halfLines && tailBytes+len(lines[last-1])+1 <= halfBytes {
			tailBytes += len(lines[last-1]) + 1
			last--
			continue
		}
		break
	}
	head = strings.Join(lines[:first], "\n")
	tail = strings.Join(lines[last:], "\n")

	// A line wider than the budget fits in neither end. Keep as much of it
	// as the budget allows rather than returning nothing.
	if head == "" {
		head = clipHead(lines[0], halfBytes)
	}
	if tail == "" {
		tail = clipTail(lines[len(lines)-1], halfBytes)
	}
	return head, tail
}

// headLines returns the complete lines at the start of window, which is the
// beginning of the output cut off mid-line, within the budget.
func headLines(window []byte, halfBytes, halfLines int) string {
	w := window[:min(len(window), halfBytes)]
	end, lines := -1, 0
	for i, b := range w {
		if b != '\n' {
			continue
		}
		end, lines = i, lines+1
		if lines == halfLines {
			break
		}
	}
	if end < 0 {
		return clipHead(string(w), halfBytes)
	}
	return string(w[:end])
}

// tailLines returns the complete lines at the end of window, which is the
// end of the output starting mid-line, within the budget.
func tailLines(window []byte, halfBytes, halfLines int) string {
	w := bytes.TrimSuffix(window[max(len(window)-halfBytes, 0):], []byte{'\n'})
	start, lines := -1, 0
	for i := len(w) - 1; i >= 0; i-- {
		if w[i] != '\n' {
			continue
		}
		start, lines = i+1, lines+1
		if lines == halfLines {
			break
		}
	}
	if start < 0 {
		return clipTail(string(w), halfBytes)
	}
	return string(w[start:])
}

// lineCount returns the number of lines in s, where a final newline does not
// start another line.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if s[len(s)-1] != '\n' {
		n++
	}
	return n
}

// clipHead returns at most n bytes from the start of s, cut on a rune
// boundary.
func clipHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// clipTail returns at most n bytes from the end of s, cut on a rune
// boundary.
func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
