package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// decodeLogLines parses JSON log output into one map per line.
func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		lines = append(lines, line)
	}
	return lines
}

// findLog returns the first log line with the given message.
func findLog(t *testing.T, lines []map[string]any, msg string) map[string]any {
	t.Helper()
	for _, line := range lines {
		if line["msg"] == msg {
			return line
		}
	}
	t.Fatalf("no %q log line in %v", msg, lines)
	return nil
}

func TestRequestLogRecordsStatusAndRequestID(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		incomingID    string
		wantStatus    int
		wantRoute     string
		wantReusedID  bool
		wantPanicLine bool
	}{
		{name: "200 with a generated id", path: "/healthz", wantStatus: http.StatusOK, wantRoute: "GET /healthz"},
		{name: "caller's id is reused", path: "/healthz", incomingID: "abc-123", wantStatus: http.StatusOK, wantRoute: "GET /healthz", wantReusedID: true},
		{name: "404 for an unmatched path", path: "/nope", wantStatus: http.StatusNotFound, wantRoute: ""},
		{name: "panic becomes a logged 500", path: "/panic", wantStatus: http.StatusInternalServerError, wantRoute: "GET /panic", wantPanicLine: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))

			mux := http.NewServeMux()
			mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
			})
			mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
				panic("boom")
			})
			h := withRequestID(logRequests(logger, recoverPanics(logger, jsonNotFound(mux))))

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.incomingID != "" {
				req.Header.Set(requestIDHeader, tt.incomingID)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			id := rec.Header().Get(requestIDHeader)
			if id == "" {
				t.Fatal("response has no X-Request-ID header")
			}
			if tt.wantReusedID && id != tt.incomingID {
				t.Errorf("X-Request-ID = %q, want the caller's %q", id, tt.incomingID)
			}

			lines := decodeLogLines(t, &logs)
			line := findLog(t, lines, "request")
			if got := line["status"]; got != float64(tt.wantStatus) {
				t.Errorf("logged status = %v, want %d", got, tt.wantStatus)
			}
			if got := line["request_id"]; got != id {
				t.Errorf("logged request_id = %v, want %q", got, id)
			}
			if got := line["route"]; got != tt.wantRoute {
				t.Errorf("logged route = %v, want %q", got, tt.wantRoute)
			}
			if got := line["bytes"]; got != float64(rec.Body.Len()) {
				t.Errorf("logged bytes = %v, want %d", got, rec.Body.Len())
			}

			if tt.wantPanicLine {
				panicLine := findLog(t, lines, "panic serving request")
				if got := panicLine["request_id"]; got != id {
					t.Errorf("panic log request_id = %v, want %q", got, id)
				}
				want := `{"error":{"code":"internal","message":"internal error"}}` + "\n"
				if got := rec.Body.String(); got != want {
					t.Errorf("body = %q, want %q", got, want)
				}
			}
		})
	}
}
