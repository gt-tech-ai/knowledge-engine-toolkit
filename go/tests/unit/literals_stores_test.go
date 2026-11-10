package unit_test

// Values the stores tests share.
const (
	// textPostgresUnused is the literal "postgres://unused".
	textPostgresUnused = "postgres://unused"

	// claimInput is the limit passed to Claim in
	// TestOutboxStoreNewFromConfig_StubBootsAndIsDecorated,
	// TestPgstore_RefusesWritesOutsideATransaction and 2 more.
	claimInput = 10

	// iterationsPartitions is an element of a literal in TestRelayConfig_Partitions, and
	// an element of a []int in TestPgstore_ValidatesPartitions.
	iterationsPartitions = 3

	// relayPartitionCount is the value assigned to base.Partitions in
	// TestPgstore_ValidatesPartitions, and the value assigned to cfg.Partitions in
	// TestRelayConfig_Partitions.
	relayPartitionCount = 3
)
