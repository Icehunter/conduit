package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"

	"github.com/icehunter/conduit/internal/tools/todowritetool"
)

// seedTodos seeds the global in-memory todo store for testing and returns a
// cleanup function that resets it.
func seedTodos(t *testing.T, todos []todowritetool.Todo) func() {
	t.Helper()
	// Use the package-internal store via the exported Execute path so we don't
	// need to reach into unexported state.
	if err := todowritetool.SetForTest(todos); err != nil {
		t.Skipf("todowritetool.SetForTest unavailable: %v", err)
	}
	return func() { _ = todowritetool.SetForTest(nil) }
}

func makeTodoModel(t *testing.T) Model {
	t.Helper()
	m := Model{width: 100, height: 40}
	m.input = textarea.New()
	return m
}

func TestTodoStripRows_EmptyReturnsZero(t *testing.T) {
	cleanup := seedTodos(t, nil)
	defer cleanup()

	m := makeTodoModel(t)
	if got := m.todoStripRows(); got != 0 {
		t.Errorf("todoStripRows() = %d, want 0 for empty list", got)
	}
}

func TestTodoStripRows_HiddenReturnsZero(t *testing.T) {
	cleanup := seedTodos(t, []todowritetool.Todo{
		{ID: "1", Content: "task", Status: todowritetool.StatusPending},
	})
	defer cleanup()

	m := makeTodoModel(t)
	m.todoStripHidden = true
	if got := m.todoStripRows(); got != 0 {
		t.Errorf("todoStripRows() = %d, want 0 when hidden", got)
	}
}

func TestTodoStripRows_ThreeTasksReturnsFive(t *testing.T) {
	cleanup := seedTodos(t, []todowritetool.Todo{
		{ID: "1", Content: "a", Status: todowritetool.StatusPending},
		{ID: "2", Content: "b", Status: todowritetool.StatusInProgress},
		{ID: "3", Content: "c", Status: todowritetool.StatusCompleted},
	})
	defer cleanup()

	m := makeTodoModel(t)
	// header(1) + rule(1) + 3 tasks = 5, below the cap of maxTodoStripRows=7.
	want := 5
	if got := m.todoStripRows(); got != want {
		t.Errorf("todoStripRows() = %d, want %d", got, want)
	}
}

func TestTodoStripRows_CapAtMax(t *testing.T) {
	todos := make([]todowritetool.Todo, 20)
	for i := range todos {
		todos[i] = todowritetool.Todo{ID: string(rune('a' + i)), Content: "task", Status: todowritetool.StatusPending}
	}
	cleanup := seedTodos(t, todos)
	defer cleanup()

	m := makeTodoModel(t)
	if got := m.todoStripRows(); got != maxTodoStripRows {
		t.Errorf("todoStripRows() = %d, want %d (cap) for 20 tasks", got, maxTodoStripRows)
	}
}

func TestRenderTodoStrip_EmptyReturnsEmpty(t *testing.T) {
	cleanup := seedTodos(t, nil)
	defer cleanup()

	m := makeTodoModel(t)
	if got := m.renderTodoStrip(); got != "" {
		t.Errorf("renderTodoStrip() = %q, want \"\" for empty list", got)
	}
}

func TestRenderTodoStrip_ShowsAllTaskContent(t *testing.T) {
	cleanup := seedTodos(t, []todowritetool.Todo{
		{ID: "1", Content: "Research auth patterns", Status: todowritetool.StatusCompleted},
		{ID: "2", Content: "Implement gate", Status: todowritetool.StatusInProgress},
		{ID: "3", Content: "Write tests", Status: todowritetool.StatusPending},
	})
	defer cleanup()

	m := makeTodoModel(t)
	out := plainText(m.renderTodoStrip())

	for _, want := range []string{
		"Research auth patterns",
		"Implement gate",
		"Write tests",
		"◆ Tasks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTodoStrip() missing %q\noutput:\n%s", want, out)
		}
	}
	// In-progress task shows "now" badge.
	if !strings.Contains(out, "now") {
		t.Errorf("renderTodoStrip() missing 'now' badge for in_progress task\noutput:\n%s", out)
	}
}

func TestRenderTodoStrip_OverflowShowsEllipsis(t *testing.T) {
	todos := make([]todowritetool.Todo, 10)
	for i := range todos {
		todos[i] = todowritetool.Todo{
			ID:      string(rune('a' + i)),
			Content: "task " + string(rune('a'+i)),
			Status:  todowritetool.StatusPending,
		}
	}
	cleanup := seedTodos(t, todos)
	defer cleanup()

	m := makeTodoModel(t)
	out := plainText(m.renderTodoStrip())
	if !strings.Contains(out, "more…") {
		t.Errorf("renderTodoStrip() should show overflow indicator for 10 tasks\noutput:\n%s", out)
	}
}

func TestTodoStatusCounts(t *testing.T) {
	todos := []todowritetool.Todo{
		{Status: todowritetool.StatusPending},
		{Status: todowritetool.StatusPending},
		{Status: todowritetool.StatusInProgress},
		{Status: todowritetool.StatusCompleted},
		{Status: todowritetool.StatusCompleted},
		{Status: todowritetool.StatusCompleted},
	}
	got := todoStatusCounts(todos)
	if got.pending != 2 {
		t.Errorf("pending = %d, want 2", got.pending)
	}
	if got.inProgress != 1 {
		t.Errorf("inProgress = %d, want 1", got.inProgress)
	}
	if got.done != 3 {
		t.Errorf("done = %d, want 3", got.done)
	}
}

func TestSortedTodos_CompletedLast(t *testing.T) {
	input := []todowritetool.Todo{
		{ID: "1", Content: "done task", Status: todowritetool.StatusCompleted},
		{ID: "2", Content: "pending task", Status: todowritetool.StatusPending},
		{ID: "3", Content: "active task", Status: todowritetool.StatusInProgress},
	}
	got := sortedTodos(input)
	wantOrder := []string{
		todowritetool.StatusInProgress,
		todowritetool.StatusPending,
		todowritetool.StatusCompleted,
	}
	for i, want := range wantOrder {
		if got[i].Status != want {
			t.Errorf("sortedTodos()[%d].Status = %q, want %q", i, got[i].Status, want)
		}
	}
	// Original slice must not be mutated.
	if input[0].Status != todowritetool.StatusCompleted {
		t.Error("sortedTodos() mutated the input slice")
	}
}

func TestRenderTodoStrip_CompletedRenderedLast(t *testing.T) {
	cleanup := seedTodos(t, []todowritetool.Todo{
		{ID: "1", Content: "done task", Status: todowritetool.StatusCompleted},
		{ID: "2", Content: "active task", Status: todowritetool.StatusInProgress},
		{ID: "3", Content: "pending task", Status: todowritetool.StatusPending},
	})
	defer cleanup()

	m := makeTodoModel(t)
	out := plainText(m.renderTodoStrip())

	activeIdx := strings.Index(out, "active task")
	pendingIdx := strings.Index(out, "pending task")
	doneIdx := strings.Index(out, "done task")

	if activeIdx < 0 || pendingIdx < 0 || doneIdx < 0 {
		t.Fatalf("renderTodoStrip() missing expected tasks\noutput:\n%s", out)
	}
	if activeIdx > pendingIdx {
		t.Errorf("in_progress task should appear before pending task")
	}
	if pendingIdx > doneIdx {
		t.Errorf("pending task should appear before completed task")
	}
}
