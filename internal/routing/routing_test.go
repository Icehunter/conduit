package routing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func request(id string) Request {
	return Request{SessionID: id, Role: "implement", PoolID: "gpu", Members: []Member{{ProviderRef: "one", Capacity: 1}, {ProviderRef: "two", Capacity: 1}}, TTL: time.Minute}
}

func TestAcquireStickyAcrossRestartAndCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.db")
	s := openTestStore(t, path)
	ctx := context.Background()
	first, err := s.Acquire(ctx, request("a"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Acquire(ctx, request("b"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderRef == other.ProviderRef {
		t.Fatal("new sessions share full endpoint")
	}
	if _, err := s.Acquire(ctx, request("a")); !errors.Is(err, ErrBusy) {
		t.Fatalf("sticky busy err=%v", err)
	}
	if err := s.Release(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	again := openTestStore(t, path)
	next, err := again.Acquire(ctx, request("a"))
	if err != nil {
		t.Fatal(err)
	}
	if next.ProviderRef != first.ProviderRef {
		t.Fatal("lost durable affinity")
	}
	changed := request("a")
	changed.Members = []Member{{ProviderRef: other.ProviderRef, Capacity: 2}}
	if _, err := again.Acquire(ctx, changed); !errors.Is(err, ErrAssignmentUnavailable) {
		t.Fatalf("missing sticky member err=%v", err)
	}
}

func TestExpiredLeaseReleasesCapacityKeepsAffinity(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "routing.db"))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	req := request("a")
	req.Members = req.Members[:1]
	lease, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := s.Renew(context.Background(), lease.ID, time.Minute); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("renew expired err=%v", err)
	}
	next, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if next.ProviderRef != lease.ProviderRef {
		t.Fatal("expired lease lost binding")
	}
}

func TestIndependentStoresReserveAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.db")
	a, b := openTestStore(t, path), openTestStore(t, path)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := request(string(rune('a' + i)))
			req.Members = req.Members[:1]
			_, err := s.Acquire(context.Background(), req)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, busy := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrBusy) {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || busy != 1 {
		t.Fatalf("success=%d busy=%d", success, busy)
	}
}

func TestAcquireCancelledAndNormalizedLoad(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "routing.db"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Acquire(ctx, request("cancel")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	req := request("a")
	req.Members[0].Capacity = 4
	first, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.SessionID = "b"
	second, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.SessionID = "c"
	third, err := s.Acquire(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProviderRef != "one" || second.ProviderRef != "two" || third.ProviderRef != "one" {
		t.Fatalf("refs=%s,%s,%s", first.ProviderRef, second.ProviderRef, third.ProviderRef)
	}
}

func TestRoutingSubprocessHelper(t *testing.T) {
	path := os.Getenv("CONDUIT_ROUTING_TEST_DB")
	if path == "" {
		return
	}
	s := openTestStore(t, path)
	r := request(os.Getenv("CONDUIT_ROUTING_TEST_SESSION"))
	r.Members = r.Members[:1]
	_, err := s.Acquire(context.Background(), r)
	switch {
	case err == nil:
		fmt.Println("ROUTING_RESULT_ACQUIRED")
	case errors.Is(err, ErrBusy):
		fmt.Println("ROUTING_RESULT_BUSY")
	default:
		t.Fatal(err)
	}
}

func TestSeparateProcessesContendForOneSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.db")
	openTestStore(t, path)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	results := make(chan string, 2)
	var wg sync.WaitGroup
	for _, session := range []string{"process-a", "process-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRoutingSubprocessHelper$")
			cmd.Env = append(os.Environ(), "CONDUIT_ROUTING_TEST_DB="+path, "CONDUIT_ROUTING_TEST_SESSION="+session)
			output, err := cmd.CombinedOutput()
			if err != nil {
				results <- fmt.Sprintf("error %v: %s", err, output)
				return
			}
			results <- string(output)
		}()
	}
	wg.Wait()
	close(results)
	acquired, busy := 0, 0
	for output := range results {
		switch {
		case strings.Contains(output, "ROUTING_RESULT_ACQUIRED"):
			acquired++
		case strings.Contains(output, "ROUTING_RESULT_BUSY"):
			busy++
		default:
			t.Fatalf("unexpected child output %s", output)
		}
	}
	if acquired != 1 || busy != 1 {
		t.Fatalf("acquired=%d busy=%d", acquired, busy)
	}
}

func TestBlockedDatabaseAcquisitionHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.db")
	a, b := openTestStore(t, path), openTestStore(t, path)
	conn, err := a.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = b.Acquire(ctx, request("blocked"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked acquire=%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("acquisition ignored cancellation while database locked")
	}
}

func TestEndpointCapacitySharedAcrossModels(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "routing.db"))
	first := request("a")
	first.Members = []Member{{ProviderRef: "one-model", EndpointID: "same-gpu", Capacity: 1}}
	if _, err := s.Acquire(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := request("b")
	second.Members = []Member{{ProviderRef: "other-model", EndpointID: "same-gpu", Capacity: 1}}
	if _, err := s.Acquire(context.Background(), second); !errors.Is(err, ErrBusy) {
		t.Fatalf("shared capacity=%v", err)
	}
}
