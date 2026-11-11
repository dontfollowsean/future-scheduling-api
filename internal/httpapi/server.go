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
	mux.HandleFunc("/", handleNotFound)

	// The request id is outermost so every later log line can carry it.
	// Recover sits inside logging so a panic is logged as a 500.
	var h http.Handler = mux
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
