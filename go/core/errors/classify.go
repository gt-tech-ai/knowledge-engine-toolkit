package errors

import "net/http"

// IsTransient returns true if the error represents a transient failure
// that may succeed on retry (timeout, unavailable). CodeResourceExhausted is
// deliberately not transient: a spent quota does not recover within a retry
// backoff, only when its window resets.
func IsTransient(err error) bool {
	code := Code(err)
	switch code {
	case ErrTimeout, ErrUnavailable:
		return true
	case CodeUnknown, CodeInternal, CodeNotFound, CodeUnauthorized, CodeForbidden,
		CodeInvalidInput, CodeConflict, CodeCanceled, CodeIngestion, CodeQualityFailed,
		CodeUpstream, CodeResourceExhausted:
		return false
	default:
		return false
	}
}

// IsPermanent returns true if the error represents a permanent failure that will not
// succeed on retry (not found, invalid input, unauthorized, forbidden, conflict).
// CodeResourceExhausted is not permanent: the request succeeds once the quota window
// resets.
func IsPermanent(err error) bool {
	code := Code(err)
	switch code {
	case ErrNotFound, ErrInvalidInput, ErrUnauthorized, ErrForbidden, ErrConflict:
		return true
	case CodeUnknown, CodeInternal, CodeTimeout, CodeCanceled, CodeUnavailable,
		CodeIngestion, CodeQualityFailed, CodeUpstream, CodeResourceExhausted:
		return false
	default:
		return false
	}
}

// ToHTTPStatus maps an ErrorCode to the corresponding HTTP status code.
func ToHTTPStatus(code ErrorCode) int {
	switch code {
	case ErrNotFound:
		return http.StatusNotFound
	case ErrInvalidInput:
		return http.StatusBadRequest
	case ErrUnauthorized:
		return http.StatusUnauthorized
	case ErrForbidden:
		return http.StatusForbidden
	case ErrConflict:
		return http.StatusConflict
	case ErrTimeout:
		return http.StatusGatewayTimeout
	case ErrUnavailable:
		return http.StatusServiceUnavailable
	case ErrInternal:
		return http.StatusInternalServerError
	case CodeUpstream:
		return http.StatusBadGateway
	case CodeResourceExhausted:
		return http.StatusTooManyRequests
	case CodeUnknown, CodeCanceled, CodeIngestion, CodeQualityFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
