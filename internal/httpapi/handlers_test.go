package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
)

// handlerNow is the fixed clock for the handler tests: Tuesday 2026-11-10
// at noon PST, the week before the Monday 2026-11-16 the examples use.
var handlerNow = time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)

// fakeStore is an in-memory scheduling.Store. Like Postgres, it refuses an
// overlapping appointment for the same trainer, and it returns times in
// UTC rather than the business zone, as pgx returns them in time.Local.
type fakeStore struct {
	mu    sync.Mutex
	appts []scheduling.Appointment
	err   error // when set, every method fails with it
}

func (s *fakeStore) CreateAppointment(_ context.Context, a scheduling.Appointment) (scheduling.Appointment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return scheduling.Appointment{}, s.err
	}
	for _, b := range s.appts {
		if b.TrainerID == a.TrainerID && a.StartsAt.Before(b.EndsAt) && b.StartsAt.Before(a.EndsAt) {
			return scheduling.Appointment{}, scheduling.ErrSlotTaken
		}
	}
	a.ID = int64(len(s.appts) + 1)
	a.StartsAt, a.EndsAt = a.StartsAt.UTC(), a.EndsAt.UTC()
	s.appts = append(s.appts, a)
	return a, nil
}

func (s *fakeStore) ListByTrainer(ctx context.Context, trainerID int64) ([]scheduling.Appointment, error) {
	return s.list(trainerID, func(scheduling.Appointment) bool { return true })
}

func (s *fakeStore) ListOverlapping(ctx context.Context, trainerID int64, from, to time.Time) ([]scheduling.Appointment, error) {
	return s.list(trainerID, func(a scheduling.Appointment) bool {
		return a.StartsAt.Before(to) && from.Before(a.EndsAt)
	})
}

// list returns the trainer's appointments that keep accepts, ordered by
// start time and then ID.
func (s *fakeStore) list(trainerID int64, keep func(scheduling.Appointment) bool) ([]scheduling.Appointment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	appts := []scheduling.Appointment{}
	for _, a := range s.appts {
		if a.TrainerID == trainerID && keep(a) {
			appts = append(appts, a)
		}
	}
	slices.SortFunc(appts, func(a, b scheduling.Appointment) int {
		if c := a.StartsAt.Compare(b.StartsAt); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	return appts, nil
}

// book adds an appointment that the test expects to succeed.
func (s *fakeStore) book(t *testing.T, trainerID int64, start, end string) {
	t.Helper()
	_, err := s.CreateAppointment(t.Context(), scheduling.Appointment{
		TrainerID: trainerID,
		UserID:    7,
		StartsAt:  mustParse(t, start),
		EndsAt:    mustParse(t, end),
	})
	if err != nil {
		t.Fatalf("book trainer %d at %s: %v", trainerID, start, err)
	}
}

func newService(t *testing.T, store scheduling.Store) *scheduling.Service {
	t.Helper()
	rules, err := scheduling.DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	return scheduling.NewService(store, rules, func() time.Time { return handlerNow })
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// serve sends one request through the full handler, middleware included.
func serve(t *testing.T, store *fakeStore, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandler(discardLogger(), &fakePinger{}, newService(t, store))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

// response is what a handler test expects back. An empty wantBody means
// only the status, code and field are checked.
type response struct {
	wantStatus int
	wantCode   string // the error code; empty for a success
	wantField  string
	wantBody   string // exact JSON, without the trailing newline
}

func checkResponse(t *testing.T, rec *httptest.ResponseRecorder, want response) {
	t.Helper()
	if rec.Code != want.wantStatus {
		t.Errorf("status = %d, want %d (body %s)", rec.Code, want.wantStatus, rec.Body)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if want.wantBody != "" {
		if got := strings.TrimSuffix(rec.Body.String(), "\n"); got != want.wantBody {
			t.Errorf("body =\n%s\nwant\n%s", got, want.wantBody)
		}
	}
	if want.wantCode == "" {
		return
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body, err)
	}
	if body.Error.Code != want.wantCode || body.Error.Field != want.wantField {
		t.Errorf("code, field = %q, %q, want %q, %q (message %q)",
			body.Error.Code, body.Error.Field, want.wantCode, want.wantField, body.Error.Message)
	}
	if body.Error.Message == "" {
		t.Error("error message is empty")
	}
}

// bookingBody is a POST /appointments body for trainer 1 and user 42.
func bookingBody(start, end string) string {
	return fmt.Sprintf(`{"trainer_id": 1, "user_id": 42, "starts_at": %q, "ends_at": %q}`, start, end)
}

// TestBook has at least one case for each row of PLAN.md section 3, in
// order. Trainer 1 is already booked 09:00-09:30 on Monday 2026-11-16.
func TestBook(t *testing.T) {
	tests := []struct {
		name string
		body string
		response
	}{
		// 1. The body is one JSON object of the right shape.
		{"not JSON", `{"trainer_id": 1,`, response{wantStatus: 400, wantCode: "invalid_json"}},
		{"empty body", ``, response{wantStatus: 400, wantCode: "invalid_json"}},
		{"an array", `[]`, response{wantStatus: 400, wantCode: "invalid_json"}},
		{"null", `null`, response{wantStatus: 400, wantCode: "invalid_json"}},
		{"two objects", bookingBody("2026-11-16T10:00:00-08:00", "2026-11-16T10:30:00-08:00") + `{}`,
			response{wantStatus: 400, wantCode: "invalid_json"}},
		{"trainer_id as a string", `{"trainer_id": "1", "user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantCode: "invalid_json"}},
		{"a field name from the seed file", `{"trainer_id": 1, "user_id": 42, "started_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantBody: `{"error":{"code":"invalid_json","message":"json: unknown field \"started_at\""}}`}},
		{"body over 1 MiB", `{"trainer_id": 1, "user_id": 42, "starts_at": "` + strings.Repeat("x", maxBodyBytes) + `"}`,
			response{wantStatus: 400, wantBody: `{"error":{"code":"invalid_json","message":"request body must be at most 1048576 bytes"}}`}},

		// 2. The ids are present and positive.
		{"trainer_id missing", `{"user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "trainer_id"}},
		{"trainer_id zero", `{"trainer_id": 0, "user_id": 42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "trainer_id"}},
		{"user_id missing", `{"trainer_id": 1, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "user_id"}},
		{"user_id negative", `{"trainer_id": 1, "user_id": -42, "starts_at": "2026-11-16T10:00:00-08:00", "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "user_id"}},

		// 3. The times are present and RFC 3339 with an offset.
		{"starts_at missing", `{"trainer_id": 1, "user_id": 42, "ends_at": "2026-11-16T10:30:00-08:00"}`,
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "starts_at"}},
		{"starts_at without an offset", bookingBody("2026-11-16T10:00:00", "2026-11-16T10:30:00-08:00"),
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "starts_at"}},
		{"ends_at not a time", bookingBody("2026-11-16T10:00:00-08:00", "half past ten"),
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "ends_at"}},

		// 4 to 8. The scheduling rules, from the Service.
		{"60 minutes", bookingBody("2026-11-16T10:00:00-08:00", "2026-11-16T11:00:00-08:00"),
			response{wantStatus: 422, wantCode: "invalid_duration", wantField: "ends_at"}},
		{"off the 15-minute grid", bookingBody("2026-11-16T10:10:00-08:00", "2026-11-16T10:40:00-08:00"),
			response{wantStatus: 422, wantBody: `{"error":{"code":"off_grid","message":"starts_at must be on a 15-minute boundary in America/Los_Angeles, with zero seconds","field":"starts_at"}}`}},
		{"off the grid by seconds at :45", bookingBody("2026-11-16T10:45:30-08:00", "2026-11-16T11:15:30-08:00"),
			response{wantStatus: 422, wantCode: "off_grid", wantField: "starts_at"}},
		{"Saturday", bookingBody("2026-11-21T10:00:00-08:00", "2026-11-21T10:30:00-08:00"),
			response{wantStatus: 422, wantCode: "not_business_day", wantField: "starts_at"}},
		{"17:00 is after hours", bookingBody("2026-11-16T17:00:00-08:00", "2026-11-16T17:30:00-08:00"),
			response{wantStatus: 422, wantCode: "outside_business_hours", wantField: "starts_at"}},
		{"last Monday is in the past", bookingBody("2026-11-09T10:00:00-08:00", "2026-11-09T10:30:00-08:00"),
			response{wantStatus: 422, wantCode: "in_past", wantField: "starts_at"}},

		// 9. The trainer is free, which the store decides.
		{"slot already taken", bookingBody("2026-11-16T09:00:00-08:00", "2026-11-16T09:30:00-08:00"),
			response{wantStatus: 409, wantBody: `{"error":{"code":"slot_unavailable","message":"trainer 1 already has an appointment overlapping 2026-11-16T09:00:00-08:00 to 2026-11-16T09:30:00-08:00"}}`}},

		// Success.
		{"free slot", bookingBody("2026-11-16T10:00:00-08:00", "2026-11-16T10:30:00-08:00"),
			response{wantStatus: 201, wantBody: `{"id":2,"trainer_id":1,"user_id":42,"starts_at":"2026-11-16T10:00:00-08:00","ends_at":"2026-11-16T10:30:00-08:00"}`}},
		{"on the grid at :15", bookingBody("2026-11-16T10:15:00-08:00", "2026-11-16T10:45:00-08:00"),
			response{wantStatus: 201, wantBody: `{"id":2,"trainer_id":1,"user_id":42,"starts_at":"2026-11-16T10:15:00-08:00","ends_at":"2026-11-16T10:45:00-08:00"}`}},
		{"on the grid at :45", bookingBody("2026-11-16T10:45:00-08:00", "2026-11-16T11:15:00-08:00"),
			response{wantStatus: 201, wantBody: `{"id":2,"trainer_id":1,"user_id":42,"starts_at":"2026-11-16T10:45:00-08:00","ends_at":"2026-11-16T11:15:00-08:00"}`}},
		{"overlapping the booked slot at :15", bookingBody("2026-11-16T09:15:00-08:00", "2026-11-16T09:45:00-08:00"),
			response{wantStatus: 409, wantCode: "slot_unavailable"}},
		{"back to back with the booked slot", bookingBody("2026-11-16T09:30:00-08:00", "2026-11-16T10:00:00-08:00"),
			response{wantStatus: 201, wantBody: `{"id":2,"trainer_id":1,"user_id":42,"starts_at":"2026-11-16T09:30:00-08:00","ends_at":"2026-11-16T10:00:00-08:00"}`}},
		{"times in UTC are answered in Pacific time", bookingBody("2026-11-16T18:00:00Z", "2026-11-16T18:30:00Z"),
			response{wantStatus: 201, wantBody: `{"id":2,"trainer_id":1,"user_id":42,"starts_at":"2026-11-16T10:00:00-08:00","ends_at":"2026-11-16T10:30:00-08:00"}`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			store.book(t, 1, "2026-11-16T09:00:00-08:00", "2026-11-16T09:30:00-08:00")

			rec := serve(t, store, http.MethodPost, "/appointments", tt.body)
			checkResponse(t, rec, tt.response)

			wantAppts := 1
			if tt.wantStatus == http.StatusCreated {
				wantAppts = 2
			}
			if len(store.appts) != wantAppts {
				t.Errorf("store holds %d appointments, want %d", len(store.appts), wantAppts)
			}
		})
	}
}

// TestAvailability follows PLAN.md section 5. Trainer 1 is already booked
// 09:00-09:30 on Monday 2026-11-16.
func TestAvailability(t *testing.T) {
	tests := []struct {
		name   string
		target string
		response
	}{
		{"the PLAN.md example", "/trainers/1/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T10:00:00-08:00",
			response{wantStatus: 200, wantBody: `{"trainer_id":1,"slots":[` +
				`{"starts_at":"2026-11-16T08:00:00-08:00","ends_at":"2026-11-16T08:30:00-08:00"},` +
				`{"starts_at":"2026-11-16T08:15:00-08:00","ends_at":"2026-11-16T08:45:00-08:00"},` +
				`{"starts_at":"2026-11-16T08:30:00-08:00","ends_at":"2026-11-16T09:00:00-08:00"},` +
				`{"starts_at":"2026-11-16T09:30:00-08:00","ends_at":"2026-11-16T10:00:00-08:00"}]}`}},
		{":15 and :45 starts are listed", "/trainers/2/availability?starts_at=2026-11-16T09:00:00-08:00&ends_at=2026-11-16T10:15:00-08:00",
			response{wantStatus: 200, wantBody: `{"trainer_id":2,"slots":[` +
				`{"starts_at":"2026-11-16T09:00:00-08:00","ends_at":"2026-11-16T09:30:00-08:00"},` +
				`{"starts_at":"2026-11-16T09:15:00-08:00","ends_at":"2026-11-16T09:45:00-08:00"},` +
				`{"starts_at":"2026-11-16T09:30:00-08:00","ends_at":"2026-11-16T10:00:00-08:00"},` +
				`{"starts_at":"2026-11-16T09:45:00-08:00","ends_at":"2026-11-16T10:15:00-08:00"}]}`}},
		{"another trainer is free", "/trainers/2/availability?starts_at=2026-11-16T09:00:00-08:00&ends_at=2026-11-16T09:30:00-08:00",
			response{wantStatus: 200, wantBody: `{"trainer_id":2,"slots":[{"starts_at":"2026-11-16T09:00:00-08:00","ends_at":"2026-11-16T09:30:00-08:00"}]}`}},
		{"a positive offset sent as %2B", "/trainers/2/availability?starts_at=2026-11-16T16:00:00%2B00:00&ends_at=2026-11-16T16:30:00%2B00:00",
			response{wantStatus: 200, wantBody: `{"trainer_id":2,"slots":[{"starts_at":"2026-11-16T08:00:00-08:00","ends_at":"2026-11-16T08:30:00-08:00"}]}`}},
		{"a weekend is empty, not null", "/trainers/1/availability?starts_at=2026-11-21T00:00:00-08:00&ends_at=2026-11-23T00:00:00-08:00",
			response{wantStatus: 200, wantBody: `{"trainer_id":1,"slots":[]}`}},
		{"the past is empty", "/trainers/1/availability?starts_at=2026-11-09T00:00:00-08:00&ends_at=2026-11-10T00:00:00-08:00",
			response{wantStatus: 200, wantBody: `{"trainer_id":1,"slots":[]}`}},

		{"trainer_id not a number", "/trainers/abc/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T10:00:00-08:00",
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "trainer_id"}},
		{"trainer_id zero", "/trainers/0/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T10:00:00-08:00",
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "trainer_id"}},
		{"starts_at missing", "/trainers/1/availability?ends_at=2026-11-16T10:00:00-08:00",
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "starts_at"}},
		{"ends_at missing", "/trainers/1/availability?starts_at=2026-11-16T08:00:00-08:00",
			response{wantStatus: 400, wantBody: `{"error":{"code":"invalid_field","message":"ends_at is required (RFC 3339 with offset)","field":"ends_at"}}`}},
		{"a date without a time", "/trainers/1/availability?starts_at=2026-11-16&ends_at=2026-11-17",
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "starts_at"}},
		{"an unencoded + decodes to a space", "/trainers/1/availability?starts_at=2026-11-16T16:00:00+00:00&ends_at=2026-11-16T18:00:00-08:00",
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "starts_at"}},
		{"empty window", "/trainers/1/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T08:00:00-08:00",
			response{wantStatus: 422, wantCode: "invalid_range"}},
		{"60-day window", "/trainers/1/availability?starts_at=2026-11-16T00:00:00-08:00&ends_at=2027-01-15T00:00:00-08:00",
			response{wantStatus: 422, wantBody: `{"error":{"code":"range_too_large","message":"the window between starts_at and ends_at must be 31 days or less"}}`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			store.book(t, 1, "2026-11-16T09:00:00-08:00", "2026-11-16T09:30:00-08:00")

			rec := serve(t, store, http.MethodGet, tt.target, "")
			checkResponse(t, rec, tt.response)
		})
	}
}

// TestBookingAtQuarterPastFreesQuarterTo books 09:15-09:45 and checks
// that availability then hides every start it overlaps but still lists
// 09:45, which starts as the booking ends.
func TestBookingAtQuarterPastFreesQuarterTo(t *testing.T) {
	store := &fakeStore{}
	rec := serve(t, store, http.MethodPost, "/appointments", bookingBody("2026-11-16T09:15:00-08:00", "2026-11-16T09:45:00-08:00"))
	checkResponse(t, rec, response{wantStatus: 201})

	rec = serve(t, store, http.MethodGet, "/trainers/1/availability?starts_at=2026-11-16T08:45:00-08:00&ends_at=2026-11-16T10:30:00-08:00", "")
	checkResponse(t, rec, response{wantStatus: 200, wantBody: `{"trainer_id":1,"slots":[` +
		`{"starts_at":"2026-11-16T08:45:00-08:00","ends_at":"2026-11-16T09:15:00-08:00"},` +
		`{"starts_at":"2026-11-16T09:45:00-08:00","ends_at":"2026-11-16T10:15:00-08:00"},` +
		`{"starts_at":"2026-11-16T10:00:00-08:00","ends_at":"2026-11-16T10:30:00-08:00"}]}`})
}

func TestListAppointments(t *testing.T) {
	tests := []struct {
		name   string
		target string
		response
	}{
		{"in start order, in Pacific time", "/trainers/1/appointments",
			response{wantStatus: 200, wantBody: `{"trainer_id":1,"appointments":[` +
				`{"id":2,"trainer_id":1,"user_id":7,"starts_at":"2019-01-24T09:00:00-08:00","ends_at":"2019-01-24T09:30:00-08:00"},` +
				`{"id":1,"trainer_id":1,"user_id":7,"starts_at":"2026-11-16T09:00:00-08:00","ends_at":"2026-11-16T09:30:00-08:00"}]}`}},
		{"a trainer with none is empty, not null", "/trainers/5/appointments",
			response{wantStatus: 200, wantBody: `{"trainer_id":5,"appointments":[]}`}},
		{"trainer_id not a number", "/trainers/abc/appointments",
			response{wantStatus: 400, wantBody: `{"error":{"code":"invalid_field","message":"trainer_id must be a positive integer","field":"trainer_id"}}`}},
		{"trainer_id negative", "/trainers/-1/appointments",
			response{wantStatus: 400, wantCode: "invalid_field", wantField: "trainer_id"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Booked out of order, and trainer 2's must not appear.
			store := &fakeStore{}
			store.book(t, 1, "2026-11-16T09:00:00-08:00", "2026-11-16T09:30:00-08:00")
			store.book(t, 1, "2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00")
			store.book(t, 2, "2026-11-16T09:00:00-08:00", "2026-11-16T09:30:00-08:00")

			rec := serve(t, store, http.MethodGet, tt.target, "")
			checkResponse(t, rec, tt.response)
		})
	}
}

// TestStoreFailureIsLogged500 checks that a database error reaches the log
// with the request id, and the client sees only the generic 500.
func TestStoreFailureIsLogged500(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		body   string
	}{
		{"availability", http.MethodGet, "/trainers/1/availability?starts_at=2026-11-16T08:00:00-08:00&ends_at=2026-11-16T10:00:00-08:00", ""},
		{"book", http.MethodPost, "/appointments", bookingBody("2026-11-16T10:00:00-08:00", "2026-11-16T10:30:00-08:00")},
		{"list", http.MethodGet, "/trainers/1/appointments", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			store := &fakeStore{err: errors.New("connection reset by peer")}
			h := NewHandler(slog.New(slog.NewJSONHandler(&logs, nil)), &fakePinger{}, newService(t, store))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body)))

			checkResponse(t, rec, response{wantStatus: 500, wantBody: `{"error":{"code":"internal","message":"internal error"}}`})
			line := findLog(t, decodeLogLines(t, &logs), "request failed")
			if got := line["error"]; got != "connection reset by peer" {
				t.Errorf("logged error = %v, want the store's error", got)
			}
			if got := line["request_id"]; got != rec.Header().Get(requestIDHeader) {
				t.Errorf("logged request_id = %v, want %q", got, rec.Header().Get(requestIDHeader))
			}
		})
	}
}

func TestWrongMethodOnAppointmentsIs405(t *testing.T) {
	rec := serve(t, &fakeStore{}, http.MethodGet, "/appointments", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
}
