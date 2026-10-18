package interfaces

import "context"

// Op is one operation of a component: a named query, command, saga or outbound call,
// built by its tier's base under its own decoration name. A component whose verbs are
// not CRUD exposes them as Op values beside its role interfaces, so each verb is
// decorated, configured and mocked on its own. Pipeline and Workflow have the same
// method set, so an Op is both.
type Op[In any, Out any] interface {
	// Execute runs the operation on in.
	Execute(ctx context.Context, in In) (Out, error)
}
