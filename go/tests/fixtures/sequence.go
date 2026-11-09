package fixtures

// Sequence is a deterministic pseudo-random stream for generated test data. The same
// seed always yields the same values, so a failing run reproduces exactly; it is not a
// source of security randomness.
type Sequence struct {
	// state is the generator's current 32-bit state.
	state uint32
}

// NewSequence returns a Sequence that starts from seed.
func NewSequence(seed uint32) *Sequence { return &Sequence{state: seed} }

// IntN returns a value in [0, n); n must be positive.
func (s *Sequence) IntN(n int) int { return int(s.next()) % n }

// Float64 returns a value in [0, 1).
func (s *Sequence) Float64() float64 { return float64(s.next()) / (1 << 24) }

// next advances the generator (a linear congruential step) and returns its high 24 bits,
// which are the well-mixed ones.
func (s *Sequence) next() uint32 {
	s.state = s.state*1664525 + 1013904223

	return s.state >> 8
}
