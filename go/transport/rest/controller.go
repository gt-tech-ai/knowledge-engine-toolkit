// Package rest provides HTTP response helpers for REST API handlers.
//
// BaseController offers common HTTP response methods (WriteJSON, WriteError,
// WriteSuccess, WriteCreated, WriteNoContent). Use Adapt to bridge an
// http.HandlerFunc to a transport.HandlerFunc for decorator composition.
package rest

import (
	"encoding/json"
	"net/http"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
)

// BaseController provides common HTTP response methods.
type BaseController struct{}

// ErrorResponse represents an HTTP error response.
type ErrorResponse struct {
	// Error is the human-readable error message.
	Error string `json:"error"`

	// Code is the application-level error code, omitted when empty.
	Code string `json:"code,omitempty"`

	// Details provides additional context about the error, omitted when empty.
	Details string `json:"details,omitempty"`
}

// MapErrorToHTTPStatus maps AppError to HTTP status codes.
func MapErrorToHTTPStatus(err error) int {
	var appErr *apperr.AppError

	if apperr.As(err, &appErr) {
		return apperr.ToHTTPStatus(appErr.Code)
	}

	return http.StatusInternalServerError
}

// WriteJSON writes a JSON response.
func (*BaseController) WriteJSON(writer http.ResponseWriter, status int, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusInternalServerError)

		_, _ = writer.Write([]byte(`{"error":"internal server error"}`))

		return
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)

	_, _ = writer.Write(body)
}

// WriteError writes an error response.
func (c *BaseController) WriteError(writer http.ResponseWriter, err error) {
	status := MapErrorToHTTPStatus(err)
	response := ErrorResponse{
		Error:   "internal server error",
		Code:    "",
		Details: "",
	}

	var appErr *apperr.AppError

	if apperr.As(err, &appErr) {
		response.Code = string(appErr.Code)
		// Expose the message unless the code hides it (taxonomy hide_message).
		if !apperr.HidesMessage(appErr.Code) {
			response.Error = appErr.Message
			response.Details = appErr.Message
		}
	}

	c.WriteJSON(writer, status, response)
}

// WriteSuccess writes a success response with 200 OK.
func (c *BaseController) WriteSuccess(w http.ResponseWriter, data any) {
	c.WriteJSON(w, http.StatusOK, data)
}

// WriteCreated writes a created response with 201 Created.
func (c *BaseController) WriteCreated(w http.ResponseWriter, data any) {
	c.WriteJSON(w, http.StatusCreated, data)
}

// WriteNoContent writes a no content response with 204 No Content.
func (*BaseController) WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
