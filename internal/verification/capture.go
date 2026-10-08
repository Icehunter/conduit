package verification

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/rtk"
)

// capture hashes all bytes but retains only bounded head/tail excerpts in memory.
// It never writes raw process output to disk or RTK's recovery store.
type capture struct {
	mu           sync.Mutex
	hash         hash.Hash
	head, tail   []byte
	limit, total int
}

func newCapture(limit int) *capture { return &capture{hash: sha256.New(), limit: limit} }

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.hash.Write(p)
	c.total += len(p)
	remaining := c.limit/2 - len(c.head)
	if remaining > 0 {
		c.head = append(c.head, p[:min(remaining, len(p))]...)
	}
	n := c.limit - c.limit/2
	if len(p) >= n {
		c.tail = append(c.tail[:0], p[len(p)-n:]...)
	} else {
		if extra := len(c.tail) + len(p) - n; extra > 0 {
			c.tail = c.tail[extra:]
		}
		c.tail = append(c.tail, p...)
	}
	return len(p), nil
}

func (c *capture) result() (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var text string
	truncated := c.total > c.limit
	if !truncated {
		if c.total <= len(c.head) {
			text = string(c.head)
		} else {
			text = string(c.head) + string(c.tail[len(c.tail)-(c.total-len(c.head)):])
		}
	} else {
		// Drop cut lines, including partial credentials at either cut boundary.
		head, tail := string(c.head), string(c.tail)
		if i := strings.LastIndexByte(head, '\n'); i >= 0 {
			head = head[:i]
		} else {
			head = ""
		}
		if i := strings.IndexByte(tail, '\n'); i >= 0 {
			tail = tail[i+1:]
		} else {
			tail = ""
		}
		text = head + "\n[output truncated]\n" + tail
	}
	text = strings.ToValidUTF8(text, "")
	text, _ = rtk.RedactSecrets(text)
	return text, hex.EncodeToString(c.hash.Sum(nil)), truncated
}
