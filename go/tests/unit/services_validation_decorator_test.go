package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/validator"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// nameRequired is the shared business rule used by the validation-decorator tests: an
// entity's Name must be non-empty, and a violation is a coded invalid-input error
// (ARCHITECTURE.md#error-codes).
func nameRequired(e *fixtures.TestEntity) error {
	if e.Name == "" {
		return apperr.New(apperr.CodeInvalidInput, "name is required")
	}
	return nil
}

// TestServiceDecorator_WithValidation_RejectsInvalidAndShortCircuits tests that the
// service builder's WithValidation decorator rejects an entity that fails a business
// rule and short-circuits before the inner service runs, while a valid entity passes
// through.
//
// Why this test is important:
//   - Business-rule validation below the transport boundary is the guard that keeps a
//     malformed write from ever reaching the domain service; if it did not short-circuit,
//     invalid data would hit the repository.
//
// What it tests:
//   - A Create with an entity failing the rule returns the rule's coded error and the
//     inner service's success is never observed; a valid entity's Create succeeds and
//     returns the inner result.
func TestServiceDecorator_WithValidation_RejectsInvalidAndShortCircuits(t *testing.T) {
	t.Parallel()

	v := validator.New(nameRequired)

	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithValidation(v.Validate).Build()

	// Invalid: empty name → rejected with the rule's coded error; inner not reached
	// (the StubService's Create would return the entity with no error).
	_, err := svc.Create(context.Background(), &fixtures.TestEntity{ID: "1", Name: ""})
	require.Error(t, err)
	require.True(
		t,
		apperr.Is(err, apperr.CodeInvalidInput),
		"rejection must carry CodeInvalidInput",
	)

	// Valid: passes validation and reaches the inner service.
	out, err := svc.Create(
		context.Background(),
		&fixtures.TestEntity{ID: "1", Name: "ok"},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", out.Name)
}

// TestServiceDecorator_WithValidation_ValidatesUpdateAndPassesReadsThrough tests that the
// validation decorator also guards Update while letting entity-less operations (Get,
// List, Delete) pass straight through unvalidated.
//
// Why this test is important:
//   - Update carries an entity and must be validated exactly like Create; conversely,
//     reads and deletes carry no entity, so forcing them through validation would be
//     wrong (and could reject a legitimate read). Both behaviors must hold.
//
// What it tests:
//   - An invalid Update short-circuits with the rule's coded error and a valid Update
//     succeeds; Get, List, and Delete all pass through without invoking the rule.
func TestServiceDecorator_WithValidation_ValidatesUpdateAndPassesReadsThrough(
	t *testing.T,
) {
	t.Parallel()

	svc := decorators.NewBuilder[fixtures.TestEntity, fixtures.TestParams, string](
		fixtures.StubService(nil, false), "test",
	).WithValidation(validator.New(nameRequired).Validate).Build()
	ctx := context.Background()

	// Update is validated: invalid short-circuits, valid passes through to the inner
	// service.
	_, err := svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1", Name: ""})
	require.True(
		t,
		apperr.Is(err, apperr.CodeInvalidInput),
		"invalid Update is rejected",
	)
	out, err := svc.Update(ctx, "1", &fixtures.TestEntity{ID: "1", Name: "ok"})
	require.NoError(t, err)
	require.Equal(t, "ok", out.Name)

	// Entity-less operations pass through unvalidated (no rule to violate).
	_, err = svc.Get(ctx, "1")
	require.NoError(t, err)
	_, err = svc.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, "1"))
}
