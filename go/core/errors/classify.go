package errors

import "net/http"

// IsTransient returns true if the error represents a transient failure that may
// succeed on retry (timeout, unavailable). CodeResourceExhausted is deliberately not
// transient: a spent quota does not recover within a retry backoff, only when its
// window resets. The retry class of each code is the taxonomy's (codes.go).
func IsTransient(err error) bool {
	info, ok := infoOf(Code(err))
	return ok && info.retry == retryTransient
}

// IsPermanent returns true if the error represents a permanent failure that will not
// succeed on retry (not found, invalid input, unauthorized, forbidden, conflict).
// CodeResourceExhausted is not permanent: the request succeeds once the quota window
// resets.
func IsPermanent(err error) bool {
	info, ok := infoOf(Code(err))
	return ok && info.retry == retryPermanent
}

// ToHTTPStatus maps an ErrorCode to the corresponding HTTP status code; a code the
// taxonomy does not declare is 500.
func ToHTTPStatus(code ErrorCode) int {
	if info, ok := infoOf(code); ok {
		return info.http
	}
	return http.StatusInternalServerError
}
