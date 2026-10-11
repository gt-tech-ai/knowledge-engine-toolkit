package rest

import (
	"net/http"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/transport"
)

// Adapt bridges an http.HandlerFunc to a typed transport.HandlerFunc.
//
// The adapter handles the full HTTP lifecycle:
//  1. Parse the HTTP request into a typed Req using the provided parse function
//  2. Call the decorated HandlerFunc with the parsed request
//  3. Write the response using BaseController (WriteSuccess for 200, WriteCreated for
//     201, etc.)
//
// Use writeStatus to control the success HTTP status code (e.g., http.StatusOK,
// http.StatusCreated). For 204 No Content responses, use AdaptNoContent instead.
//
// Example:
//
//	mux.HandleFunc("GET /api/v1/users/{id}", rest.Adapt(
//	    bc, getHandler, http.StatusOK,
//	    func(r *http.Request) (GetUserReq, error) {
//	        return GetUserReq{ID: r.PathValue("id")}, nil
//	    },
//	))
func Adapt[Req, Resp any](
	bc *BaseController,
	handler transport.HandlerFunc[Req, Resp],
	writeStatus int,
	parse func(r *http.Request) (Req, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := parse(r)
		if err != nil {
			bc.WriteError(w, err)
			return
		}

		resp, err := handler(r.Context(), req)
		if err != nil {
			bc.WriteError(w, err)
			return
		}

		bc.WriteJSON(w, writeStatus, resp)
	}
}

// AdaptNoContent bridges an http.HandlerFunc to a typed transport.HandlerFunc
// that returns 204 No Content on success (ignoring the response value).
//
// Example:
//
//	mux.HandleFunc("DELETE /api/v1/users/{id}", rest.AdaptNoContent(
//	    bc, deleteHandler,
//	    func(r *http.Request) (DeleteUserReq, error) {
//	        return DeleteUserReq{ID: r.PathValue("id")}, nil
//	    },
//	))
func AdaptNoContent[Req, Resp any](
	bc *BaseController,
	handler transport.HandlerFunc[Req, Resp],
	parse func(r *http.Request) (Req, error),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, err := parse(r)
		if err != nil {
			bc.WriteError(w, err)
			return
		}

		_, err = handler(r.Context(), req)
		if err != nil {
			bc.WriteError(w, err)
			return
		}

		bc.WriteNoContent(w)
	}
}
