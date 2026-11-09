package tracer

const (
	// schemeSeparatorLen is the offset value 3.
	schemeSeparatorLen = 3

	// minTraceparentParts is the named value used where min traceparent parts applies.
	minTraceparentParts = 2

	// fullSampleRate is the named value used where full sample rate applies.
	fullSampleRate = 1.0
)

const (
	// traceIDHexLen is the named value used where trace idhex len applies.
	traceIDHexLen = 32
)
