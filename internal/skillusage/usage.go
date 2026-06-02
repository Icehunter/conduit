// Package skillusage tracks per-skill usage metrics in a JSON store at
// ~/.conduit/skill-usage.json.  All public functions are best-effort: they log
// errors but never panic and never block callers.
package skillusage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// Record holds all metrics and metadata for a single skill.
type Record struct {
	Name       string    `json:"name"`
	Scope      string    `json:"scope"`      // "conduit-global" | "claude-global" | "project"
	CreatedBy  string    `json:"created_by"` // "agent" | "user"
	UseCount   int       `json:"use_count"`
	ViewCount  int       `json:"view_count"`
	PatchCount int       `json:"patch_count"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	State      string    `json:"state"` // "active" | "stale" | "archived"
	Pinned     bool      `json:"pinned"`
}

// Transition records a state change made by ApplyTransitions.
type Transition struct {
	Name string
	From string
	To   string
}

// storePath returns the path to the JSON store file.
func storePath() string {
	return filepath.Join(settings.ConduitDir(), "skill-usage.json")
}

// lockPath returns the path to the advisory lock file.
func lockPath() string {
	return filepath.Join(settings.ConduitDir(), ".skill-usage.lock")
}

// lockData is written to the lock file: "<pid> <unix-nano>".
func lockData() []byte {
	return fmt.Appendf(nil, "%d %d", os.Getpid(), time.Now().UnixNano())
}

// parseLockAge reads the lock file and returns how old it is.
// Returns a large duration if the lock cannot be parsed.
func parseLockAge(path string) time.Duration {
	data, err := os.ReadFile(path) //nolint:gosec // lock file path is controlled
	if err != nil {
		return time.Hour // treat as stale
	}
	parts := strings.SplitN(strings.TrimSpace(string(data)), " ", 2)
	if len(parts) != 2 {
		return time.Hour
	}
	ns, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Hour
	}
	return time.Since(time.Unix(0, ns))
}

const (
	lockStaleAge  = 30 * time.Second
	lockTimeout   = 2 * time.Second
	lockRetryWait = 20 * time.Millisecond
)

// acquireLock tries to obtain an exclusive advisory lock using O_EXCL.
// Returns a release function and nil on success.  On timeout it logs and
// returns a no-op release so callers can proceed anyway.
func acquireLock() func() {
	lp := lockPath()
	deadline := time.Now().Add(lockTimeout)

	for {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(lockData())
			_ = f.Close()
			return func() { _ = os.Remove(lp) }
		}

		// Lock file exists — check if it's stale.
		if age := parseLockAge(lp); age > lockStaleAge {
			// Steal the stale lock by overwriting it.
			if err2 := os.WriteFile(lp, lockData(), 0o600); err2 == nil {
				return func() { _ = os.Remove(lp) }
			}
		}

		if time.Now().After(deadline) {
			log.Printf("skillusage: lock timeout after %s; proceeding without lock", lockTimeout)
			return func() {}
		}

		time.Sleep(lockRetryWait)
	}
}

// loadStore reads the JSON store from disk.  A missing file returns an empty
// map (not an error).
func loadStore() (map[string]Record, error) {
	if err := os.MkdirAll(settings.ConduitDir(), 0o700); err != nil {
		return nil, fmt.Errorf("skillusage: mkdir: %w", err)
	}
	data, err := os.ReadFile(storePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Record{}, nil
		}
		return nil, fmt.Errorf("skillusage: read store: %w", err)
	}
	var store map[string]Record
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("skillusage: decode store: %w", err)
	}
	if store == nil {
		store = map[string]Record{}
	}
	return store, nil
}

// saveStore marshals store and writes it to disk.
func saveStore(store map[string]Record) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("skillusage: encode store: %w", err)
	}
	if err := os.WriteFile(storePath(), data, 0o600); err != nil {
		return fmt.Errorf("skillusage: write store: %w", err)
	}
	return nil
}

// withStore is a helper that acquires the lock, loads the store, calls fn,
// and saves if fn returns true.
func withStore(fn func(map[string]Record) bool) {
	release := acquireLock()
	defer release()

	store, err := loadStore()
	if err != nil {
		log.Printf("skillusage: load: %v", err)
		return
	}
	if fn(store) {
		if err := saveStore(store); err != nil {
			log.Printf("skillusage: save: %v", err)
		}
	}
}

// BumpUse increments UseCount, updates LastUsedAt, and ensures State=="active".
// Creates the record if it does not exist.
func BumpUse(name string) {
	withStore(func(s map[string]Record) bool {
		r := s[name]
		if r.Name == "" {
			r = Record{Name: name, CreatedAt: time.Now(), CreatedBy: "agent"}
		}
		r.UseCount++
		r.LastUsedAt = time.Now()
		r.State = "active"
		s[name] = r
		return true
	})
}

// BumpView increments ViewCount.
func BumpView(name string) {
	withStore(func(s map[string]Record) bool {
		r := s[name]
		if r.Name == "" {
			r = Record{Name: name, CreatedAt: time.Now(), CreatedBy: "agent"}
		}
		r.ViewCount++
		s[name] = r
		return true
	})
}

// BumpPatch increments PatchCount, updates LastUsedAt, and ensures State=="active".
func BumpPatch(name string) {
	withStore(func(s map[string]Record) bool {
		r := s[name]
		if r.Name == "" {
			r = Record{Name: name, CreatedAt: time.Now(), CreatedBy: "agent"}
		}
		r.PatchCount++
		r.LastUsedAt = time.Now()
		r.State = "active"
		s[name] = r
		return true
	})
}

// RecordCreate writes a new Record.  No-op if the record already exists.
func RecordCreate(name, scope string, byAgent bool) {
	withStore(func(s map[string]Record) bool {
		if _, ok := s[name]; ok {
			return false
		}
		createdBy := "user"
		if byAgent {
			createdBy = "agent"
		}
		s[name] = Record{
			Name:      name,
			Scope:     scope,
			CreatedBy: createdBy,
			CreatedAt: time.Now(),
			State:     "active",
		}
		return true
	})
}

// UpdateScope updates the Scope field on an existing record.  No-op if not found.
func UpdateScope(name, scope string) {
	withStore(func(s map[string]Record) bool {
		r, ok := s[name]
		if !ok {
			return false
		}
		r.Scope = scope
		s[name] = r
		return true
	})
}

// Pin sets Pinned=true on the named record.
func Pin(name string) {
	withStore(func(s map[string]Record) bool {
		r, ok := s[name]
		if !ok {
			return false
		}
		r.Pinned = true
		s[name] = r
		return true
	})
}

// Unpin sets Pinned=false on the named record.
func Unpin(name string) {
	withStore(func(s map[string]Record) bool {
		r, ok := s[name]
		if !ok {
			return false
		}
		r.Pinned = false
		s[name] = r
		return true
	})
}

// AgentCreatedNames returns all skill names where CreatedBy=="agent", sorted.
func AgentCreatedNames() []string {
	release := acquireLock()
	defer release()

	store, err := loadStore()
	if err != nil {
		log.Printf("skillusage: AgentCreatedNames: %v", err)
		return nil
	}
	var names []string
	for _, r := range store {
		if r.CreatedBy == "agent" {
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return names
}

// IsAgentCreated returns true if the named skill was created by an agent.
func IsAgentCreated(name string) bool {
	release := acquireLock()
	defer release()

	store, err := loadStore()
	if err != nil {
		log.Printf("skillusage: IsAgentCreated: %v", err)
		return false
	}
	r, ok := store[name]
	return ok && r.CreatedBy == "agent"
}

// All returns all records sorted by Name.
func All() []Record {
	release := acquireLock()
	defer release()

	store, err := loadStore()
	if err != nil {
		log.Printf("skillusage: All: %v", err)
		return nil
	}
	records := make([]Record, 0, len(store))
	for _, r := range store {
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Name < records[j].Name
	})
	return records
}

// ApplyTransitions runs deterministic state transitions on all records and
// returns the list of transitions made.  It does NOT move any directories on
// disk — the caller (curator) is responsible for that.
//
// Rules (applied in this order):
//   - Skip if Pinned==true or CreatedBy!="agent"
//   - "active"→"archived" or "stale"→"archived" if age > archiveDays
//   - "active"→"stale" if age > staleDays (and archiveDays not triggered)
func ApplyTransitions(now time.Time, staleDays, archiveDays int) []Transition {
	release := acquireLock()
	defer release()

	store, err := loadStore()
	if err != nil {
		log.Printf("skillusage: ApplyTransitions: %v", err)
		return nil
	}

	staleDur := time.Duration(staleDays) * 24 * time.Hour
	archiveDur := time.Duration(archiveDays) * 24 * time.Hour

	var transitions []Transition
	changed := false

	// Iterate in sorted order for determinism.
	names := make([]string, 0, len(store))
	for k := range store {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		r := store[name]
		if r.Pinned || r.CreatedBy != "agent" {
			continue
		}

		age := now.Sub(r.LastUsedAt)
		if r.LastUsedAt.IsZero() {
			age = now.Sub(r.CreatedAt)
			if r.CreatedAt.IsZero() {
				age = archiveDur + 1 // treat unknown as ancient
			}
		}

		from := r.State
		switch {
		case age > archiveDur && (r.State == "active" || r.State == "stale"):
			r.State = "archived"
		case age > staleDur && r.State == "active":
			r.State = "stale"
		default:
			continue
		}

		transitions = append(transitions, Transition{Name: name, From: from, To: r.State})
		store[name] = r
		changed = true
	}

	if changed {
		if err := saveStore(store); err != nil {
			log.Printf("skillusage: ApplyTransitions save: %v", err)
		}
	}
	return transitions
}
