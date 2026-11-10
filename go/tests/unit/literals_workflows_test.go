package unit_test

// Values the workflows tests share.
const (
	// textAudit is the literal "audit".
	textAudit = "audit"

	// textLane is the literal "lane".
	textLane = "lane"

	// outboxRecordsInput is the n passed to outboxRecords in
	// TestRelay_CancelledRunLeavesFailedRowsLeased, TestRelay_DrainStopsOnTheFirstError
	// and 7 more, and the attempts passed to outboxRecords in
	// TestRelay_ParksAfterMaxAttempts and testRelayReportsDepthAndLagExpectsCounters.
	outboxRecordsInput = 2

	// relayConfigInput is the maxAttempts passed to relayConfig in
	// TestRelay_MarksSentOnSuccess, TestRelay_PartialBatchFailureRetriesOnlyFailedRows
	// and 2 more.
	relayConfigInput = 5
)
