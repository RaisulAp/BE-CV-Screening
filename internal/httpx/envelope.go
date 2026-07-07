package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-playground/validator/v10"
)

// Uniform response envelope (BACKEND.md §6):
//   success: { "success": true,  "data": {...} }
//   failure: { "success": false, "error": { "code", "message" } }

type successBody struct {
	Success bool `json:"success"`
	Data    any  `json:"data"`
}

type errorBody struct {
	Success bool          `json:"success"`
	Error   errorEnvelope `json:"error"`
}

type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteSuccess writes a 2xx envelope.
func WriteSuccess(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, successBody{Success: true, Data: data})
}

// WriteError maps any error to the failure envelope. AppError is emitted with
// its code/status; anything else is masked as a generic 500 (no internal leak).
func WriteError(w http.ResponseWriter, err error) {
	var appErr *AppError
	if !errors.As(err, &appErr) {
		appErr = ErrInternal()
	}
	writeJSON(w, appErr.HTTPStatus, errorBody{
		Success: false,
		Error:   errorEnvelope{Code: appErr.Code, Message: appErr.Message},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

var validate = validator.New(validator.WithRequiredStructEnabled())

// DecodeAndValidate reads a JSON body into dst and runs struct validation.
// Returns an AppError (VALIDATION_ERROR) on any failure so handlers can just
// forward it to WriteError.
func DecodeAndValidate(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20)) // 1MB cap for JSON bodies
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return ErrValidation("Format permintaan tidak valid: " + err.Error())
	}
	if err := validate.Struct(dst); err != nil {
		return ErrValidation("Input tidak valid: " + err.Error())
	}
	return nil
}
