package tasktool

import (
	"sync"
	"testing"
)

// ─── Assignee field ───────────────────────────────────────────────────────────

func TestStore_Assignee_StoredOnCreate(t *testing.T) {
	s := newStore()
	task := s.Create("work", "desc", "Working", nil)
	task.Assignee = "alice"
	if task.Assignee != "alice" {
		t.Error("Assignee should be settable")
	}
}

// ─── Dependencies ─────────────────────────────────────────────────────────────

func TestStore_Dependencies_StoredOnCreate(t *testing.T) {
	s := newStore()
	a := s.Create("a", "", "", nil)
	b := s.Create("b", "", "", nil)
	b.Dependencies = []string{a.ID}
	if len(b.Dependencies) != 1 || b.Dependencies[0] != a.ID {
		t.Error("Dependencies should be settable")
	}
}

// ─── Claim ────────────────────────────────────────────────────────────────────

func TestStore_Claim_HappyPath(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	if err := s.Claim(task.ID, "alice"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	got, _ := s.Get(task.ID)
	if got.Status != StatusInProgress {
		t.Errorf("status = %q, want %q", got.Status, StatusInProgress)
	}
	if got.Assignee != "alice" {
		t.Errorf("assignee = %q, want %q", got.Assignee, "alice")
	}
}

func TestStore_Claim_UnknownTask(t *testing.T) {
	s := newStore()
	if err := s.Claim("no-such-task", "alice"); err == nil {
		t.Error("Claim unknown task should error")
	}
}

func TestStore_Claim_AlreadyInProgress(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	if err := s.Claim(task.ID, "alice"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if err := s.Claim(task.ID, "bob"); err == nil {
		t.Error("second Claim on in-progress task should error")
	}
}

func TestStore_Claim_AlreadyCompleted(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	_ = s.Claim(task.ID, "alice")
	_, _ = s.Complete(task.ID)
	if err := s.Claim(task.ID, "bob"); err == nil {
		t.Error("Claim on completed task should error")
	}
}

func TestStore_Claim_UnmetDependency(t *testing.T) {
	s := newStore()
	a := s.Create("dep", "", "", nil)
	b := s.Create("task", "", "", nil)
	b.Dependencies = []string{a.ID}
	// a is still pending → b cannot be claimed
	if err := s.Claim(b.ID, "alice"); err == nil {
		t.Error("Claim with unmet dependency should error")
	}
}

func TestStore_Claim_AfterDepCompleted(t *testing.T) {
	s := newStore()
	a := s.Create("dep", "", "", nil)
	b := s.Create("task", "", "", nil)
	b.Dependencies = []string{a.ID}
	_ = s.Claim(a.ID, "alice")
	_, _ = s.Complete(a.ID)
	if err := s.Claim(b.ID, "bob"); err != nil {
		t.Errorf("Claim after dep completed should succeed: %v", err)
	}
}

func TestStore_Claim_WrongAssignee(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	task.Assignee = "alice" // pre-assigned
	if err := s.Claim(task.ID, "bob"); err == nil {
		t.Error("Claim with wrong assignee should error")
	}
}

func TestStore_Claim_SameAssignee(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	task.Assignee = "alice"
	if err := s.Claim(task.ID, "alice"); err != nil {
		t.Errorf("Claim by pre-assigned assignee should succeed: %v", err)
	}
}

// Atomicity: two goroutines race to claim; exactly one must succeed.
func TestStore_Claim_Atomicity(t *testing.T) {
	s := newStore()
	task := s.Create("race-task", "", "", nil)

	const racers = 20
	results := make(chan error, racers)
	var wg sync.WaitGroup
	for range racers {
		wg.Go(func() {
			results <- s.Claim(task.ID, "racer")
		})
	}
	wg.Wait()
	close(results)

	var successes int
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("exactly one Claim should succeed; got %d successes", successes)
	}
}

// ─── NextClaimable ────────────────────────────────────────────────────────────

func TestStore_NextClaimable_EmptyStore(t *testing.T) {
	s := newStore()
	if s.NextClaimable("alice") != nil {
		t.Error("NextClaimable on empty store should return nil")
	}
}

func TestStore_NextClaimable_FindsUnblocked(t *testing.T) {
	s := newStore()
	task := s.Create("work", "", "", nil)
	got := s.NextClaimable("alice")
	if got == nil || got.ID != task.ID {
		t.Errorf("NextClaimable should find %q, got %v", task.ID, got)
	}
}

func TestStore_NextClaimable_SkipsBlocked(t *testing.T) {
	s := newStore()
	dep := s.Create("dep", "", "", nil)
	blocked := s.Create("blocked", "", "", nil)
	blocked.Dependencies = []string{dep.ID}
	if got := s.NextClaimable("alice"); got != nil && got.ID == blocked.ID {
		t.Error("NextClaimable should skip tasks with unmet deps")
	}
}

func TestStore_NextClaimable_SkipsAlreadyClaimed(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	_ = s.Claim(task.ID, "bob")
	if got := s.NextClaimable("alice"); got != nil {
		t.Errorf("NextClaimable should skip in-progress task, got %v", got.ID)
	}
}

func TestStore_NextClaimable_ReturnsUnblockedAfterDepDone(t *testing.T) {
	s := newStore()
	dep := s.Create("dep", "", "", nil)
	child := s.Create("child", "", "", nil)
	child.Dependencies = []string{dep.ID}
	_ = s.Claim(dep.ID, "alice")
	_, _ = s.Complete(dep.ID)
	got := s.NextClaimable("bob")
	if got == nil || got.ID != child.ID {
		t.Errorf("NextClaimable should return %q after dep done, got %v", child.ID, got)
	}
}

func TestStore_NextClaimable_SkipsAssignedToOther(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	task.Assignee = "alice"
	if got := s.NextClaimable("bob"); got != nil {
		t.Errorf("NextClaimable should skip tasks pre-assigned to others, got %v", got.ID)
	}
}

func TestStore_NextClaimable_FindsOwnPreAssigned(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	task.Assignee = "alice"
	if got := s.NextClaimable("alice"); got == nil || got.ID != task.ID {
		t.Error("NextClaimable should find own pre-assigned task")
	}
}

// ─── Complete ─────────────────────────────────────────────────────────────────

func TestStore_Complete_HappyPath(t *testing.T) {
	s := newStore()
	task := s.Create("task", "", "", nil)
	_ = s.Claim(task.ID, "alice")
	unblocked, err := s.Complete(task.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(unblocked) != 0 {
		t.Errorf("no dependents; want 0 unblocked, got %d", len(unblocked))
	}
	got, _ := s.Get(task.ID)
	if got.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", got.Status)
	}
}

func TestStore_Complete_UnknownTask(t *testing.T) {
	s := newStore()
	if _, err := s.Complete("no-such-task"); err == nil {
		t.Error("Complete unknown task should error")
	}
}

func TestStore_Complete_UnblocksDependent(t *testing.T) {
	s := newStore()
	a := s.Create("a", "", "", nil)
	b := s.Create("b", "", "", nil)
	b.Dependencies = []string{a.ID}

	_ = s.Claim(a.ID, "alice")
	unblocked, err := s.Complete(a.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(unblocked) != 1 || unblocked[0].ID != b.ID {
		t.Errorf("Complete should unblock b; got %v", unblocked)
	}
}

func TestStore_Complete_NotFullyUnblocked(t *testing.T) {
	s := newStore()
	a := s.Create("a", "", "", nil)
	c := s.Create("c", "", "", nil)
	b := s.Create("b", "", "", nil)
	b.Dependencies = []string{a.ID, c.ID}

	_ = s.Claim(a.ID, "alice")
	unblocked, _ := s.Complete(a.ID)
	// c is still pending → b not unblocked yet
	for _, u := range unblocked {
		if u.ID == b.ID {
			t.Error("b should not be unblocked while c is still pending")
		}
	}
}

func TestStore_Complete_MultipleDependents(t *testing.T) {
	s := newStore()
	a := s.Create("a", "", "", nil)
	b := s.Create("b", "", "", nil)
	c := s.Create("c", "", "", nil)
	b.Dependencies = []string{a.ID}
	c.Dependencies = []string{a.ID}

	_ = s.Claim(a.ID, "alice")
	unblocked, _ := s.Complete(a.ID)
	if len(unblocked) != 2 {
		t.Errorf("want 2 unblocked, got %d: %v", len(unblocked), unblocked)
	}
}

func TestStore_Complete_ChainUnblocks(t *testing.T) {
	s := newStore()
	a := s.Create("a", "", "", nil)
	b := s.Create("b", "", "", nil)
	c := s.Create("c", "", "", nil)
	b.Dependencies = []string{a.ID}
	c.Dependencies = []string{b.ID}

	_ = s.Claim(a.ID, "alice")
	unblocked, _ := s.Complete(a.ID)
	// c depends on b (not a), so only b should be unblocked.
	if len(unblocked) != 1 || unblocked[0].ID != b.ID {
		t.Errorf("only b should be unblocked; got %v", unblocked)
	}
}

// ─── Callbacks ────────────────────────────────────────────────────────────────

func TestStore_OnCreated_Fires(t *testing.T) {
	s := newStore()
	var fired []string
	s.OnCreated = func(task *Task) { fired = append(fired, task.ID) }

	s.Create("t1", "", "", nil)
	s.Create("t2", "", "", nil)
	if len(fired) != 2 {
		t.Errorf("OnCreated fired %d times, want 2", len(fired))
	}
}

func TestStore_OnCompleted_Fires(t *testing.T) {
	s := newStore()
	var fired []string
	s.OnCompleted = func(task *Task) { fired = append(fired, task.ID) }

	task := s.Create("t1", "", "", nil)
	_ = s.Claim(task.ID, "alice")
	_, _ = s.Complete(task.ID)
	if len(fired) != 1 || fired[0] != task.ID {
		t.Errorf("OnCompleted fired %v, want [%s]", fired, task.ID)
	}
}

func TestStore_OnCreated_NotFiredOnComplete(t *testing.T) {
	s := newStore()
	var created int
	s.OnCreated = func(*Task) { created++ }
	task := s.Create("t", "", "", nil)
	_ = s.Claim(task.ID, "x")
	_, _ = s.Complete(task.ID)
	if created != 1 {
		t.Errorf("OnCreated should fire exactly once; fired %d", created)
	}
}

// Callback must not deadlock if it calls a store method (fired after lock released).
func TestStore_CallbackNoDeadlock(t *testing.T) {
	s := newStore()
	done := make(chan struct{})
	s.OnCreated = func(*Task) {
		// Calling List() from inside the callback must not deadlock.
		_ = s.List()
		close(done)
	}
	s.Create("t", "", "", nil)
	select {
	case <-done:
	default:
		t.Error("callback deadlocked")
	}
}

func TestStore_OnCompleted_CallbackNoDeadlock(t *testing.T) {
	s := newStore()
	done := make(chan struct{})
	s.OnCompleted = func(*Task) {
		_ = s.List()
		close(done)
	}
	task := s.Create("t", "", "", nil)
	_ = s.Claim(task.ID, "x")
	_, _ = s.Complete(task.ID)
	select {
	case <-done:
	default:
		t.Error("OnCompleted callback deadlocked")
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func newStore() *Store {
	return &Store{tasks: make(map[string]*Task), nextID: 1}
}
