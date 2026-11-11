package httpapi

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// requestIDHeader carries the request id in both directions.
const requestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestID returns the id withRequestID stored in ctx, or "" if there is none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID reuses the caller's X-Request-ID or generates a new one,
// stores it in the request context and echoes it in the response header.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = rand.Text()
		}
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder remembers the status code and body size a handler wrote,
// so the logging middleware can report them.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rec *statusRecorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rec *statusRecorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}

// logRequests writes one log line per request. It logs the route pattern
// rather than the raw path to keep cardinality low, and never logs bodies.
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		// A handler that writes nothing gets an implicit 200.
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		// ServeMux sets r.Pattern on this same *http.Request, which is why
		// the middleware between here and the mux must not copy r.
		logger.InfoContext(r.Context(), "request",
			"method", r.Method,
			"route", r.Pattern,
			"status", status,
			"bytes", rec.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestID(r.Context()),
		)
	})
}

// recoverPanics turns a panic in a handler into a logged error and a JSON 500.
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			err := recover()
			if err == nil {
				return
			}
			// ErrAbortHandler is net/http's signal to abort the response silently.
			if err == http.ErrAbortHandler {
				panic(err)
			}
			logger.ErrorContext(r.Context(), "panic serving request",
				"error", err,
				"stack", string(debug.Stack()),
				"request_id", RequestID(r.Context()),
			)
			writeError(w, http.StatusInternalServerError, "internal", "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}
