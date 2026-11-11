package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakePinger returns err from Ping and records whether Ping had a deadline.
type fakePinger struct {
	err         error
	hadDeadline bool
}

func (p *fakePinger) Ping(ctx context.Context) error {
	_, p.hadDeadline = ctx.Deadline()
	return p.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{"database reachable", nil, http.StatusOK, `{"status":"ok"}` + "\n"},
		{"database unreachable", errors.New("connection refused"), http.StatusServiceUnavailable, `{"status":"unavailable"}` + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakePinger{err: tt.pingErr}
			h := NewHandler(discardLogger(), db)

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if !db.hadDeadline {
				t.Error("Ping was called without a deadline")
			}
		})
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	h := NewHandler(discardLogger(), &fakePinger{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	want := `{"error":{"code":"not_found","message":"no route for GET /nope"}}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
