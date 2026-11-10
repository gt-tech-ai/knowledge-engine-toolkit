package outbox

const (
	// noLag is the named value used where no lag applies.
	noLag = 0.0

	// backoffHalfDivisor is the named value used where backoff half divisor applies.
	backoffHalfDivisor = 2

	// textLane is the literal "lane".
	textLane = "lane"

	// backoffFactor multiplies the delay after each failed pass.
	backoffFactor = 2
)
