package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/docker"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestClientsDocker_Exec tests the docker binary backend's command lines.
//
// Why this test is important:
//   - Builds must load into the local daemon without attestations (Kind and Compose
//     can't run an attested manifest list), and Compose must see the same files,
//     profiles and variables on up and down, or down leaves containers behind.
//
// What it tests:
//   - Build passes --load, no attestations, Dockerfile, target, platform, sorted build
//     args and tags, then the context; inspect of a missing image is NOT_FOUND;
//     compose up and down share the project, files and profiles, pass the env, wait on
//     up and remove volumes on down when asked; the stub finds no image.
func TestClientsDocker_Exec(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := docker.NewFromConfig(ctx, docker.Config{Kind: docker.KindExec},
		docker.Deps{Decorations: fixtures.Decorations(), Runner: r, Root: "/repo"})
	require.NoError(t, err)

	r.EXPECT().
		RunBufferedWithEnv(gomock.Any(), "/repo", gomock.Nil(), "docker", "buildx", "build",
			"--load", "--provenance=false", "--sbom=false", "-f", "d/Dockerfile",
			"--target", "run",
			"--platform", "linux/amd64", "--build-arg", "A=1", "--build-arg", "B=2",
			"-t", "api:dev", ".").
		Return(interfaces.CmdResult{})
	require.NoError(t, c.Build(ctx, types.BuildSpec{
		Context: ".", Dockerfile: "d/Dockerfile", Target: "run", Platform: "linux/amd64",
		BuildArgs: map[string]string{"B": "2", "A": "1"}, Tags: []string{"api:dev"},
	}))

	r.EXPECT().RunBufferedWithEnv(gomock.Any(), "/repo", gomock.Nil(), "docker", "image",
		"inspect", "gone:1").Return(exitWith(1, "Error: No such image: gone:1"))
	_, err = c.Inspect(ctx, "gone:1")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	spec := types.ComposeSpec{
		Project: "dev", Files: []string{"a.yaml", "b.yaml"}, Profiles: []string{"obs"},
		Services: []string{"api"}, Env: []string{"X=1"}, RemoveVolumes: true,
	}
	compose := []any{
		"compose",
		"-p",
		"dev",
		"-f",
		"a.yaml",
		"-f",
		"b.yaml",
		"--profile",
		"obs",
	}
	r.EXPECT().RunBufferedWithEnv(gomock.Any(), "/repo", []string{"X=1"}, "docker",
		append(compose, "up", "-d", "--wait", "api")...).Return(interfaces.CmdResult{})
	require.NoError(t, c.ComposeUp(ctx, spec))
	r.EXPECT().RunBufferedWithEnv(gomock.Any(), "/repo", []string{"X=1"}, "docker",
		append(compose, "down", "--volumes")...).Return(interfaces.CmdResult{})
	require.NoError(t, c.ComposeDown(ctx, spec))

	stub, err := docker.NewFromConfig(ctx, docker.Config{},
		docker.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	_, err = stub.Inspect(ctx, "x")
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}
