// Package rest provides HTTP response helpers for REST API handlers.
//
// BaseController offers common HTTP response methods (WriteJSON, WriteError,
// WriteSuccess, WriteCreated, WriteNoContent). Use Adapt to bridge an
// http.HandlerFunc to a transport.HandlerFunc for decorator composition.
package rest

import (
	"encoding/json"
	"net/http"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
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
func (c *BaseController) WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	body, err := json.Marshal(data)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal server error"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// WriteError writes an error response.
func (c *BaseController) WriteError(w http.ResponseWriter, err error) {
	status := MapErrorToHTTPStatus(err)
	response := ErrorResponse{
		Error: "internal server error",
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

	c.WriteJSON(w, status, response)
}

// WriteSuccess writes a success response with 200 OK.
func (c *BaseController) WriteSuccess(w http.ResponseWriter, data interface{}) {
	c.WriteJSON(w, http.StatusOK, data)
}

// WriteCreated writes a created response with 201 Created.
func (c *BaseController) WriteCreated(w http.ResponseWriter, data interface{}) {
	c.WriteJSON(w, http.StatusCreated, data)
}

// WriteNoContent writes a no content response with 204 No Content.
func (c *BaseController) WriteNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
