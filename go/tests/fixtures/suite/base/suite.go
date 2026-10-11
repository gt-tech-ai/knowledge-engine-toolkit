// Package base provides the base test suite with shared utilities.
package base

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// TestSuite provides common test infrastructure for all suites.
type TestSuite struct {
	// Suite is the embedded testify suite providing assertions and lifecycle hooks.
	suite.Suite
	// TempDir is a per-suite temporary directory created in SetupSuite.
	TempDir string
}

// SetupSuite creates shared resources for the suite.
func (s *TestSuite) SetupSuite() {
	s.TempDir = s.T().TempDir()
}

// IsIntegration returns true if integration tests should run.
// Skips the calling test if not in integration mode.
func (s *TestSuite) IsIntegration() bool {
	if testing.Short() {
		s.T().Skip("skipping integration test in short mode")
		return false
	}
	if os.Getenv("INTEGRATION") == "" && os.Getenv("CI") == "" {
		s.T().Skip("set INTEGRATION=1 or CI=1 to run integration tests")
		return false
	}
	return true
}

// --------- Test Helpers ----------

// TimesEqual asserts that the provided timestamps are approximately equal.
func (s *TestSuite) TimesEqual(expected, actual time.Time) {
	s.WithinDuration(expected, actual, 10*time.Millisecond)
}

// TimesNotEqual asserts that the provided times are not equal.
func (s *TestSuite) TimesNotEqual(expected, actual time.Time) {
	s.T().Helper()

	equal := expected.Equal(actual)
	if equal {
		s.Failf(
			"Times are equal",
			"Expected %v not to equal %v",
			actual.String(),
			expected.String(),
		)
	}
}

// AssertDomainError asserts that the provided error is an application domain error
// with the expected error code.
func (s *TestSuite) AssertDomainError(err error, expectedCode apperr.ErrorCode) {
	s.T().Helper()

	s.Truef(
		apperr.Is(err, expectedCode),
		"expected app %s error, got %+v",
		string(expectedCode),
		err,
	)
}
