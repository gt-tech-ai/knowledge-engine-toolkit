// Package workflow is the workflow tier: the one boundary observed in every
// environment and the only tier that opens a transaction. A consumer's workflow embeds a
// Base built from its contract and runs each contract method in one of three shapes: a
// read (Exec, no transaction), a command (ExecTx, one transaction) or a saga (Exec whose
// body commits each local step with InTx).
package workflow

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the workflow tier's decoration: recovery → logging → tracing → metrics →
// timeout, outermost first.
func Tier() decorate.Tier {
	return decorate.Tier{Name: "workflow", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerLogging,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerTimeout,
	}}
}

// Base is the embeddable core of a workflow named by its Subject: Name and Exec (a read
// or a saga) are promoted from the Subject, ExecTx runs a command and InTx a saga's local
// step. Each operation is one method of the workflow's contract, decorated as the
// component's name plus the method's snake_case name. The zero Base runs reads
// undecorated and refuses every transaction.
type Base struct {
	// tx opens the transaction a command or a saga step runs in.
	tx interfaces.TransactionManager

	// Subject runs the workflow's operations through the tier; it supplies Name and Exec.
	decorate.Subject
}

// NewBase builds the Base for the workflow named name over tx (needed only by ExecTx and
// InTx), decorated from decs: one chain per method of the contract W, each resolved as
// the workflow-tier component name+"."+op, so the config can tune the workflow or one of
// its operations. A W that is not an interface, or a decoration that fails to resolve,
// is an error.
func NewBase[W any](
	decs decorate.Decorations,
	name string,
	tx interfaces.TransactionManager,
) (Base, error) {
	subject, err := decs.SubjectOf[W](Tier(), name)
	if err != nil {
		return Base{}, err
	}
	return Base{tx: tx, Subject: subject}, nil
}

// ExecTx runs fn as the command op: one decorated operation whose body runs in one
// transaction, which commits when fn succeeds and rolls back when it fails. Every store
// call fn makes joins the transaction through the context. The body's error, or the
// commit's, is returned with its code intact and a zero result, so a result whose
// transaction did not commit never reaches the caller.
func (b Base) ExecTx[R any](
	ctx context.Context,
	op string,
	fn func(context.Context) (R, error),
) (R, error) {
	return b.Exec(ctx, op, func(ctx context.Context) (R, error) {
		return b.InTx(ctx, fn)
	})
}

// InTx runs fn in one transaction, undecorated: a saga's local step, inside the one
// decorated operation (Exec) that runs the saga, committing a write between remote calls
// or compensating one. A context already in a transaction, or a Base with no transaction
// manager, fails INTERNAL without running fn: a transaction never opens inside another.
func (b Base) InTx[R any](
	ctx context.Context,
	fn func(context.Context) (R, error),
) (R, error) {
	var zero R
	if interfaces.InTx(ctx) {
		return zero, apperr.New(
			apperr.CodeInternal,
			"workflow "+b.Name()+": a transaction is already open on this context",
		)
	}
	if b.tx == nil {
		return zero, apperr.New(
			apperr.CodeInternal,
			"workflow "+b.Name()+": no transaction manager",
		)
	}
	var out R
	err := b.tx.WithTransaction(ctx, func(txCtx context.Context) error {
		var err error
		out, err = fn(txCtx)
		return err
	})
	if err != nil {
		return zero, err
	}
	return out, nil
}

// Pass runs a background loop's pass as the operation op (its loop's name) on the
// workflow's own decoration, with no transaction: a pass
// that writes commits each write with InTx. Its method value is a loop.Decorator, so
// every pass is logged, counted, traced and recovered like a request.
func (b Base) Pass(
	op string,
	pass interfaces.Workflow[struct{}, struct{}],
) interfaces.Workflow[struct{}, struct{}] {
	return passFlow(func(ctx context.Context) (struct{}, error) {
		return b.Component().Exec(ctx, op, func(ctx context.Context) (struct{}, error) {
			return pass.Execute(ctx, struct{}{})
		})
	})
}

// passFlow is a decorated loop pass: a function that is an interfaces.Workflow.
type passFlow func(ctx context.Context) (struct{}, error)

// Execute runs the pass.
func (f passFlow) Execute(ctx context.Context, _ struct{}) (struct{}, error) {
	return f(ctx)
}
