package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestServicesGuard_Gen tests the gen kind: a generator's drift as a guard row.
//
// Why this test is important:
//   - A committed generated file that drifts from its source ships stale contracts; a
//     drift check must be the generator's own check mode, named in data, so no product
//     writes a Go guard per generator.
//
// What it tests:
//   - A row naming the generator (expanded from each) reports one finding per stale or
//     missing output, the state followed by the row's message, and passes in sync.
//   - A row whose files select nothing passes when optional, without rendering.
//   - A lookup failure fails the row; building rejects a gen row with no gen or no
//     generator lookup.
func TestServicesGuard_Gen(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	render := func(out types.Artifacts) interfaces.Generator {
		g := mocks.NewMockGenerator(ctrl)
		g.EXPECT().Render(gomock.Any(), gomock.Any()).Return(out, nil).AnyTimes()
		return g
	}
	gens := map[string]interfaces.Generator{
		"images": render(types.Artifacts{
			textDeployAYaml:     []byte("image: app:1.3\nreplicas: 2\n"),
			"deploy/c.yaml":     []byte("new\n"),
			textDeployBManifest: []byte("image: app:latest\nreplicas: 1\n"),
		}),
		textInfra: render(
			types.Artifacts{textInfraMainTf: []byte("// TODO\nresource \"x\" {}\n")},
		),
	}
	lookup := func(_ context.Context, name string) (interfaces.Generator, error) {
		if g, ok := gens[name]; ok {
			return g, nil
		}
		return nil, apperr.NotFound("no generator " + name)
	}
	each := func(context.Context, interfaces.Codebase) ([]map[string]string, error) {
		return []map[string]string{{textGen: "images"}}, nil
	}
	v := runGuards(t, []types.GuardRow{
		{
			Name: textStale, Kind: types.GuardGen, Gen: "{gen}", Each: "gens",
			Message: "run `gen {gen}`",
		},
		{Name: "current", Kind: types.GuardGen, Gen: textInfra},
		{
			Name: "unsourced", Kind: types.GuardGen, Gen: textNope, Optional: true,
			Files: types.FileSelector{Roots: []string{textAbsent}},
		},
		{Name: textUnknown, Kind: types.GuardGen, Gen: textNope},
	}, &guard.Deps{
		Generators: lookup,
		Expanders:  map[string]guard.Expander{"gens": each},
	})

	require.Equal(t, types.StatusFail, v[textStale].Status)
	got := map[string]string{}
	for _, f := range v[textStale].Findings {
		got[f.File] = f.Message
	}
	assert.Equal(t, map[string]string{
		textDeployAYaml: "out of date; run `gen images`",
		"deploy/c.yaml": "missing; run `gen images`",
	}, got)
	assert.Equal(t, types.StatusPass, v["current"].Status)
	assert.Equal(t, types.StatusPass, v["unsourced"].Status)
	assert.Equal(t, types.StatusFail, v[textUnknown].Status)
	assert.Contains(t, v[textUnknown].Detail, "no generator nope")

	for _, r := range []types.GuardRow{
		{Name: "no gen", Kind: types.GuardGen},
	} {
		_, err := guard.NewFromConfig(
			[]types.GuardRow{r},
			&guard.Deps{Generators: lookup},
		)
		require.Error(t, err, r.Name)
	}
	_, err := guard.NewFromConfig(
		[]types.GuardRow{{Name: textX, Kind: types.GuardGen, Gen: textA}},
		&guard.Deps{},
	)
	require.Error(t, err, "no lookup")
}
