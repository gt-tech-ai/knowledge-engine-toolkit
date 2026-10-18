package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// docsRepository is a repository contract with two operations, count and find.
type docsRepository interface {
	// Count counts org's documents.
	Count(ctx context.Context, org string) (int, error)

	// Find finds the document id.
	Find(ctx context.Context, id string) (string, error)
}

// reposEnt and reposEntParams are minimal entity/param types for the decorated-repo
// tests.
type reposEnt struct{ ID string }

type reposEntParams struct{}

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
		func(_ string, kv ...any) { *logged = append(*logged, kv[3].(string)) }).
		AnyTimes()
	logger.EXPECT().Info(gomock.Any(), gomock.Any()).AnyTimes()
	return decorate.NewDecorations(cfg, decorate.Collaborators{
		Observability: decorate.Observability{Logger: logger},
	}), logged
}

// TestBases_EachContractMethodTakesItsOwnDecoration tests per-operation decoration.
//
// Why this test is important:
//   - A component's operations are tuned one by one in YAML; if they shared the
//     component's decoration, an operator could not trace one query or time out one
//     call without doing the same to every other operation on the component.
//
// What it tests:
//   - A Base built from a contract declares one operation per method, in snake case:
//     with logging turned on for "api.docs.count" only, count is logged and find is not.
//   - Every operation is listed by Components under its tier and counts as used for
//     Verify.
func TestBases_EachContractMethodTakesItsOwnDecoration(t *testing.T) {
	t.Parallel()
	on := true
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.docs.count": {Logging: &on},
	}
	decs, logged := loggedNames(t, &cfg)

	b, err := repository.NewBase[docsRepository](decs, "api.docs")
	require.NoError(t, err)
	_, err = b.Exec(
		t.Context(),
		"count",
		func(context.Context) (int, error) { return 4, nil },
	)
	require.NoError(t, err)
	_, err = b.Exec(
		t.Context(),
		"find",
		func(context.Context) (string, error) { return "d", nil },
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"count"}, *logged)
	assert.Contains(t, decs.Components(),
		decorate.Component{Tier: "repository", Name: "api.docs.count"})
	assert.Contains(t, decs.Components(),
		decorate.Component{Tier: "repository", Name: "api.docs.find"})
	require.NoError(t, decs.Verify("api"))
}

// TestBases_AnOperationTheContractLacksFails tests the declared-operation guard.
//
// Why this test is important:
//   - An operation name the contract does not have has no config key an operator could
//     tune; running it on the component's chain would hide a typo until production.
//
// What it tests:
//   - Exec of an undeclared operation fails INTERNAL naming the component and the
//     operation, and the body never runs.
func TestBases_AnOperationTheContractLacksFails(t *testing.T) {
	t.Parallel()
	b, err := repository.NewBase[docsRepository](fixtures.Decorations(), "api.docs")
	require.NoError(t, err)
	ran := false

	_, err = b.Exec(t.Context(), "rename", func(context.Context) (int, error) {
		ran = true
		return 0, nil
	})

	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	require.ErrorContains(t, err, "api.docs has no operation rename")
	assert.False(t, ran)
}

// TestBases_AContractMustBeAnInterfaceWithMethods tests the contract check.
//
// Why this test is important:
//   - A base built from a struct or an empty interface declares no operations, so every
//     call would fail at run time; the composition root must refuse it at boot.
//
// What it tests:
//   - NewBase over a struct, or over an interface with no methods, fails INTERNAL.
func TestBases_AContractMustBeAnInterfaceWithMethods(t *testing.T) {
	t.Parallel()
	_, err := service.NewBase[struct{}](fixtures.Decorations(), "api.docs")
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	_, err = service.NewBase[any](fixtures.Decorations(), "api.docs")
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestDecorationsVerify_FailsAnOperationOverrideNoContractDeclares tests that an override
// naming a missing operation is caught.
//
// Why this test is important:
//   - An override for an operation that was renamed or never existed silently tunes
//     nothing; the operator believes a timeout is set when it is not.
//
// What it tests:
//   - Verify fails INVALID_INPUT naming "api.docs.rename" when no contract declares it,
//     and passes for an override of a declared operation.
func TestDecorationsVerify_FailsAnOperationOverrideNoContractDeclares(t *testing.T) {
	t.Parallel()
	off := false
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.docs.rename": {Logging: &off},
	}
	decs := decorate.NewDecorations(&cfg, decorate.Collaborators{})
	_, err := repository.NewBase[docsRepository](decs, "api.docs")
	require.NoError(t, err)

	err = decs.Verify("api")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "api.docs.rename")

	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.docs.find": {Logging: &off},
	}
	clean := decorate.NewDecorations(&cfg, decorate.Collaborators{})
	_, err = repository.NewBase[docsRepository](clean, "api.docs")
	require.NoError(t, err)
	require.NoError(t, clean.Verify("api"))
}

// TestDecorations_OperationsShareTheirComponentsBreaker tests stateful-layer sharing.
//
// Why this test is important:
//   - A breaker protects one dependency: when one operation trips it, the component's
//     other operations must stop calling the failing dependency too. A breaker per
//     operation would let every operation fail on its own before shedding.
//   - An operation that configures its own breaker opts out on purpose.
//
// What it tests:
//   - With the component's breaker tripping after one failure, a failed count opens it
//     for find as well (find's body never runs).
//   - When find's own override configures a breaker, find keeps running.
func TestDecorations_OperationsShareTheirComponentsBreaker(t *testing.T) {
	t.Parallel()
	on := true
	breaker := decoration.CircuitBreaker{
		Enabled: &on,
		BreakerConfig: resilience.BreakerConfig{
			ConsecutiveFailures: 1,
			Timeout:             time.Hour,
		},
	}
	run := func(t *testing.T, overrides map[string]decoration.Layers) bool {
		t.Helper()
		cfg := decoration.DefaultConfig()
		cfg.Repositories.Overrides = overrides
		decs := decorate.NewDecorations(&cfg, decorate.Collaborators{})
		b, err := repository.NewBase[docsRepository](decs, "api.docs")
		require.NoError(t, err)
		_, err = b.Exec(t.Context(), "count", func(context.Context) (int, error) {
			return 0, apperr.New(apperr.CodeUnavailable, "db down")
		})
		require.Error(t, err)
		ran := false
		_, _ = b.Exec(t.Context(), "find", func(context.Context) (string, error) {
			ran = true
			return "", nil
		})
		return ran
	}

	assert.False(t, run(t, map[string]decoration.Layers{
		"api.docs": {CircuitBreaker: breaker},
	}), "a tripped component breaker sheds every operation")
	assert.True(t, run(t, map[string]decoration.Layers{
		"api.docs":      {CircuitBreaker: breaker},
		"api.docs.find": {CircuitBreaker: breaker},
	}), "an operation with its own breaker is not shed by another's failures")
}

// TestDecorationsInventory_ListsEachOperationWithItsSources tests the inventory.
//
// Why this test is important:
//   - An operator reading --print-decorations must see which config keys produced each
//     operation's layers; without the sources, a surprising timeout cannot be traced to
//     the override that set it.
//
// What it tests:
//   - The inventory lists the component and each operation, and an operation's sources
//     run from the defaults through the component's override to its own.
func TestDecorationsInventory_ListsEachOperationWithItsSources(t *testing.T) {
	t.Parallel()
	timeout := time.Second
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{
		"api.docs":       {Timeout: &timeout},
		"api.docs.count": {Timeout: &timeout},
	}
	decs := decorate.NewDecorations(&cfg, decorate.Collaborators{})
	_, err := repository.NewBase[docsRepository](decs, "api.docs")
	require.NoError(t, err)

	byName := map[string]decorate.Resolved{}
	for _, r := range decs.Inventory() {
		byName[r.Name] = r
	}

	require.Contains(t, byName, "api.docs")
	require.Contains(t, byName, "api.docs.find")
	count := byName["api.docs.count"]
	assert.Equal(t, "repository", count.Tier)
	assert.Equal(t, []string{
		"defaults", "overrides.api.docs", "overrides.api.docs.count",
	}, count.Sources)
	require.NotNil(t, count.Layers.Timeout)
	assert.Equal(t, time.Second, *count.Layers.Timeout)
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
