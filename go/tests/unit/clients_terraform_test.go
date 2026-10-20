package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/terraform"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// terraformShow is `terraform show -json` covering each action kind.
const terraformShow = `{"format_version":"1.2","resource_changes":[
 {"address":"aws_s3_bucket.a","type":"aws_s3_bucket","name":"a",
  "change":{"actions":["create"]}},
 {"address":"aws_iam_role.r","type":"aws_iam_role","name":"r",
  "change":{"actions":["update"]}},
 {"address":"aws_sqs_queue.q","type":"aws_sqs_queue","name":"q",
  "change":{"actions":["delete"]}},
 {"address":"aws_db_instance.d","type":"aws_db_instance","name":"d",
  "change":{"actions":["delete","create"]}},
 {"address":"aws_vpc.v","type":"aws_vpc","name":"v","change":{"actions":["no-op"]}},
 {"address":"data.aws_caller_identity.c","type":"aws_caller_identity","name":"c",
  "change":{"actions":["read"]}}]}`

// TestClientsTerraform_PlanAndApply tests the terraform binary backend.
//
// Why this test is important:
//   - The plan summary is what a reviewer reads in the PR comment and what the deploy
//     gate checks before applying; a miscounted replacement hides a destroy.
//
// What it tests:
//   - Plan runs plan into the out file then shows it; the parsed plan lists create,
//     update, delete and replace, drops no-op and read, and counts a replacement as an
//     add and a destroy; apply runs the saved plan; malformed show output is
//     INVALID_INPUT; the stub plans nothing.
func TestClientsTerraform_PlanAndApply(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	r := mocks.NewMockCommandRunner(gomock.NewController(t))
	c, err := terraform.NewFromConfig(ctx, terraform.Config{Kind: terraform.KindExec},
		terraform.Deps{Decorations: fixtures.Decorations(), Runner: r})
	require.NoError(t, err)

	gomock.InOrder(
		r.EXPECT().
			RunBuffered(gomock.Any(), "envs/stg", "terraform", "plan", "-input=false",
				"-out=tf.plan").
			Return(interfaces.CmdResult{}),
		r.EXPECT().RunBuffered(gomock.Any(), "envs/stg", "terraform", "show", "-json",
			"tf.plan").Return(interfaces.CmdResult{Stdout: []byte(terraformShow)}),
	)
	p, err := c.Plan(ctx, "envs/stg", "tf.plan")
	require.NoError(t, err)
	assert.Equal(t, 2, p.Add)
	assert.Equal(t, 1, p.Change)
	assert.Equal(t, 2, p.Destroy)
	require.Len(t, p.Changes, 4)
	assert.Equal(t, types.ResourceChange{
		Address: "aws_db_instance.d", Type: "aws_db_instance", Name: "d",
		Actions: []string{"delete", "create"},
	}, p.Changes[3])

	r.EXPECT().RunBuffered(gomock.Any(), "envs/stg", "terraform", "apply", "-input=false",
		"tf.plan").Return(interfaces.CmdResult{})
	require.NoError(t, c.Apply(ctx, "envs/stg", "tf.plan"))

	r.EXPECT().RunBuffered(gomock.Any(), "envs/stg", "terraform", "show", "-json", "x").
		Return(interfaces.CmdResult{Stdout: []byte("not json")})
	_, err = c.Show(ctx, "envs/stg", "x")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	stub, err := terraform.NewFromConfig(ctx, terraform.Config{},
		terraform.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	p, err = stub.Plan(ctx, "d", "o")
	require.NoError(t, err)
	assert.True(t, p.Empty())
}
