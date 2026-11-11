package httpapi

import (
	"encoding/json"
	"net/http"
	"time"
)

// errorBody is the shape of every 4xx and 5xx response:
// {"error": {"code": "...", "message": "...", "field": "..."}}.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent, so an encoding error cannot change
	// the response; the client sees a truncated body.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// formatTime renders t as RFC 3339 in the business zone loc, such as
// 2026-11-16T09:00:00-08:00. Every timestamp in a response goes through
// it, because pgx scans timestamptz values into time.Local, not loc.
func formatTime(t time.Time, loc *time.Location) string {
	return t.In(loc).Format(time.RFC3339)
}
