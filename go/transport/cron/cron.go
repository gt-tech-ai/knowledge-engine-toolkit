// Package cron is the scheduled-job front-end of an operations registry
// (execution/ops): a job's entrypoint names one operation and its JSON arguments, and
// Run executes it unattended under the registry's exposure policy.
package cron

import (
	"context"
	"fmt"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/execution/ops"
)

// Run executes the operation named name with raw JSON arguments, reporting progress to
// obs. It runs only an operation reg admits (an excluded one is CodeForbidden, an
// unknown one CodeNotFound), refuses an interactive operation, which would wait for a
// human, and an argument the input does not declare (both CodeInvalidInput), and holds
// reg's working-tree slot for the run (a held slot is CodeUnavailable).
func Run(
	ctx context.Context,
	reg *ops.Registry,
	name string,
	raw []byte,
	obs interfaces.ExecutionObserver,
) error {
	res, err := reg.Reserve(name, "cron:"+name)
	if err != nil {
		return err
	}
	op, _ := reg.Lookup(name)
	if op.Descriptor().Lifecycle == types.OpLifecycleInteractive {
		res.Release()
		return apperr.New(apperr.CodeInvalidInput, fmt.Sprintf(
			"operation %q is interactive; a scheduled job cannot run it", name,
		))
	}
	input, err := ops.Decode(op, raw)
	if err != nil {
		res.Release()
		return err
	}
	return res.Run(ctx, input, obs)
}
