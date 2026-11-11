// Package httpapi is the HTTP transport: routing, middleware, request
// parsing and JSON responses. It holds no business rules and no SQL.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// healthTimeout bounds how long /healthz waits for the database.
const healthTimeout = 2 * time.Second

// Pinger reports whether the database is reachable.
// *pgxpool.Pool satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewHandler returns the API's root handler with all routes and middleware.
func NewHandler(logger *slog.Logger, db Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth(logger, db))

	// The request id is outermost so every later log line can carry it.
	// Recover sits inside logging so a panic is logged as a 500.
	h := jsonNotFound(mux)
	h = recoverPanics(logger, h)
	h = logRequests(logger, h)
	h = withRequestID(h)
	return h
}

type healthResponse struct {
	Status string `json:"status"`
}

func handleHealth(logger *slog.Logger, db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(ctx, "health check failed",
				"request_id", RequestID(ctx), "error", err)
			writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	}
}

func handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "no route for "+r.Method+" "+r.URL.Path)
}

// jsonNotFound serves mux but turns its plain-text 404 for an unmatched
// path into the JSON 404. The mux's 405 for a wrong method on a known
// path, with its Allow header, passes through unchanged.
func jsonNotFound(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(&notFoundWriter{ResponseWriter: w, r: r}, r)
	})
}

// notFoundWriter replaces a 404 response with handleNotFound's and drops
// the body the mux writes after it.
type notFoundWriter struct {
	http.ResponseWriter
	r        *http.Request
	replaced bool
}

func (w *notFoundWriter) WriteHeader(status int) {
	if status == http.StatusNotFound {
		w.replaced = true
		handleNotFound(w.ResponseWriter, w.r)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *notFoundWriter) Write(b []byte) (int, error) {
	if w.replaced {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
