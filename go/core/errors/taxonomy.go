package errors

// retryClass is whether a retry can fix a failure carrying the code.
type retryClass int

// The retry classes.
const (
	// retryNone is neither transient nor permanent: the caller decides.
	retryNone retryClass = iota

	// retryTransient is a failure a retry may fix.
	retryTransient

	// retryPermanent is a failure no retry fixes.
	retryPermanent
)

// grpcInternal is the gRPC INTERNAL status number, the status of a code the taxonomy
// does not declare.
const grpcInternal = 13

// codeInfo is one code's row of the error-code taxonomy (taxonomy/error-codes.yaml,
// rendered into infoOf in codes.go).
type codeInfo struct {
	// safeMessage is the fixed client-safe message an RPC edge sends; empty means the
	// generic internal message.
	safeMessage string

	// http is the HTTP status an edge returns.
	http int

	// retry is the code's retry class.
	retry retryClass

	// grpc is the gRPC / Connect status number an edge returns.
	grpc uint32

	// hideMessage is true when the REST edge hides the error's own message.
	hideMessage bool
}

// GRPCStatus returns the gRPC / Connect status number for code; a code the taxonomy
// does not declare is INTERNAL (13).
func GRPCStatus(code ErrorCode) uint32 {
	if info, ok := infoOf(code); ok {
		return info.grpc
	}
	return grpcInternal
}

// SafeMessage returns code's fixed client-safe message, or "" when the code has none
// and an edge sends its generic internal message instead.
func SafeMessage(code ErrorCode) string {
	info, _ := infoOf(code)
	return info.safeMessage
}

// HidesMessage reports whether the REST edge hides the own message of an error
// carrying code; a code the taxonomy does not declare is shown.
func HidesMessage(code ErrorCode) bool {
	info, _ := infoOf(code)
	return info.hideMessage
}
