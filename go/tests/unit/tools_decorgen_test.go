package unit_test

import (
	"context"
	"os"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/metrics/prom"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/decorgen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	gen "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tools/decorgen"
)

// decorgenFixture is the import path of the package decorgen's fixture lives in.
const decorgenFixture = "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/decorgen"

// TestDecorgen_GeneratesTheCommittedDecorator tests decorgen.Generate.
//
// Why this test is important:
//   - A consumer commits the generated decorators and a drift check regenerates them;
//     the generator must be deterministic and its fixture output current.
//   - A typo in an interface name, or a name that is not an interface, must fail loudly
//     instead of writing an empty file.
//
// What it tests:
//   - Generating the fixture's Store reproduces the committed decorated_gen.go byte for
//     byte.
//   - An unknown name, a non-interface and a missing package name are CodeInvalidInput.
func TestDecorgen_GeneratesTheCommittedDecorator(t *testing.T) {
	t.Parallel()
	cfg := gen.Config{
		Source:      decorgenFixture,
		Package:     "decorgen",
		PackagePath: decorgenFixture,
		Interfaces:  []string{"Store"},
	}
	got, err := gen.Generate(cfg)
	require.NoError(t, err)
	want, err := os.ReadFile("../fixtures/decorgen/decorated_gen.go")
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))

	for name, bad := range map[string]gen.Config{
		"unknown name":  {Source: decorgenFixture, Package: "p", Interfaces: []string{"Nope"}},
		"not an iface":  {Source: "context", Package: "p", Interfaces: []string{"CancelFunc"}},
		"no package":    {Source: decorgenFixture, Interfaces: []string{"Store"}},
		"no interfaces": {Source: decorgenFixture, Package: "p"},
	} {
		_, err := gen.Generate(bad)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err), name)
	}
}

// TestDecorgen_DecoratorRunsOperationsThroughTheSubject tests a generated decorator.
//
// Why this test is important:
//   - The generated wrapper is how a component's custom operations get its tier's
//     layers; each must run through the subject under its own name, with its arguments
//     (a variadic tail included) and every value forwarded, and a non-operation must be
//     left alone.
//
// What it tests:
//   - Exists, Mark and Pair each count once under their method name, return the wrapped
//     values, and Mark forwards its variadic reasons.
//   - A panic in an operation is recovered into CodeInternal by the subject's recovery.
//   - Name and Close reach the wrapped store and are not counted.
func TestDecorgen_DecoratorRunsOperationsThroughTheSubject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	inner := mocks.NewMockDecorgenStore(ctrl)
	reg := promclient.NewRegistry()
	tier := client.Tier()
	tier.Order = append([]decorate.Layer{decorate.LayerRecovery}, tier.Order...)
	subject := tier.Subject("store", decorate.Deps{
		Observability: decorate.Observability{Metrics: prom.NewFromRegistry(reg)},
	})
	store := decorgen.NewDecoratedStore(inner, subject)

	inner.EXPECT().Exists(gomock.Any(), "a").Return(true, nil)
	inner.EXPECT().Mark(gomock.Any(), "a", "x", "y").Return(nil)
	inner.EXPECT().Pair(gomock.Any()).Return(7, "seven", nil)
	inner.EXPECT().Name().Return("inner")
	inner.EXPECT().Close(gomock.Any())

	ok, err := store.Exists(ctx, "a")
	require.NoError(t, err)
	assert.True(t, ok)
	require.NoError(t, store.Mark(ctx, "a", "x", "y"))
	n, s, err := store.Pair(ctx)
	require.NoError(t, err)
	assert.Equal(t, 7, n)
	assert.Equal(t, "seven", s)
	assert.Equal(t, "inner", store.Name())
	store.Close(ctx)
	for _, op := range []string{"Exists", "Mark", "Pair"} {
		assert.InDelta(t, 1, clientOpCounter(t, reg, op), 0, op)
	}

	inner.EXPECT().Pair(gomock.Any()).DoAndReturn(
		func(context.Context) (int, string, error) { panic("boom") },
	)
	_, _, err = store.Pair(ctx)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}
