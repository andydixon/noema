// Package api serves the versioned JSON API under /api/v1 and composes the
// middleware chain shared with the web UI.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/andydixon/noema/internal/storage"
)

// ErrorBody is the single error envelope used by every endpoint.
type ErrorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	enc.Encode(v) //nolint:errcheck // client gone; nothing to do
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	var b ErrorBody
	b.Error.Code, b.Error.Message, b.Error.RequestID = code, msg, RequestID(r)
	writeJSON(w, status, b)
}

// fail maps common errors to HTTP statuses; unknown errors become 500 without
// leaking internals.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, storage.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "resource already exists")
	default:
		var ve ValidationError
		if errors.As(err, &ve) {
			writeError(w, r, http.StatusBadRequest, "invalid", ve.Error())
			return
		}
		logger(r).Error("request failed", "err", err)
		writeError(w, r, http.StatusInternalServerError, "internal", "internal error")
	}
}

// ValidationError marks user-correctable input problems.
type ValidationError string

func (v ValidationError) Error() string { return string(v) }

// decode reads a JSON body with unknown fields rejected and size already
// limited by middleware.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return ValidationError("invalid JSON body: " + err.Error())
	}
	return nil
}

// page is the standard list envelope.
type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
