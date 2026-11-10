//go:build integration

package integration_test

const (
	// textAnyKey is the literal "any-key".
	textAnyKey = "any-key"

	// textAudit is the literal "audit".
	textAudit = "audit"

	// textBurst is the literal "burst".
	textBurst = "burst"

	// textChanA is the literal "chan.A".
	textChanA = "chan.A"

	// textD is the literal "d".
	textD = "d"

	// textDelOneMd is the literal "del/one.md".
	textDelOneMd = "del/one.md"

	// textEventType is the literal "event_type".
	textEventType = "event_type"

	// textEvents is the literal "events".
	textEvents = "events"

	// textK is the literal "k".
	textK = "k"

	// textK1 is the literal "k1".
	textK1 = "k1"

	// textKrChan is the literal "kr.chan".
	textKrChan = "kr.chan"

	// textLoadedValue is the literal "loaded-value".
	textLoadedValue = "loaded-value"

	// textMinio is the literal "minio".
	textMinio = "minio"

	// textModel is the literal "model".
	textModel = "model"

	// textMsg3 is the literal "msg-3".
	textMsg3 = "msg-3"

	// textMsg4 is the literal "msg-4".
	textMsg4 = "msg-4"

	// textMsg8 is the literal "msg-8".
	textMsg8 = "msg-8"

	// textOrg1 is the literal "org-1".
	textOrg1 = "org-1"

	// textRtChan is the literal "rt.chan".
	textRtChan = "rt.chan"

	// textSingle is the literal "single".
	textSingle = "single"

	// textT1 is the literal "t-1".
	textT1 = "t-1"

	// textT2 is the literal "t-2".
	textT2 = "t-2"

	// textTeam is the literal "team".
	textTeam = "team"

	// textTextMarkdown is the literal "text/markdown".
	textTextMarkdown = "text/markdown"

	// textTokensIn is the literal "tokens_in".
	textTokensIn = "tokens_in"

	// textUsEast1 is the literal "us-east-1".
	textUsEast1 = "us-east-1"

	// textV is the literal "v".
	textV = "v"
)

const (
	// textM is the literal "m".
	textM = "m"

	// textMsg1 is the literal "msg-1".
	textMsg1 = "msg-1"

	// textMsg5 is the literal "msg-5".
	textMsg5 = "msg-5"

	// textT3 is the literal "t-3".
	textT3 = "t-3"
)

const (
	// dateInput is the year passed to Date in
	// TestCassandra_AggregatesAcrossBucketBoundaries,
	// TestCassandra_CrashBetweenWritesDoesNotLoseMeasures and 6 more.
	dateInput = 2026

	// initialOpTimeout is the factor multiplying time.Second in SetupTest and newCache.
	initialOpTimeout = 2

	// seconds is the factor multiplying time.Second in SetupSuite.
	seconds = 20
)
