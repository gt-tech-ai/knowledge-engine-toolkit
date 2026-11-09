package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/docker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
		docker.Deps{Decorations: fixtures.Decorations(), Runner: r, Root: textRepo})
	require.NoError(t, err)

	r.EXPECT().
		RunBufferedWithEnv(gomock.Any(), textRepo, gomock.Nil(), textDocker, "buildx",
			textBuild,
			"--load", "--provenance=false", "--sbom=false", "-f", "d/Dockerfile",
			"--target", textRun,
			"--platform", "linux/amd64", "--build-arg", "A=1", "--build-arg", "B=2",
			"-t", "api:dev", ".").
		Return(interfaces.CmdResult{})
	require.NoError(t, c.Build(ctx, types.BuildSpec{
		Context:    ".",
		Dockerfile: "d/Dockerfile",
		Target:     textRun,
		Platform:   "linux/amd64",
		BuildArgs:  map[string]string{"B": "2", "A": "1"},
		Tags:       []string{"api:dev"},
	}))

	r.EXPECT().
		RunBufferedWithEnv(gomock.Any(), textRepo, gomock.Nil(), textDocker, textImage,
			"inspect", "gone:1").
		Return(exitWith(1, "Error: No such image: gone:1"))

	_, err = c.Inspect(ctx, "gone:1")

	testClientsDockerExecRest(ctx, t, r, c, err)
}

// testClientsDockerExecRest continues TestClientsDocker_Exec with its remaining steps.
func testClientsDockerExecRest(
	ctx context.Context,
	t *testing.T,
	r *mocks.MockCommandRunner,
	c interfaces.DockerClient,
	err error,
) {
	t.Helper()

	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))

	spec := types.ComposeSpec{
		Project: textDev, Files: []string{textAYaml, "b.yaml"}, Profiles: []string{"obs"},
		Services: []string{textAPI}, Env: []string{textX1}, RemoveVolumes: true,
	}
	compose := []any{
		textCompose,
		"-p",
		textDev,
		"-f",
		textAYaml,
		"-f",
		"b.yaml",
		"--profile",
		"obs",
	}
	r.EXPECT().RunBufferedWithEnv(gomock.Any(), textRepo, []string{textX1}, textDocker,
		append(
			compose,
			textUp,
			"-d",
			"--wait",
			textAPI,
		)...).
		Return(interfaces.CmdResult{})
	require.NoError(t, c.ComposeUp(ctx, spec))
	r.EXPECT().RunBufferedWithEnv(gomock.Any(), textRepo, []string{textX1}, textDocker,
		append(compose, textDown, "--volumes")...).Return(interfaces.CmdResult{})
	require.NoError(t, c.ComposeDown(ctx, spec))

	stub, err := docker.NewFromConfig(ctx, docker.Config{},
		docker.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)

	_, err = stub.Inspect(ctx, textX)
	assert.Equal(t, apperr.CodeNotFound, apperr.Code(err))
}
