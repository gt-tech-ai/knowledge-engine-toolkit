package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// loggedNames returns Decorations whose logger records each logged component name, and
// the slice it records into.
func loggedNames(
	t *testing.T,
	cfg *decoration.Config,
) (decs decorate.Decorations, logged *[]string) {
	t.Helper()
	logger := mocks.NewMockLogger(gomock.NewController(t))
	logger.EXPECT().WithContext(gomock.Any()).Return(logger).AnyTimes()
	names := []string{}
	logged = &names
	// The logging layer logs each operation as (tier, name, "operation", op).
	logger.EXPECT().Debug(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ string, kv ...any) { *logged = append(*logged, kv[1].(string)) }).
		AnyTimes()
	logger.EXPECT().Info(gomock.Any(), gomock.Any()).AnyTimes()
	return decorate.NewDecorations(cfg, decorate.Collaborators{
		Observability: decorate.Observability{Logger: logger},
	}), logged
}

// TestOperations_EachOperationTakesItsOwnDecorationName tests per-operation decoration.
//
// Why this test is important:
//   - A component's operations are tuned one by one in YAML; if they shared the
//     component's decoration, an operator could not trace one query or time out one
//     SDK call without doing the same to every other operation on the component.
//
// What it tests:
//   - A repository query built from NewBaseFrom(decs, "api.docs") is logged as
//     "api.docs.count" when that override turns logging on; its sibling "find" is not.
//   - Every operation is listed by Components under its tier, and counts as used for
//     Verify.
func TestOperations_EachOperationTakesItsOwnDecorationName(t *testing.T) {
	t.Parallel()
	on := true
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.docs.count": {Logging: &on},
	}
	decs, logged := loggedNames(t, &cfg)

	base, err := repository.NewBaseFrom(decs, "api.docs")
	require.NoError(t, err)
	count := repository.Query(
		base,
		"count",
		func(_ context.Context, org string) (int, error) {
			return len(org), nil
		},
	)
	find := repository.Query(
		base,
		"find",
		func(_ context.Context, id string) (string, error) {
			return id, nil
		},
	)
	var _ interfaces.Op[string, int] = count

	n, err := count.Execute(t.Context(), "org1")
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	_, err = find.Execute(t.Context(), "d1")
	require.NoError(t, err)

	assert.Contains(t, *logged, "api.docs.count")
	assert.NotContains(t, *logged, "api.docs.find")
	assert.Contains(
		t,
		decs.Components(),
		decorate.Component{Tier: "repository", Name: "api.docs.count"},
	)
	assert.Contains(
		t,
		decs.Components(),
		decorate.Component{Tier: "repository", Name: "api.docs.find"},
	)
	require.NoError(t, decs.Verify("api"))
}

// TestStoreOperations_QueryReadsAndStatementWritesOnlyInATransaction tests the store
// tier's operation builders.
//
// Why this test is important:
//   - A named query must see the request transaction's uncommitted writes, and a named
//     write must never autocommit outside the workflow that owns it.
//
// What it tests:
//   - A Query runs on the raw client outside a transaction and on the bound client
//     inside one.
//   - A Statement runs on the bound client inside a transaction, and outside one fails
//     INTERNAL naming the store and operation without running.
func TestStoreOperations_QueryReadsAndStatementWritesOnlyInATransaction(t *testing.T) {
	t.Parallel()
	base, err := store.NewBaseFrom(
		decorate.Decorations{},
		"identity.members",
		&storeClient{name: "raw"},
	)
	require.NoError(t, err)
	query := store.Query(
		base,
		"count_admins",
		func(_ context.Context, c *storeClient, _ string) (string, error) {
			return c.name, nil
		},
	)
	ran := false
	stmt := store.Statement(
		base,
		"stamp",
		func(_ context.Context, c *storeClient, _ string) (string, error) {
			ran = true
			return c.name, nil
		},
	)

	got, err := query.Execute(t.Context(), "o")
	require.NoError(t, err)
	assert.Equal(t, "raw", got)

	inTx := store.WithClient(interfaces.WithInTx(t.Context()), &storeClient{name: "tx"})
	got, err = query.Execute(inTx, "o")
	require.NoError(t, err)
	assert.Equal(t, "tx", got)
	got, err = stmt.Execute(inTx, "o")
	require.NoError(t, err)
	assert.Equal(t, "tx", got)

	ran = false
	_, err = stmt.Execute(t.Context(), "o")
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.ErrorContains(t, err, "identity.members.stamp")
	assert.False(t, ran, "a statement never runs outside a transaction")
}

// TestRepositoryOperations_CommandNeverRetries tests the repository tier's builders.
//
// Why this test is important:
//   - A retried write whose first attempt committed writes twice; only reads may replay.
//
// What it tests:
//   - A Query runs through the retrier and a Command does not (one Retry call for two
//     operations).
func TestRepositoryOperations_CommandNeverRetries(t *testing.T) {
	t.Parallel()
	base := repository.NewBase(
		"api.docs",
		decorate.Deps{Retrier: passThroughRetrier(t, 1)},
	)
	read := repository.Query(
		base,
		"find",
		func(_ context.Context, id string) (string, error) {
			return id, nil
		},
	)
	write := repository.Command(
		base,
		"rename",
		func(_ context.Context, id string) (string, error) {
			return id, nil
		},
	)
	_, err := read.Execute(t.Context(), "d1")
	require.NoError(t, err)
	_, err = write.Execute(t.Context(), "d1")
	require.NoError(t, err)
}

// renameInput is a service input that validates itself.
type renameInput struct {
	// Name is the new name; empty is invalid.
	Name string
}

// Validate rejects an empty name.
func (r renameInput) Validate() error {
	if r.Name == "" {
		return apperr.InvalidInput("name is required")
	}
	return nil
}

// TestServiceOperation_ValidatesAValidatableInput tests the service tier's builder.
//
// Why this test is important:
//   - The service is where an operation's input is checked; an invalid input that
//     reached the repository would persist bad data.
//
// What it tests:
//   - An input that implements Validatable is validated before the operation runs: an
//     invalid one returns INVALID_INPUT and the operation does not run; a valid one runs.
func TestServiceOperation_ValidatesAValidatableInput(t *testing.T) {
	t.Parallel()
	base, err := service.NewBaseFrom(decorate.Decorations{}, "api.docs")
	require.NoError(t, err)
	ran := 0
	rename := service.Op(
		base,
		"rename",
		func(_ context.Context, in renameInput) (string, error) {
			ran++
			return in.Name, nil
		},
	)

	_, err = rename.Execute(t.Context(), renameInput{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	got, err := rename.Execute(t.Context(), renameInput{Name: "x"})
	require.NoError(t, err)
	assert.Equal(t, "x", got)
	assert.Equal(t, 1, ran)
}

// TestWorkflowOperations_ReadOpensNoTransactionAndCommandOpensOne tests the workflow
// tier's builders.
//
// Why this test is important:
//   - The workflow is the only tier that opens a transaction: a read that opened one
//     holds a connection for nothing, and a command that did not would have its writes
//     refused.
//
// What it tests:
//   - Read and Saga run with no transaction open; Command runs in exactly one.
func TestWorkflowOperations_ReadOpensNoTransactionAndCommandOpensOne(t *testing.T) {
	t.Parallel()
	base, err := workflow.NewBaseFrom(
		decorate.Decorations{},
		"identity.team",
		txManager(t, 1, nil),
	)
	require.NoError(t, err)
	inTx := func(ctx context.Context, _ string) (bool, error) {
		return interfaces.InTx(ctx), nil
	}

	got, err := workflow.Read(base, "get", inTx).Execute(t.Context(), "t1")
	require.NoError(t, err)
	assert.False(t, got)
	got, err = workflow.Saga(base, "provision", inTx).Execute(t.Context(), "t1")
	require.NoError(t, err)
	assert.False(t, got)
	got, err = workflow.Command(base, "create", inTx).Execute(t.Context(), "t1")
	require.NoError(t, err)
	assert.True(t, got)
}

// sdkOption is an SDK call's functional option.
type sdkOption func(*string)

// TestClientCall_RunsAnSDKFunctionAsAnOperation tests the client tier's builders.
//
// Why this test is important:
//   - An SDK call is the one place an outbound request leaves the process; it needs the
//     client tier's retry, breaker and timeout from config, and a non-idempotent one must
//     never be replayed.
//
// What it tests:
//   - Call adapts an SDK function func(ctx, in, ...opt) into an Op, through the retrier.
//   - CallOnce runs without the retrier (one Retry call for two calls).
func TestClientCall_RunsAnSDKFunctionAsAnOperation(t *testing.T) {
	t.Parallel()
	base := client.NewBase("cli.sts", decorate.Deps{Retrier: passThroughRetrier(t, 1)})
	sdk := func(_ context.Context, in string, _ ...sdkOption) (string, error) {
		return "caller:" + in, nil
	}
	call := client.Call(base, "get_caller_identity", sdk)
	once := client.CallOnce(base, "send_email", sdk)

	got, err := call.Execute(t.Context(), "acct")
	require.NoError(t, err)
	assert.Equal(t, "caller:acct", got)
	_, err = once.Execute(t.Context(), "acct")
	require.NoError(t, err)
}

// TestDecorationsVerifyExplicit_FailsOnAComponentWithNoEntry tests the strict check.
//
// Why this test is important:
//   - A generated decoration file lists every component; a component added without its
//     entry silently takes only the defaults, which is the drift the generator exists to
//     prevent.
//
// What it tests:
//   - VerifyExplicit("api") fails INVALID_INPUT naming the resolved component that has no
//     override of its own name, and ignores components outside its prefixes.
//   - With an entry for each, it passes.
func TestDecorationsVerifyExplicit_FailsOnAComponentWithNoEntry(t *testing.T) {
	t.Parallel()
	off := false
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{"api.docs": {Logging: &off}}
	decs := decorate.NewDecorations(&cfg, decorate.Collaborators{})
	_, err := decs.Deps(repository.Tier(), "api.docs")
	require.NoError(t, err)
	_, err = decs.Deps(repository.Tier(), "api.users")
	require.NoError(t, err)
	_, err = decs.Deps(repository.Tier(), "identity.users")
	require.NoError(t, err)

	err = decs.VerifyExplicit("api")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "repositories.overrides.api.users")
	assert.NotContains(t, err.Error(), "identity.users")

	cfg.Repositories.Overrides["api.users"] = decoration.Layers{Logging: &off}
	clean := decorate.NewDecorations(&cfg, decorate.Collaborators{})
	_, err = clean.Deps(repository.Tier(), "api.docs")
	require.NoError(t, err)
	_, err = clean.Deps(repository.Tier(), "api.users")
	require.NoError(t, err)
	require.NoError(t, clean.VerifyExplicit("api"))
}
