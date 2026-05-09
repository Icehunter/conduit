package pendingedits

import (
	"sync"
	"testing"
)

func TestStage_NewEntry(t *testing.T) {
	tab := NewTable()
	if err := tab.Stage(Entry{Path: "/a", NewContent: []byte("v1"), Op: OpEdit}); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if got := tab.Len(); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	e, ok := tab.Get("/a")
	if !ok {
		t.Fatal("Get returned !ok")
	}
	if string(e.NewContent) != "v1" {
		t.Errorf("NewContent = %q", e.NewContent)
	}
	if e.StagedAt.IsZero() {
		t.Error("StagedAt not set")
	}
}

func TestStage_EmptyPathRejected(t *testing.T) {
	tab := NewTable()
	if err := tab.Stage(Entry{Path: "", NewContent: []byte("x")}); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestStage_CompositeMerge_PreservesOrigContent(t *testing.T) {
	tab := NewTable()
	first := Entry{
		Path:        "/a",
		OrigContent: []byte("disk"),
		OrigExisted: true,
		NewContent:  []byte("v1"),
		Op:          OpEdit,
		ToolName:    "Edit",
	}
	if err := tab.Stage(first); err != nil {
		t.Fatal(err)
	}
	second := Entry{
		Path:        "/a",
		OrigContent: []byte("WRONG"), // should be ignored on merge
		OrigExisted: false,           // should be ignored on merge
		NewContent:  []byte("v2"),
		Op:          OpEdit,
		ToolName:    "Edit",
	}
	if err := tab.Stage(second); err != nil {
		t.Fatal(err)
	}
	if tab.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (merge expected)", tab.Len())
	}
	e, _ := tab.Get("/a")
	if string(e.OrigContent) != "disk" {
		t.Errorf("OrigContent = %q, want disk (must survive merge)", e.OrigContent)
	}
	if !e.OrigExisted {
		t.Error("OrigExisted lost on merge")
	}
	if string(e.NewContent) != "v2" {
		t.Errorf("NewContent = %q, want v2", e.NewContent)
	}
}

func TestStage_WritePromotesEditOp(t *testing.T) {
	tab := NewTable()
	_ = tab.Stage(Entry{Path: "/a", NewContent: []byte("a"), Op: OpEdit, ToolName: "Edit"})
	_ = tab.Stage(Entry{Path: "/a", NewContent: []byte("b"), Op: OpWrite, ToolName: "Write"})
	e, _ := tab.Get("/a")
	if e.Op != OpWrite {
		t.Errorf("Op = %v, want OpWrite (Write must clobber Edit)", e.Op)
	}
	if e.ToolName != "Write" {
		t.Errorf("ToolName = %q, want Write", e.ToolName)
	}
}

func TestDrain_ReturnsSortedAndClears(t *testing.T) {
	tab := NewTable()
	_ = tab.Stage(Entry{Path: "/c", NewContent: []byte("c")})
	_ = tab.Stage(Entry{Path: "/a", NewContent: []byte("a")})
	_ = tab.Stage(Entry{Path: "/b", NewContent: []byte("b")})

	out := tab.Drain()
	if len(out) != 3 {
		t.Fatalf("Drain returned %d entries, want 3", len(out))
	}
	if out[0].Path != "/a" || out[1].Path != "/b" || out[2].Path != "/c" {
		t.Errorf("Drain not sorted: %v %v %v", out[0].Path, out[1].Path, out[2].Path)
	}
	if tab.Len() != 0 {
		t.Errorf("Len after Drain = %d, want 0", tab.Len())
	}
}

func TestDiscard(t *testing.T) {
	tab := NewTable()
	_ = tab.Stage(Entry{Path: "/a", NewContent: []byte("a")})
	if !tab.Discard("/a") {
		t.Error("Discard returned false for present entry")
	}
	if tab.Discard("/a") {
		t.Error("Discard returned true for absent entry")
	}
	if tab.Len() != 0 {
		t.Errorf("Len = %d, want 0", tab.Len())
	}
}

// TestStage_ConcurrentStageAndDrain exercises the lock under -race.
// Each goroutine stages a unique path; periodic drains shouldn't deadlock or
// drop entries.
func TestStage_ConcurrentStageAndDrain(t *testing.T) {
	tab := NewTable()
	const workers = 16
	const stagesPer = 64

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < stagesPer; i++ {
				path := pathf(id, i)
				_ = tab.Stage(Entry{
					Path:       path,
					NewContent: []byte("x"),
					Op:         OpEdit,
					ToolName:   "Edit",
				})
			}
		}(w)
	}

	// Concurrent drainer that just exercises the lock; counts are passed back
	// over a channel so the race detector sees the synchronization edge.
	doneCh := make(chan struct{})
	drainedCh := make(chan int, 1)
	go func() {
		count := 0
		for {
			select {
			case <-doneCh:
				drainedCh <- count
				return
			default:
				count += len(tab.Drain())
			}
		}
	}()

	wg.Wait()
	close(doneCh)
	drained := <-drainedCh
	drained += len(tab.Drain())
	want := workers * stagesPer
	if drained != want {
		t.Errorf("drained %d, want %d (no entry should be lost across concurrent Stage+Drain)", drained, want)
	}
}

func pathf(worker, i int) string {
	// Worker+index is unique → no collisions, so each Stage creates a fresh
	// entry. This isolates the lock-contention test from merge semantics.
	return string(rune('a'+worker)) + ":" + string(rune('A'+(i%26))) + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
