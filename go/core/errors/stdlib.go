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

// Sentinel creates a simple sentinel error value with the given message.
// Use this instead of importing the standard "errors" package for errors.New.
// For application errors with error codes, use New() instead.
func Sentinel(text string) error { return stderrors.New(text) }
