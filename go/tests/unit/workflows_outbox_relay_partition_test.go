package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/outbox"
)

// TestRelayConfig_Partitions tests the relay's partition validation.
//
// Why this test is important:
//   - A relay pointed at a partition that does not exist claims nothing forever,
//     silently; boot must stop instead
//
// What it tests:
//   - the default (zero partitions, partition 0) validates; partition 2 of 3
//     validates; partition 3 of 3, a negative partition and negative partitions are
//     CodeInvalidInput
func TestRelayConfig_Partitions(t *testing.T) {
	t.Parallel()
	require.NoError(t, new(outbox.DefaultRelayConfig(textAudit)).Validate())

	cfg := outbox.DefaultRelayConfig(textAudit)

	cfg.Partitions, cfg.Partition = partitions3, partition2
	require.NoError(t, cfg.Validate())

	for _, bad := range [][iterations2]int{{iterations3, iterations3}, {
		iterations3,
		-1,
	}, {-1, 0}, {0, 1}} {
		cfg.Partitions, cfg.Partition = bad[0], bad[1]
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(cfg.Validate()), textV2, bad)
	}
}

// TestRelay_ClaimsItsPartition tests that a relay claims only its partition.
//
// Why this test is important:
//   - N relays drain N partitions in parallel only if each claims its own; a relay
//     claiming partition 0 regardless would leave the others undrained
//
// What it tests:
//   - a relay configured for partition 2 of 3 calls Claim with partition 2
func TestRelay_ClaimsItsPartition(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockOutboxStore(ctrl)
	cfg := relayConfig(sendBatch10, 1, maxAttempts3)

	cfg.Partitions, cfg.Partition = partitions3, partition2

	store.EXPECT().Claim(gomock.Any(), textAudit, 2, 100).Return(nil, nil)

	relay := outbox.NewRelay(&cfg, relayTx(t), store, mocks.NewMockOutboxSink(ctrl), nil,
		nil, nil, fixedClock())

	require.NoError(t, relay.RunOnce(context.Background()))
}
