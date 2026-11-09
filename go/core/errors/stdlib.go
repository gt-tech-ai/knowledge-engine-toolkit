package errors

import stderrors "errors"

// Standard library re-exports.
//
// These allow all packages to import go/core/errors exclusively,
// without needing a separate import of the standard "errors" package.

// As finds the first error in err's tree that matches target, and if one
// is found, sets target to that error value and returns true.
// Re-export of errors.As.
func As(err error, target any) bool { return stderrors.As(err, target) }

// StdIs reports whether any error in err's tree matches target.
// Re-export of errors.Is. Named StdIs to avoid collision with the
// package-level Is(err, ErrorCode) function.
func StdIs(err, target error) bool { return stderrors.Is(err, target) }

// Join returns an error that wraps the given errors. Any nil error values
// are discarded. Re-export of errors.Join.
func Join(errs ...error) error { return stderrors.Join(errs...) }

// Unwrap returns the result of calling the Unwrap method on err.
// Re-export of errors.Unwrap.
func Unwrap(err error) error { return stderrors.Unwrap(err) }

// sentinelError is the uncoded error Sentinel returns. Each Sentinel call allocates a
// distinct value, so two sentinels with the same text are different errors, as with the
// standard library's errors.New.
type sentinelError struct {
	// text is the error message.
	text string
}

// Error returns the sentinel's message.
func (e *sentinelError) Error() string { return e.text }

// Sentinel creates a simple sentinel error value with the given message.
// Use this instead of importing the standard "errors" package for errors.New.
// For application errors with error codes, use New() instead.
func Sentinel(text string) error { return &sentinelError{text: text} }

// Identical reports whether a and b are the same error value, by identity rather than by
// chain membership (which is what Is checks). A boundary uses it to tell an error it
// passed through untouched from one it produced or rewrapped.
func Identical(a, b error) bool { return any(a) == any(b) }
