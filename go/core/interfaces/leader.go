package interfaces

import "context"

// LeaderElector coordinates single-instance execution: only the elected leader
// runs the guarded work, so a job scheduled across N replicas fires once.
//
// Implementations: a single-process elector that is always the leader
// (dev/single-replica), or a distributed elector (a Postgres advisory lock, the sibling
// of clients/lock's postgres backend). Job code depends on this interface, never a
// concrete elector.
type LeaderElector interface {
	// IsLeader reports whether this instance currently holds leadership.
	IsLeader(ctx context.Context) (bool, error)
}
