// Package sessionstats provides session-scoped metrics for token savings.
package sessionstats

import "sync/atomic"

// SessionMetrics tracks per-session token savings from optimization features.
// All methods are goroutine-safe.
var SessionMetrics sessionMetrics

type sessionMetrics struct {
	rtkBytesSaved           atomic.Int64
	rtkCallCount            atomic.Int64
	microcompactTokensSaved atomic.Int64
	microcompactCallCount   atomic.Int64
	truncateBytesSaved      atomic.Int64
	truncateCallCount       atomic.Int64
	compactCallCount        atomic.Int64
}

// RecordRTK records bytes saved by RTK filtering.
func (m *sessionMetrics) RecordRTK(bytesSaved int) {
	m.rtkBytesSaved.Add(int64(bytesSaved))
	m.rtkCallCount.Add(1)
}

// RecordMicrocompact records tokens cleared by microcompact.
func (m *sessionMetrics) RecordMicrocompact(tokensSaved int) {
	m.microcompactTokensSaved.Add(int64(tokensSaved))
	m.microcompactCallCount.Add(1)
}

// RecordTruncate records bytes truncated to disk.
func (m *sessionMetrics) RecordTruncate(bytesSaved int) {
	m.truncateBytesSaved.Add(int64(bytesSaved))
	m.truncateCallCount.Add(1)
}

// RecordCompact records a full compaction.
func (m *sessionMetrics) RecordCompact() {
	m.compactCallCount.Add(1)
}

// Snapshot returns the current metrics as a TokenSavingsStats.
func (m *sessionMetrics) Snapshot() TokenSavingsStats {
	return TokenSavingsStats{
		RTKBytesSaved:           int(m.rtkBytesSaved.Load()),
		RTKCallCount:            int(m.rtkCallCount.Load()),
		MicrocompactTokensSaved: int(m.microcompactTokensSaved.Load()),
		MicrocompactCallCount:   int(m.microcompactCallCount.Load()),
		TruncateBytesSaved:      int(m.truncateBytesSaved.Load()),
		TruncateCallCount:       int(m.truncateCallCount.Load()),
		CompactCallCount:        int(m.compactCallCount.Load()),
	}
}

// Reset clears all metrics (e.g., at session start).
func (m *sessionMetrics) Reset() {
	m.rtkBytesSaved.Store(0)
	m.rtkCallCount.Store(0)
	m.microcompactTokensSaved.Store(0)
	m.microcompactCallCount.Store(0)
	m.truncateBytesSaved.Store(0)
	m.truncateCallCount.Store(0)
	m.compactCallCount.Store(0)
}
