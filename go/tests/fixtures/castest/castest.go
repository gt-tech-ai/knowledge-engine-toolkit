// Package castest is the reusable NodeStore and RefStore conformance suite
// (RunNodeStore, RunRefStore) a backend runs against its own store; the engine runs it
// against the stub and the postgres cas kinds. Each case scopes its keys by the
// subtest name, so one store (and one table) serves every case.
package castest

import (
	"context"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cas"
)

// domain is the hash domain the suite's nodes and refs are addressed under.
const domain = types.Domain("castest/v1")

// bodyOne and bodyTwo are two distinct node bodies.
const (
	bodyOne = `{"a":1}`
	bodyTwo = `{"a":2}`
)

// labelA and labelC name the hashes the ref cases move between.
const (
	labelA = "a"
	labelB = "b"
	labelC = "c"
)

// racers is how many goroutines race to move one ref in the compare-and-swap case.
const racers = 8

// nodeSuite is the state RunNodeStore's cases share.
type nodeSuite struct {
	// newCtx returns the context each store call runs under.
	newCtx func() context.Context
	// store is the NodeStore under test.
	store interfaces.NodeStore
}

// refSuite is the state RunRefStore's cases share.
type refSuite struct {
	// newCtx returns the context each store call runs under.
	newCtx func() context.Context
	// store is the RefStore under test.
	store interfaces.RefStore
}

// RunNodeStore checks the NodeStore contract: nodes are insert-only, a repeat put is
// idempotent, and a missing node is CodeNotFound.
//
// Why these checks matter:
//   - A node's key is the hash of its content; a store that overwrote on a repeat put,
//     or failed it, would let a retried writer change or break history.
//
// What they assert:
//   - a new node reports Stored and reads back;
//   - the same key and body is a no-op (not Stored, not Conflict);
//   - the same key with another body keeps the first body and reports Conflict;
//   - a key with no node is CodeNotFound.
func RunNodeStore(
	t *testing.T,
	newCtx func() context.Context,
	store interfaces.NodeStore,
) {
	t.Helper()

	suite := nodeSuite{newCtx: newCtx, store: store}

	t.Run("PutNewStoresAndReadsBack", suite.putNew)
	t.Run("PutSameBodyIsNoop", suite.putSame)
	t.Run("PutOtherBodyKeepsFirst", suite.putOther)
	t.Run("GetMissingIsNotFound", suite.missing)
}

// RunRefStore checks the RefStore contract: a ref moves only from the hash the caller
// saw, a lost race is false with no error, and a missing ref is CodeNotFound.
//
// Why these checks matter:
//   - Two writers that both read version A must not both win: the loser has to learn it
//     lost, not overwrite the winner.
//
// What they assert:
//   - a zero From creates the ref once, and a second create loses;
//   - a move from a stale hash loses and leaves the ref unchanged;
//   - a move from the current hash wins;
//   - of eight concurrent moves from one hash exactly one wins;
//   - a ref that was never created is CodeNotFound.
func RunRefStore(t *testing.T, newCtx func() context.Context, store interfaces.RefStore) {
	t.Helper()

	suite := refSuite{newCtx: newCtx, store: store}

	t.Run("CreateOnce", suite.createOnce)
	t.Run("StaleMoveLoses", suite.staleMoveLoses)
	t.Run("CurrentMoveWins", suite.currentMoveWins)
	t.Run("RacingMovesOneWins", suite.racingMovesOneWins)
	t.Run("MissingIsNotFound", suite.missing)
}

// node builds the node for body under scope: the body is stored in canonical form and
// keyed by its hash.
func node(t *testing.T, scope, body string) types.Node {
	t.Helper()

	canonical, err := cas.Canonical([]byte(body))
	require.NoError(t, err)

	return types.Node{
		Body: canonical,
		Key:  types.NodeKey{Scope: scope, Hash: cas.Sum(domain, canonical)},
	}
}

// canonical returns body in canonical form, so a jsonb-backed store that reformats
// what it stores still compares equal.
func canonical(t *testing.T, body []byte) string {
	t.Helper()

	out, err := cas.Canonical(body)
	require.NoError(t, err)

	return string(out)
}

// hashOf returns the hash of one distinct value per label.
func hashOf(t *testing.T, label string) types.Hash {
	t.Helper()

	out, err := cas.HashOf(domain, map[string]string{"label": label})
	require.NoError(t, err)

	return out
}

// putNew stores a new node and reads it back.
func (s nodeSuite) putNew(t *testing.T) {
	t.Parallel()

	stored := node(t, t.Name(), bodyOne)

	result, err := s.store.Put(s.newCtx(), stored)
	require.NoError(t, err)
	assert.Equal(t, types.PutResult{Stored: true, Conflict: false}, result)

	got, err := s.store.Get(s.newCtx(), stored.Key)
	require.NoError(t, err)
	assert.Equal(t, canonical(t, stored.Body), canonical(t, got.Body))
	assert.Equal(t, stored.Key, got.Key)
}

// putSame puts one node twice.
func (s nodeSuite) putSame(t *testing.T) {
	t.Parallel()

	stored := node(t, t.Name(), bodyOne)

	_, err := s.store.Put(s.newCtx(), stored)
	require.NoError(t, err)

	result, err := s.store.Put(s.newCtx(), stored)
	require.NoError(t, err)
	assert.Equal(t, types.PutResult{Stored: false, Conflict: false}, result)
}

// putOther puts a second body under a key already holding the first.
func (s nodeSuite) putOther(t *testing.T) {
	t.Parallel()

	first := node(t, t.Name(), bodyOne)
	other := node(t, t.Name(), bodyTwo)

	other.Key = first.Key

	_, err := s.store.Put(s.newCtx(), first)
	require.NoError(t, err)

	result, err := s.store.Put(s.newCtx(), other)
	require.NoError(t, err)
	assert.Equal(t, types.PutResult{Stored: false, Conflict: true}, result)

	got, err := s.store.Get(s.newCtx(), first.Key)
	require.NoError(t, err)
	assert.Equal(t, canonical(t, first.Body), canonical(t, got.Body))
}

// missing reads a key nothing was put under.
func (s nodeSuite) missing(t *testing.T) {
	t.Parallel()

	_, err := s.store.Get(s.newCtx(), node(t, t.Name(), bodyOne).Key)
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound))
}

// key is the ref this subtest owns.
func key(t *testing.T) types.RefKey {
	t.Helper()

	return types.RefKey{Scope: t.Name(), Name: "pointer"}
}

// move is the RefMove of the subtest's ref that goes from one hash to another.
func move(t *testing.T, from, to types.Hash) *types.RefMove {
	t.Helper()

	return &types.RefMove{Key: key(t), From: from, To: to}
}

// createOnce creates a ref, then tries to create it again.
func (s refSuite) createOnce(t *testing.T) {
	t.Parallel()

	moved, err := s.store.Move(s.newCtx(), move(t, types.Hash{}, hashOf(t, labelA)))
	require.NoError(t, err)
	assert.True(t, moved)

	moved, err = s.store.Move(s.newCtx(), move(t, types.Hash{}, hashOf(t, labelB)))
	require.NoError(t, err)
	assert.False(t, moved)

	got, err := s.store.Ref(s.newCtx(), key(t))
	require.NoError(t, err)
	assert.Equal(t, hashOf(t, labelA), got)
}

// staleMoveLoses moves a ref from a hash it no longer names.
func (s refSuite) staleMoveLoses(t *testing.T) {
	t.Parallel()

	s.moveFrom(t, labelB, false, labelA)
}

// currentMoveWins moves a ref from the hash it names.
func (s refSuite) currentMoveWins(t *testing.T) {
	t.Parallel()

	s.moveFrom(t, labelA, true, labelC)
}

// moveFrom creates the subtest's ref at label "a", moves it from label from to label
// "c", and checks the move's result and the label the ref names afterwards.
func (s refSuite) moveFrom(t *testing.T, from string, wantMoved bool, wantRef string) {
	t.Helper()

	_, err := s.store.Move(s.newCtx(), move(t, types.Hash{}, hashOf(t, labelA)))
	require.NoError(t, err)

	moved, err := s.store.Move(s.newCtx(), move(t, hashOf(t, from), hashOf(t, labelC)))
	require.NoError(t, err)
	assert.Equal(t, wantMoved, moved)

	got, err := s.store.Ref(s.newCtx(), key(t))
	require.NoError(t, err)
	assert.Equal(t, hashOf(t, wantRef), got)
}

// racingMovesOneWins races racers moves from one hash to racers different hashes.
func (s refSuite) racingMovesOneWins(t *testing.T) {
	t.Parallel()

	_, err := s.store.Move(s.newCtx(), move(t, types.Hash{}, hashOf(t, "start")))
	require.NoError(t, err)

	var (
		group sync.WaitGroup
		wins  = make([]bool, racers)
		fails = make([]error, racers)
	)

	for racer := range racers {
		group.Go(func() {
			wins[racer], fails[racer] = s.store.Move(
				s.newCtx(),
				move(t, hashOf(t, "start"), hashOf(t, strconv.Itoa(racer))),
			)
		})
	}

	group.Wait()

	won := 0

	for racer := range racers {
		require.NoError(t, fails[racer])

		if wins[racer] {
			won++
		}
	}

	assert.Equal(t, 1, won)
}

// missing reads a ref that was never created.
func (s refSuite) missing(t *testing.T) {
	t.Parallel()

	_, err := s.store.Ref(s.newCtx(), key(t))
	require.Error(t, err)
	assert.True(t, apperr.Is(err, apperr.CodeNotFound))
}
