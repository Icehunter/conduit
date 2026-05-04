package tui

import (
	"sync"

	"github.com/icehunter/conduit/internal/permissions"
)

// LiveState holds a small set of frequently-read Model fields that
// command callbacks need to access from outside the Bubble Tea event loop.
// All methods are safe to call from any goroutine.
type LiveState struct {
	mu             sync.RWMutex
	modelName      string
	permissionMode permissions.Mode
	inputTokens    int
	outputTokens   int
	costUSD        float64
	sessionID      string
	rateLimitWarn  string
	fastMode       bool
	effortLevel    string    // "low" | "normal" | "high" | "max" | ""
	turnCosts      []float64 // per-turn cost deltas, most-recent last
	sessionFile    string    // current session JSONL path (updated on resume)
}

func (s *LiveState) SetModelName(name string) {
	s.mu.Lock()
	s.modelName = name
	s.mu.Unlock()
}

func (s *LiveState) ModelName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modelName
}

func (s *LiveState) SetPermissionMode(m permissions.Mode) {
	s.mu.Lock()
	s.permissionMode = m
	s.mu.Unlock()
}

func (s *LiveState) PermissionMode() permissions.Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.permissionMode
}

func (s *LiveState) SetTokens(input, output int, costUSD float64) {
	s.mu.Lock()
	s.inputTokens = input
	s.outputTokens = output
	s.costUSD = costUSD
	s.mu.Unlock()
}

func (s *LiveState) Tokens() (input, output int, costUSD float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inputTokens, s.outputTokens, s.costUSD
}

func (s *LiveState) SetSessionID(id string) {
	s.mu.Lock()
	s.sessionID = id
	s.mu.Unlock()
}

func (s *LiveState) SessionID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

func (s *LiveState) SetRateLimitWarning(w string) {
	s.mu.Lock()
	s.rateLimitWarn = w
	s.mu.Unlock()
}

func (s *LiveState) RateLimitWarning() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rateLimitWarn
}

func (s *LiveState) SetFastMode(on bool) {
	s.mu.Lock()
	s.fastMode = on
	s.mu.Unlock()
}

func (s *LiveState) FastMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fastMode
}

func (s *LiveState) SetEffortLevel(level string) {
	s.mu.Lock()
	s.effortLevel = level
	s.mu.Unlock()
}

func (s *LiveState) EffortLevel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.effortLevel
}

func (s *LiveState) SetSessionFile(path string) {
	s.mu.Lock()
	s.sessionFile = path
	s.mu.Unlock()
}

func (s *LiveState) SessionFile() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionFile
}

func (s *LiveState) AppendTurnCost(delta float64) {
	s.mu.Lock()
	s.turnCosts = append(s.turnCosts, delta)
	s.mu.Unlock()
}

func (s *LiveState) TurnCosts() []float64 {
	s.mu.RLock()
	out := make([]float64, len(s.turnCosts))
	copy(out, s.turnCosts)
	s.mu.RUnlock()
	return out
}

