// Package leader provides leader election for single-instance job execution.
// The default AlwaysLeader is the single-process backend; a distributed elector (a
// Postgres advisory lock, the sibling of clients/lock's postgres backend) is
// the production backend that lands when a second replica does.
package leader

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// AlwaysLeader is the single-process elector: it always reports leadership, for dev and
// single-replica deployments where exactly one instance runs the guarded work anyway.
type AlwaysLeader struct{}

// compile-time check: AlwaysLeader satisfies the LeaderElector contract.
var _ interfaces.LeaderElector = AlwaysLeader{}

// IsLeader always reports true — the single process is trivially the leader.
func (AlwaysLeader) IsLeader(context.Context) (bool, error) { return true, nil }
