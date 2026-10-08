// Package routing schedules requests while retaining durable session-role
// affinity. Load counts participating Conduit processes, not external callers.
package routing

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // SQLite driver shared with the existing local stores.
)

var (
	// ErrBusy means the pinned endpoint, or every new-session candidate, is full.
	ErrBusy = errors.New("routing: endpoint capacity exhausted")
	// ErrAssignmentUnavailable requires an explicit configuration decision; affinity
	// is never silently moved to another provider or pool.
	ErrAssignmentUnavailable = errors.New("routing: assigned endpoint is unavailable")
	// ErrLeaseExpired means generation must not continue under this reservation.
	ErrLeaseExpired = errors.New("routing: request lease expired or missing")
)

// Member is a provider's capacity in a pool. EndpointID shares physical capacity
// across multiple provider/model registrations; empty uses ProviderRef.
type Member struct {
	ProviderRef string
	EndpointID  string
	Capacity    int
}

func (m Member) resource() string {
	if m.EndpointID != "" {
		return m.EndpointID
	}
	return m.ProviderRef
}

// Request identifies a stable session-role binding and one active request.
type Request struct {
	SessionID string
	Role      string
	PoolID    string
	Members   []Member
	TTL       time.Duration
}

// Lease is a renewable request reservation, separate from durable affinity.
type Lease struct {
	ID          string
	ProviderRef string
	EndpointID  string
	ExpiresAt   time.Time
}

// Store coordinates scheduler instances through one SQLite file.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open creates or opens a private SQLite scheduler database.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("routing: database path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("routing: create directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("routing: open file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("routing: private file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("routing: close file: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("routing: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	// busy_timeout applies to this store's single connection. BEGIN IMMEDIATE
	// reserves the writer before inspecting capacity, including across processes.
	_, err = db.ExecContext(context.Background(), `PRAGMA busy_timeout=25;
 CREATE TABLE IF NOT EXISTS route_bindings(session_id TEXT NOT NULL,role TEXT NOT NULL,pool_id TEXT NOT NULL,provider_ref TEXT NOT NULL,resource_id TEXT NOT NULL,created_ms INTEGER NOT NULL,PRIMARY KEY(session_id,role));
 CREATE TABLE IF NOT EXISTS route_leases(lease_id TEXT PRIMARY KEY,session_id TEXT NOT NULL,role TEXT NOT NULL,provider_ref TEXT NOT NULL,resource_id TEXT NOT NULL,expires_ms INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS route_leases_resource ON route_leases(resource_id,expires_ms);`)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("routing: initialize database: %w", err)
	}
	return s, nil
}

// Close releases this process's database handle; bindings remain durable.
func (s *Store) Close() error { return s.db.Close() }

func validTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		return 2 * time.Minute, nil
	}
	if ttl < time.Millisecond || ttl > 24*time.Hour {
		return 0, errors.New("routing: lease TTL must be between 1ms and 24h")
	}
	return ttl, nil
}

// Acquire atomically selects/reserves capacity. ErrBusy is retryable by the
// caller; it must wait with cancellation and must not switch a pinned session.
func (s *Store) Acquire(ctx context.Context, r Request) (Lease, error) {
	var lease Lease
	ttl, err := validTTL(r.TTL)
	if err != nil {
		return lease, err
	}
	if r.SessionID == "" || r.Role == "" || r.PoolID == "" || len(r.Members) == 0 {
		return lease, errors.New("routing: session, role, pool and members are required")
	}
	seen := map[string]bool{}
	for _, m := range r.Members {
		if m.ProviderRef == "" || m.Capacity <= 0 || seen[m.ProviderRef] {
			return lease, errors.New("routing: members require unique references and positive capacities")
		}
		seen[m.ProviderRef] = true
	}
	err = s.immediate(ctx, func(conn *sql.Conn) error {
		now := s.now()
		expires := now.Add(ttl)
		if _, err := conn.ExecContext(ctx, "DELETE FROM route_leases WHERE expires_ms<=?", now.UnixMilli()); err != nil {
			return err
		}
		var pool, ref, resource string
		err := conn.QueryRowContext(ctx, "SELECT pool_id,provider_ref,resource_id FROM route_bindings WHERE session_id=? AND role=?", r.SessionID, r.Role).Scan(&pool, &ref, &resource)
		assigned := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var chosen Member
		bestLoad := 2.0
		for _, m := range r.Members {
			if assigned && (pool != r.PoolID || ref != m.ProviderRef || resource != m.resource()) {
				continue
			}
			var active int
			if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM route_leases WHERE resource_id=?", m.resource()).Scan(&active); err != nil {
				return err
			}
			if active >= m.Capacity {
				if assigned {
					return ErrBusy
				}
				continue
			}
			load := float64(active) / float64(m.Capacity)
			if load < bestLoad {
				chosen = m
				bestLoad = load
			}
		}
		if chosen.ProviderRef == "" {
			if assigned {
				return ErrAssignmentUnavailable
			}
			return ErrBusy
		}
		if !assigned {
			if _, err := conn.ExecContext(ctx, "INSERT INTO route_bindings(session_id,role,pool_id,provider_ref,resource_id,created_ms) VALUES(?,?,?,?,?,?)", r.SessionID, r.Role, r.PoolID, chosen.ProviderRef, chosen.resource(), now.UnixMilli()); err != nil {
				return err
			}
		}
		lease = Lease{ID: rand.Text(), ProviderRef: chosen.ProviderRef, EndpointID: chosen.resource(), ExpiresAt: expires}
		_, err = conn.ExecContext(ctx, "INSERT INTO route_leases(lease_id,session_id,role,provider_ref,resource_id,expires_ms) VALUES(?,?,?,?,?,?)", lease.ID, r.SessionID, r.Role, lease.ProviderRef, lease.EndpointID, expires.UnixMilli())
		return err
	})
	if err != nil {
		return Lease{}, fmt.Errorf("routing: acquire: %w", err)
	}
	return lease, nil
}

// Renew extends an unexpired lease. A dead worker cannot resurrect expired
// capacity after another process has acquired it.
func (s *Store) Renew(ctx context.Context, id string, ttl time.Duration) error {
	ttl, err := validTTL(ttl)
	if err != nil {
		return err
	}
	return s.immediate(ctx, func(conn *sql.Conn) error {
		now := s.now()
		res, err := conn.ExecContext(ctx, "UPDATE route_leases SET expires_ms=? WHERE lease_id=? AND expires_ms>?", now.Add(ttl).UnixMilli(), id, now.UnixMilli())
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrLeaseExpired
		}
		return nil
	})
}

// Release is idempotent and removes only active request capacity, never affinity.
func (s *Store) Release(ctx context.Context, id string) error {
	return s.immediate(ctx, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, "DELETE FROM route_leases WHERE lease_id=?", id)
		return err
	})
}

func (s *Store) immediate(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := retryBusy(ctx, func() error { _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); return err }); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func retryBusy(ctx context.Context, fn func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		var coded interface{ Code() int }
		if !errors.As(err, &coded) || (coded.Code()&255 != 5 && coded.Code()&255 != 6) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
