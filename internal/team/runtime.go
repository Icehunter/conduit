package team

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// ReservedLeadName is the fixed recipient name for the lead agent.
	// Teammates address the lead as "lead"; no member may register this name.
	ReservedLeadName = "lead"
	// InboxBufferSize is the channel buffer for every inbox (lead + teammates).
	// Non-blocking Send returns an error when the inbox is at capacity.
	InboxBufferSize = 64
)

// MessageKind classifies a team message for the receiving agent's persona.
type MessageKind string

const (
	KindMessage         MessageKind = "message"
	KindPlan            MessageKind = "plan"
	KindShutdownRequest MessageKind = "shutdown-request"
	KindShutdownApprove MessageKind = "shutdown-approve"
	KindShutdownReject  MessageKind = "shutdown-reject"
	KindCompletion      MessageKind = "completion"
	KindIdle            MessageKind = "idle"
)

// Message is the unit of inter-agent communication.
type Message struct {
	From   string
	To     string
	Text   string
	Kind   MessageKind
	SentAt time.Time
}

// PlanDecision carries the lead's verdict on a teammate's plan-approval request.
type PlanDecision struct {
	Approved bool
	Feedback string
}

// Member holds the runtime state for one registered teammate.
type Member struct {
	Name          string
	Inbox         chan Message
	CancelFn      context.CancelFunc
	PlanReply     chan PlanDecision // lead sends decision here; buffered(1)
	ShutdownReply chan bool         // teammate acks shutdown; buffered(1)
}

// Team manages the member registry and lead inbox for one agent-team session.
// Create with New(); use Default for the session-wide singleton.
type Team struct {
	mu         sync.RWMutex
	name       string
	members    map[string]*Member
	leadInbox  chan Message
	isShutdown bool
}

// Default is the session-wide team singleton.
// Reset() at session start before spawning any teammates.
var Default = New("")

// New creates a fresh Team with the given name. Suitable for testing and
// multi-team scenarios; most runtime code should use Default.
func New(name string) *Team {
	return &Team{
		name:      name,
		members:   make(map[string]*Member),
		leadInbox: make(chan Message, InboxBufferSize),
	}
}

// Reset replaces Default with a fresh team derived from sessionID.
// The previous Default is shut down before replacement.
func Reset(sessionID string) {
	prev := Default
	Default = New(SessionName(sessionID))
	prev.Shutdown()
}

// Register adds a new teammate to the team. Returns the Member handle and an
// error if the team is shut down or the name is already registered.
func (t *Team) Register(name string, cancelFn context.CancelFunc) (*Member, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isShutdown {
		return nil, fmt.Errorf("team: shut down")
	}
	if _, exists := t.members[name]; exists {
		return nil, fmt.Errorf("team: %q already registered", name)
	}
	m := &Member{
		Name:          name,
		Inbox:         make(chan Message, InboxBufferSize),
		CancelFn:      cancelFn,
		PlanReply:     make(chan PlanDecision, 1),
		ShutdownReply: make(chan bool, 1),
	}
	t.members[name] = m
	return m, nil
}

// Unregister removes a teammate. No-op for unknown names.
func (t *Team) Unregister(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.members, name)
}

// Send delivers msg to the named recipient's inbox non-blocking.
// Returns an error if the team is shut down, the recipient is unknown, or the
// inbox is full. The error message for unknown recipients lists valid names.
func (t *Team) Send(msg Message) error {
	msg.SentAt = time.Now()

	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.isShutdown {
		return fmt.Errorf("team: shut down")
	}

	var inbox chan Message
	if msg.To == ReservedLeadName {
		inbox = t.leadInbox
	} else {
		m, ok := t.members[msg.To]
		if !ok {
			return fmt.Errorf("team: unknown recipient %q; valid: %s",
				msg.To, strings.Join(t.namesLocked(), ", "))
		}
		inbox = m.Inbox
	}

	select {
	case inbox <- msg:
		return nil
	default:
		return fmt.Errorf("team: mailbox full for %q", msg.To)
	}
}

// LeadInbox returns the lead's receive-only message channel.
func (t *Team) LeadInbox() <-chan Message { return t.leadInbox }

// Names returns a sorted list of all valid recipient names (lead + members).
func (t *Team) Names() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.namesLocked()
}

// namesLocked builds the sorted names slice; caller must hold at least RLock.
func (t *Team) namesLocked() []string {
	names := make([]string, 0, len(t.members)+1)
	names = append(names, ReservedLeadName)
	for n := range t.members {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Shutdown cancels all member contexts and marks the team as shut down.
// Subsequent Register and Send calls return errors. Idempotent.
func (t *Team) Shutdown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isShutdown {
		return
	}
	t.isShutdown = true
	for _, m := range t.members {
		if m.CancelFn != nil {
			m.CancelFn()
		}
	}
}
