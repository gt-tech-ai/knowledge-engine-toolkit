package interceptors

import (
	"connectrpc.com/connect/v2"
	"connectrpc.com/validate"
)

// ValidateInterceptor creates a server interceptor that validates request and response
// messages using protovalidate.
func ValidateInterceptor() connect.ServerInterceptor {
	return validate.NewServerInterceptor(validate.WithValidateResponses())
}
