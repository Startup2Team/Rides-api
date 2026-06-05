package respond

import (
	"encoding/json"
	"errors"
	"net/http"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

type envelope struct {
	Data  interface{} `json:"data,omitempty"`
	Error *errBody    `json:"error,omitempty"`
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// JSON writes a successful JSON response.
func JSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data})
}

// OK writes HTTP 200 with data.
func OK(w http.ResponseWriter, data interface{}) {
	JSON(w, http.StatusOK, data)
}

// Created writes HTTP 201 with data.
func Created(w http.ResponseWriter, data interface{}) {
	JSON(w, http.StatusCreated, data)
}

// NoContent writes HTTP 204.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Error writes an error JSON response. Accepts *AppError or falls back to 500.
func Error(w http.ResponseWriter, err error) {
	var ae *apperrors.AppError
	if !errors.As(err, &ae) {
		ae = apperrors.ErrInternal
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ae.StatusCode)
	_ = json.NewEncoder(w).Encode(envelope{
		Error: &errBody{Code: ae.Code, Message: ae.Message},
	})
}

// ErrorMsg writes a raw error string response.
func ErrorMsg(w http.ResponseWriter, status int, code, message string) {
	Error(w, &apperrors.AppError{StatusCode: status, Code: code, Message: message})
}
