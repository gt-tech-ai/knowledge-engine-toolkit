//go:build integration

package integration_test

const (
	// appendSeqArg3 is the appendSeqArg value 3.
	appendSeqArg3 = 3

	// appendSeqArg5 is the appendSeqArg value 5.
	appendSeqArg5 = 5

	// capacity43 is the capacity value 43.
	capacity43 = 43

	// capacity5 is the capacity value 5.
	capacity5 = 5

	// capacity80 is the capacity value 80.
	capacity80 = 80

	// delta1em9 is the delta value 1e-9.
	delta1em9 = 1e-9

	// enqueueNArg1000 is the enqueueNArg value 1000.
	enqueueNArg1000 = 1000

	// enqueueNArg25 is the enqueueNArg value 25.
	enqueueNArg25 = 25

	// enqueueNArg5 is the enqueueNArg value 5.
	enqueueNArg5 = 5

	// equalArg2 is the equalArg value 2.
	equalArg2 = 2

	// equalArg3 is the equalArg value 3.
	equalArg3 = 3

	// equalArg43 is the equalArg value 43.
	equalArg43 = 43

	// expirySeconds300 is the expirySeconds value 300.
	expirySeconds300 = 300

	// inDeltaArg17p0 is the inDeltaArg value 17.0.
	inDeltaArg17p0 = 17.0

	// inDeltaArg1p0 is the inDeltaArg value 1.0.
	inDeltaArg1p0 = 1.0

	// inDeltaArg25p0 is the inDeltaArg value 25.0.
	inDeltaArg25p0 = 25.0

	// inDeltaArg2p0 is the inDeltaArg value 2.0.
	inDeltaArg2p0 = 2.0

	// inDeltaArg30p0 is the inDeltaArg value 30.0.
	inDeltaArg30p0 = 30.0

	// inDeltaArg41p0 is the inDeltaArg value 41.0.
	inDeltaArg41p0 = 41.0

	// intN2 is the intN value 2.
	intN2 = 2

	// intNArg2 is the intNArg value 2.
	intNArg2 = 2

	// intNArg4 is the intNArg value 4.
	intNArg4 = 4

	// iterations10 is the iterations value 10.
	iterations10 = 10

	// iterations2 is the iterations value 2.
	iterations2 = 2

	// iterations30 is the iterations value 30.
	iterations30 = 30

	// iterations43 is the iterations value 43.
	iterations43 = 43

	// iterations5 is the iterations value 5.
	iterations5 = 5

	// iterations80 is the iterations value 80.
	iterations80 = 80

	// length2 is the length value 2.
	length2 = 2

	// length3 is the length value 3.
	length3 = 3

	// lessArg10 is the lessArg value 10.
	lessArg10 = 10

	// lessOrEqualArg2 is the lessOrEqualArg value 2.
	lessOrEqualArg2 = 2

	// limit1000 is the limit value 1000.
	limit1000 = 1000

	// limit2 is the limit value 2.
	limit2 = 2

	// maxSize100 is the maxSize value 100.
	maxSize100 = 100

	// maxSize3 is the maxSize value 3.
	maxSize3 = 3

	// millis120 is the millis value 120.
	millis120 = 120

	// millis300 is the millis value 300.
	millis300 = 300

	// millis60 is the millis value 60.
	millis60 = 60

	// pageSize2 is the pageSize value 2.
	pageSize2 = 2

	// pageSize7 is the pageSize value 7.
	pageSize7 = 7

	// partitions2 is the partitions value 2.
	partitions2 = 2

	// perFile3 is the perFile value 3.
	perFile3 = 3

	// seconds2 is the seconds value 2.
	seconds2 = 2

	// seconds20 is the seconds value 20.
	seconds20 = 20

	// seed42 is the seed value 42.
	seed42 = 42

	// size2 is the size value 2.
	size2 = 2

	// size4 is the size value 4.
	size4 = 4

	// size8 is the size value 8.
	size8 = 8

	// splitNArg2 is the splitNArg value 2.
	splitNArg2 = 2

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

	// tokens100 is the tokens value 100.
	tokens100 = 100

	// value10 is the number 10.
	value10 = 10

	// value40 is the number 40.
	value40 = 40

	// want0p0 is the want value 0.0.
	want0p0 = 0.0

	// wantCount2 is the wantCount value 2.
	wantCount2 = 2

	// wantLastErrLen16 is the wantLastErrLen value 16.
	wantLastErrLen16 = 16

	// wantPending900 is the wantPending value 900.
	wantPending900 = 900

	// wantRecsLen2 is the wantRecsLen value 2.
	wantRecsLen2 = 2

	// wantWantLen5 is the wantWantLen value 5.
	wantWantLen5 = 5

	// year2025 is the year value 2025.
	year2025 = 2025

	// year2026 is the year value 2026.
	year2026 = 2026
)

const (
	// day2 is the day value 2.
	day2 = 2

	// month10 is the month value 10.
	month10 = 10

	// month3 is the month value 3.
	month3 = 3

	// month9 is the month value 9.
	month9 = 9

	// textM is the literal "m".
	textM = "m"

	// textMsg1 is the literal "msg-1".
	textMsg1 = "msg-1"

	// textMsg5 is the literal "msg-5".
	textMsg5 = "msg-5"

	// textT3 is the literal "t-3".
	textT3 = "t-3"

	// tokens17 is the tokens value 17.
	tokens17 = 17

	// tokens25 is the tokens value 25.
	tokens25 = 25
)

const (
	// day10 is the day value 10.
	day10 = 10

	// day25 is the day value 25.
	day25 = 25

	// day4 is the day value 4.
	day4 = 4

	// day7 is the day value 7.
	day7 = 7

	// day8 is the day value 8.
	day8 = 8

	// day9 is the day value 9.
	day9 = 9

	// hour3 is the hour value 3.
	hour3 = 3
)

const (
	// hour10 is the hour value 10.
	hour10 = 10

	// hour14 is the hour value 14.
	hour14 = 14

	// hour2 is the hour value 2.
	hour2 = 2

	// hour20 is the hour value 20.
	hour20 = 20

	// hour9 is the hour value 9.
	hour9 = 9

	// min4 is the min value 4.
	min4 = 4
)

const (
	// min5 is the min value 5.
	min5 = 5

	// sec5 is the sec value 5.
	sec5 = 5
)
