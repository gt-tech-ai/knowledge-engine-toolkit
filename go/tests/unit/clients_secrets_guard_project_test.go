package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestGuardDomain tests the tenant guard on a resolved credential.
//
// Why this test is important:
//   - A credential for one tenant used against another cross-provisions them; a
//     credential with no domain cannot be proven safe.
//
// What it tests:
//   - The same domain (surrounding space ignored) passes; an empty or different
//     resolved domain is INVALID_INPUT naming the subject and both domains.
func TestGuardDomain(t *testing.T) {
	t.Parallel()
	require.NoError(
		t,
		secrets.GuardDomain(
			`credentials for "stg"`,
			"acme.auth0.com",
			" acme.auth0.com ",
		),
	)

	for _, got := range []string{"", "other.auth0.com"} {
		err := secrets.GuardDomain(`credentials for "stg"`, "acme.auth0.com", got)
		assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)
		assert.Contains(t, err.Error(), `"stg"`)
		assert.Contains(t, err.Error(), `"acme.auth0.com"`)
		assert.Contains(t, err.Error(), `"`+got+`"`)
	}
}

// TestProjectEnv tests projecting a secret's fields into a child environment.
//
// Why this test is important:
//   - A child process gets its credentials as environment entries and nowhere else;
//     one field a source cannot provide must not drop the fields it can, while a hard
//     read failure must stop the run.
//
// What it tests:
//   - Each resolved field becomes VAR=value, sorted by variable; a NOT_FOUND field is
//     skipped; any other error is returned.
func TestProjectEnv(t *testing.T) {
	t.Parallel()
	ref := types.Ref{Env: "stg", Class: "mgmt"}
	fields := map[string]string{
		"client_secret": "AUTH0_CLIENT_SECRET",
		"client_id":     "AUTH0_CLIENT_ID",
		"password":      "E2E_PASSWORD",
	}
	src := mocks.NewMockSource(gomock.NewController(t))
	at := func(field string) types.Ref { r := ref; r.Field = field; return r }
	src.EXPECT().Get(gomock.Any(), at("client_id")).Return(types.NewSecret("id"), nil)
	src.EXPECT().
		Get(gomock.Any(), at("client_secret")).
		Return(types.NewSecret("sec"), nil)
	src.EXPECT().Get(gomock.Any(), at("password")).
		Return(types.Secret{}, apperr.New(apperr.CodeNotFound, "no password"))

	got, err := secrets.ProjectEnv(t.Context(), src, ref, fields)
	require.NoError(t, err)
	assert.Equal(t, []string{"AUTH0_CLIENT_ID=id", "AUTH0_CLIENT_SECRET=sec"}, got)

	hard := mocks.NewMockSource(gomock.NewController(t))
	hard.EXPECT().Get(gomock.Any(), gomock.Any()).
		Return(types.Secret{}, apperr.New(apperr.CodeUnavailable, "down")).AnyTimes()
	_, err = secrets.ProjectEnv(t.Context(), hard, ref, fields)
	assert.True(t, apperr.Is(err, apperr.CodeUnavailable), "got %v", err)
}
