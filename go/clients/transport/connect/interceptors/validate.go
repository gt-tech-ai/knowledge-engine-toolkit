package interceptors

import (
	"connectrpc.com/validate"
)

// ValidateInterceptor creates an interceptor that validates request messages using
// protovalidate.
func ValidateInterceptor() *validate.Interceptor {
	return validate.NewInterceptor(validate.WithValidateResponses())
}
