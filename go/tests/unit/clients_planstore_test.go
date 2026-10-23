package unit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/planstore"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// planSet is the plan set every store test saves and loads.
func planSet(dir string) types.PlanSet {
	return types.PlanSet{
		Dir: dir, Bucket: "state", Prefix: "plans/stg/", RunID: "42-1", Owner: "stg",
		File: "tfplan.tfplan", Manifest: "plan-manifest.json",
	}
}

// newObjectStore builds the object-store plan store over storage.
func newObjectStore(t *testing.T, storage interfaces.StorageClient) interfaces.PlanStore {
	t.Helper()
	ps, err := planstore.NewFromConfig(t.Context(),
		planstore.Config{Kind: planstore.KindObject},
		planstore.Deps{Decorations: fixtures.Decorations(), Storage: storage})
	require.NoError(t, err)
	return ps
}

// writePlan writes body as unit's plan under dir.
func writePlan(t *testing.T, dir, unit, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, unit), 0o700))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(dir, unit, "tfplan.tfplan"), []byte(body), 0o600),
	)
}

// TestPlanStore_RoundTrip tests that saved plans come back byte for byte.
//
// Why this test is important:
//   - A plan job and its apply job run on different machines; the apply must apply
//     exactly the plans the plan job made, and only those.
//
// What it tests:
//   - Save uploads each unit's plan to <Prefix><RunID>/<unit>/<File> and writes the
//     manifest; Load in an empty directory downloads each listed plan, restoring its
//     bytes with 0o600 files under 0o700 directories.
func TestPlanStore_RoundTrip(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	writePlan(t, src, "vpc", "plan-vpc")
	writePlan(t, src, "eks", "plan-eks")

	stored := map[string][]byte{}
	storage := mocks.NewMockStorageClient(gomock.NewController(t))
	storage.EXPECT().
		Upload(gomock.Any(), "state", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, key string, body io.Reader, _ string) error {
			b, err := io.ReadAll(body)
			stored[key] = b
			return apperr.Wrap(err, apperr.CodeInternal, "read the upload body")
		}).
		Times(2)
	storage.EXPECT().Download(gomock.Any(), "state", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, key string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(stored[key])), nil
		}).Times(2)
	ps := newObjectStore(t, storage)

	require.NoError(t, ps.Save(t.Context(), planSet(src)))
	assert.Contains(t, stored, "plans/stg/42-1/vpc/tfplan.tfplan")
	assert.Contains(t, stored, "plans/stg/42-1/eks/tfplan.tfplan")

	dst := t.TempDir()
	manifest, err := os.ReadFile(filepath.Join(src, "plan-manifest.json"))
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(dst, "plan-manifest.json"), manifest, 0o600),
	)
	require.NoError(t, ps.Load(t.Context(), planSet(dst)))
	got, err := os.ReadFile(filepath.Join(dst, "vpc", "tfplan.tfplan"))
	require.NoError(t, err)
	assert.Equal(t, "plan-vpc", string(got))
	info, err := os.Stat(filepath.Join(dst, "vpc", "tfplan.tfplan"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Join(dst, "vpc"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

// manifestFor writes a manifest for owner listing unit → key, sha256 into dir.
func manifestFor(t *testing.T, dir, owner string, units ...[3]string) {
	t.Helper()
	type unit struct {
		Unit   string `json:"unit"`
		Key    string `json:"key"`
		SHA256 string `json:"sha256"`
	}
	m := struct {
		Owner string `json:"owner"`
		Units []unit `json:"units"`
	}{Owner: owner}
	for _, u := range units {
		m.Units = append(m.Units, unit{Unit: u[0], Key: u[1], SHA256: u[2]})
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(
		t,
		os.WriteFile(filepath.Join(dir, "plan-manifest.json"), data, 0o600),
	)
}

// TestPlanStore_LoadRefusesUntrustedPlans tests the checks Load makes before anything
// is applied.
//
// Why this test is important:
//   - The manifest is the apply's only trust anchor: a swapped plan, another owner's
//     manifest, a key outside the set's prefix or a plan that expired must each stop
//     the apply rather than apply the wrong thing or nothing silently.
//
// What it tests:
//   - A digest mismatch is CONFLICT naming the unit and removes the download; an
//     expired (NOT_FOUND) plan is NOT_FOUND naming the object; another owner's manifest
//     and a key outside the prefix are INVALID_INPUT with no download.
func TestPlanStore_LoadRefusesUntrustedPlans(t *testing.T) {
	t.Parallel()
	key := "plans/stg/42-1/vpc/tfplan.tfplan"

	t.Run("sha mismatch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		manifestFor(t, dir, "stg", [3]string{"vpc", key, "00"})
		storage := mocks.NewMockStorageClient(gomock.NewController(t))
		storage.EXPECT().Download(gomock.Any(), "state", key).
			Return(io.NopCloser(bytes.NewReader([]byte("swapped"))), nil)
		err := newObjectStore(t, storage).Load(t.Context(), planSet(dir))
		assert.True(t, apperr.Is(err, apperr.CodeConflict), "got %v", err)
		assert.Contains(t, err.Error(), "vpc")
		_, statErr := os.Stat(filepath.Join(dir, "vpc", "tfplan.tfplan"))
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})

	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		manifestFor(t, dir, "stg", [3]string{"vpc", key, "00"})
		storage := mocks.NewMockStorageClient(gomock.NewController(t))
		storage.EXPECT().Download(gomock.Any(), "state", key).
			Return(nil, apperr.NotFound("object state/"+key))
		err := newObjectStore(t, storage).Load(t.Context(), planSet(dir))
		assert.True(t, apperr.Is(err, apperr.CodeNotFound), "got %v", err)
		assert.Contains(t, err.Error(), key)
	})

	for name, units := range map[string][3]string{
		"another owner":      {"vpc", key, "00"},
		"key outside prefix": {"vpc", "plans/prod/42-1/vpc/tfplan.tfplan", "00"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			owner := "stg"
			if name == "another owner" {
				owner = "prod"
			}
			manifestFor(t, dir, owner, units)
			storage := mocks.NewMockStorageClient(gomock.NewController(t))
			err := newObjectStore(t, storage).Load(t.Context(), planSet(dir))
			assert.True(t, apperr.Is(err, apperr.CodeInvalidInput), "got %v", err)
		})
	}
}

// TestPlanStore_Kinds tests the factory's kinds.
//
// Why this test is important:
//   - A developer's run keeps its plans where they were written, and must not need an
//     object store; an object store with no storage client must fail before a plan.
//
// What it tests:
//   - The empty kind (local) saves and loads nothing and needs no storage; the object
//     kind with no storage client and an unknown kind are INVALID_INPUT.
func TestPlanStore_Kinds(t *testing.T) {
	t.Parallel()
	local, err := planstore.NewFromConfig(t.Context(), planstore.Config{},
		planstore.Deps{Decorations: fixtures.Decorations()})
	require.NoError(t, err)
	dir := t.TempDir()
	writePlan(t, dir, "vpc", "plan")
	require.NoError(t, local.Save(t.Context(), planSet(dir)))
	require.NoError(t, local.Load(t.Context(), planSet(dir)))
	_, statErr := os.Stat(filepath.Join(dir, "plan-manifest.json"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "local writes no manifest")

	for _, cfg := range []planstore.Config{{Kind: planstore.KindObject}, {Kind: "ftp"}} {
		_, err := planstore.NewFromConfig(t.Context(), cfg,
			planstore.Deps{Decorations: fixtures.Decorations()})
		assert.True(
			t,
			apperr.Is(err, apperr.CodeInvalidInput),
			"%s: got %v",
			cfg.Kind,
			err,
		)
	}
}
