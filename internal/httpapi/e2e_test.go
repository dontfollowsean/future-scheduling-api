package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dontfollowsean/future-scheduling-api/internal/httpapi"
	"github.com/dontfollowsean/future-scheduling-api/internal/postgres"
	"github.com/dontfollowsean/future-scheduling-api/internal/postgres/postgrestest"
	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
)

// These tests run the real handler, Service and Postgres store over HTTP.
// Like every postgrestest test, they skip unless TEST_DATABASE_URL is set.

// seedNow pins the clock to Monday 2019-01-21, before the seed data's
// appointments, so they are not hidden as past (PLAN.md section 5).
var seedNow = time.Date(2019, 1, 21, 8, 0, 0, 0, time.UTC)

// newServer starts the API on an empty, migrated test database with the
// clock fixed at now.
func newServer(t *testing.T, now time.Time) (*httptest.Server, *postgres.Store) {
	t.Helper()
	pool := postgrestest.Pool(t)
	store := postgres.NewStore(pool)
	svc := scheduling.NewService(store, mustRules(t), func() time.Time { return now })

	srv := httptest.NewServer(httpapi.NewHandler(slog.New(slog.DiscardHandler), pool, svc))
	t.Cleanup(srv.Close)
	return srv, store
}

func mustRules(t *testing.T) scheduling.Rules {
	t.Helper()
	rules, err := scheduling.DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// seedAppointments stores the 10 records in appointments.json, in file
// order, so on an empty table they get ids 1 to 10 as in the file.
//
// It stands in for the seed loader, which is not written yet; when it is,
// this helper should call it instead.
func seedAppointments(t *testing.T, store *postgres.Store) {
	t.Helper()
	data, err := os.ReadFile("../../appointments.json")
	if err != nil {
		t.Fatal(err)
	}
	// The file says started_at and ended_at where the API says starts_at
	// and ends_at.
	var records []struct {
		ID        int64     `json:"id"`
		TrainerID int64     `json:"trainer_id"`
		UserID    int64     `json:"user_id"`
		StartedAt time.Time `json:"started_at"`
		EndedAt   time.Time `json:"ended_at"`
	}
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		a, err := store.CreateAppointment(t.Context(), scheduling.Appointment{
			TrainerID: r.TrainerID,
			UserID:    r.UserID,
			StartsAt:  r.StartedAt,
			EndsAt:    r.EndedAt,
		})
		if err != nil {
			t.Fatalf("seed record %d: %v", r.ID, err)
		}
		if a.ID != r.ID {
			t.Fatalf("seed record %d got id %d; is the table empty?", r.ID, a.ID)
		}
	}
}

// get fetches url and returns the status and body.
func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return readResponse(t, resp)
}

// post sends body as JSON to url and returns the status and body.
func post(t *testing.T, client *http.Client, url, body string) (int, string) {
	t.Helper()
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return readResponse(t, resp)
}

func readResponse(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, strings.TrimSuffix(string(body), "\n")
}

func bookingBody(trainerID, userID int64, start, end string) string {
	return fmt.Sprintf(`{"trainer_id": %d, "user_id": %d, "starts_at": %q, "ends_at": %q}`, trainerID, userID, start, end)
}

// TestSeededAvailability checks the counts from PLAN.md section 5: the 36
// slots of Thursday and Friday 2019-01-24 and 25, less each trainer's
// seed appointments. Saturday's appointments take nothing away.
func TestSeededAvailability(t *testing.T) {
	srv, store := newServer(t, seedNow)
	seedAppointments(t, store)

	tests := []struct {
		trainerID   int
		wantMissing []string
	}{
		{1, []string{"2019-01-24T09:00:00-08:00", "2019-01-24T10:00:00-08:00", "2019-01-25T10:00:00-08:00", "2019-01-25T10:30:00-08:00"}},
		{2, []string{"2019-01-24T09:00:00-08:00"}},
		{3, nil},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("trainer %d", tt.trainerID), func(t *testing.T) {
			status, body := get(t, srv.Client(), fmt.Sprintf("%s/trainers/%d/availability?starts_at=%s&ends_at=%s",
				srv.URL, tt.trainerID, "2019-01-24T00:00:00-08:00", "2019-01-27T00:00:00-08:00"))
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", status, body)
			}
			var resp struct {
				Slots []struct {
					StartsAt string `json:"starts_at"`
				} `json:"slots"`
			}
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatal(err)
			}

			var got []string
			for _, s := range resp.Slots {
				got = append(got, s.StartsAt)
			}
			if want := 36 - len(tt.wantMissing); len(got) != want {
				t.Errorf("got %d slots, want %d", len(got), want)
			}
			var missing []string
			for _, start := range thursdayAndFridayStarts() {
				if !slices.Contains(got, start) {
					missing = append(missing, start)
				}
			}
			if !slices.Equal(missing, tt.wantMissing) {
				t.Errorf("missing slots = %v, want %v", missing, tt.wantMissing)
			}
		})
	}
}

// thursdayAndFridayStarts lists the 36 slot starts on 2019-01-24 and 25.
func thursdayAndFridayStarts() []string {
	var starts []string
	for _, day := range []string{"2019-01-24", "2019-01-25"} {
		for h := 8; h < 17; h++ {
			starts = append(starts,
				fmt.Sprintf("%sT%02d:00:00-08:00", day, h),
				fmt.Sprintf("%sT%02d:30:00-08:00", day, h))
		}
	}
	return starts
}

// TestSeededAppointments checks the PLAN.md section 6 example, which
// includes past and Saturday appointments.
func TestSeededAppointments(t *testing.T) {
	srv, store := newServer(t, seedNow)
	seedAppointments(t, store)

	status, body := get(t, srv.Client(), srv.URL+"/trainers/1/appointments")

	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	want := `{"trainer_id":1,"appointments":[` +
		`{"id":1,"trainer_id":1,"user_id":1,"starts_at":"2019-01-24T09:00:00-08:00","ends_at":"2019-01-24T09:30:00-08:00"},` +
		`{"id":2,"trainer_id":1,"user_id":2,"starts_at":"2019-01-24T10:00:00-08:00","ends_at":"2019-01-24T10:30:00-08:00"},` +
		`{"id":3,"trainer_id":1,"user_id":3,"starts_at":"2019-01-25T10:00:00-08:00","ends_at":"2019-01-25T10:30:00-08:00"},` +
		`{"id":4,"trainer_id":1,"user_id":4,"starts_at":"2019-01-25T10:30:00-08:00","ends_at":"2019-01-25T11:00:00-08:00"},` +
		`{"id":5,"trainer_id":1,"user_id":5,"starts_at":"2019-01-26T10:00:00-08:00","ends_at":"2019-01-26T10:30:00-08:00"}]}`
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

// TestSeededBooking books against the seed data, then checks that the
// new appointment shows up in both reads.
func TestSeededBooking(t *testing.T) {
	srv, store := newServer(t, seedNow)
	seedAppointments(t, store)
	client := srv.Client()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"between seed ids 1 and 2 is free, and gets the next id", bookingBody(1, 42, "2019-01-24T09:30:00-08:00", "2019-01-24T10:00:00-08:00"),
			http.StatusCreated, `{"id":11,"trainer_id":1,"user_id":42,"starts_at":"2019-01-24T09:30:00-08:00","ends_at":"2019-01-24T10:00:00-08:00"}`},
		{"seed id 1's slot is taken", bookingBody(1, 42, "2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00"),
			http.StatusConflict, `{"error":{"code":"slot_unavailable","message":"trainer 1 already has an appointment overlapping 2019-01-24T09:00:00-08:00 to 2019-01-24T09:30:00-08:00"}}`},
		{"the same slot again", bookingBody(1, 43, "2019-01-24T09:30:00-08:00", "2019-01-24T10:00:00-08:00"),
			http.StatusConflict, `{"error":{"code":"slot_unavailable","message":"trainer 1 already has an appointment overlapping 2019-01-24T09:30:00-08:00 to 2019-01-24T10:00:00-08:00"}}`},
		{"Saturday is refused before the overlap check", bookingBody(3, 42, "2019-01-26T13:30:00-08:00", "2019-01-26T14:00:00-08:00"),
			http.StatusUnprocessableEntity, `{"error":{"code":"not_business_day","message":"starts_at is on a Saturday in America/Los_Angeles, which is not a business day","field":"starts_at"}}`},
	}

	// The cases run in order against one database, so later ones see the
	// appointments that earlier ones created. The 201 comes first because
	// a failed insert still uses up an id from the sequence.
	for _, tt := range tests {
		status, body := post(t, client, srv.URL+"/appointments", tt.body)
		if status != tt.wantStatus || body != tt.wantBody {
			t.Errorf("%s: got %d %s, want %d %s", tt.name, status, body, tt.wantStatus, tt.wantBody)
		}
	}

	_, body := get(t, client, srv.URL+"/trainers/1/appointments")
	if !strings.Contains(body, `{"id":11,`) {
		t.Errorf("trainer 1's appointments do not include the new id 11: %s", body)
	}
	_, body = get(t, client, srv.URL+"/trainers/1/availability?starts_at=2019-01-24T09:00:00-08:00&ends_at=2019-01-24T10:30:00-08:00")
	if want := `{"trainer_id":1,"slots":[]}`; body != want {
		t.Errorf("availability = %s, want %s", body, want)
	}
}

// TestConcurrentBookingOverHTTP races n requests for the same slot and
// checks that exactly one gets 201, the rest get 409, and the database
// holds exactly one row for the slot.
func TestConcurrentBookingOverHTTP(t *testing.T) {
	// Repeating over distinct slots gives the requests many chances to
	// interleave, rather than one lucky schedule.
	const slotCount = 25
	now := time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC)

	for _, n := range []int{2, 20} {
		t.Run(fmt.Sprintf("%d-way", n), func(t *testing.T) {
			srv, store := newServer(t, now)

			// Monday's 18 slots and Tuesday's first 7, 2026-11-16 and 17.
			rules := mustRules(t)
			slots := rules.Slots(
				time.Date(2026, 11, 16, 0, 0, 0, 0, rules.Location),
				time.Date(2026, 11, 18, 0, 0, 0, 0, rules.Location),
				now, nil)[:slotCount]

			for _, slot := range slots {
				statuses := postAtOnce(t, srv, n, slot)

				created := 0
				conflicts := 0
				for _, status := range statuses {
					switch status {
					case http.StatusCreated:
						created++
					case http.StatusConflict:
						conflicts++
					default:
						t.Fatalf("slot %s: unexpected status %d", slot.Start, status)
					}
				}
				if created != 1 || conflicts != n-1 {
					t.Errorf("slot %s: %d created and %d conflicts, want 1 and %d", slot.Start, created, conflicts, n-1)
				}

				rows, err := store.ListOverlapping(t.Context(), 1, slot.Start, slot.End)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) != 1 {
					t.Errorf("slot %s: %d rows in the database, want 1", slot.Start, len(rows))
				}
			}
		})
	}
}

// postAtOnce starts n goroutines that each POST a booking of slot for
// trainer 1, as a different user, and releases them together. It returns
// each request's status.
func postAtOnce(t *testing.T, srv *httptest.Server, n int, slot scheduling.Interval) []int {
	t.Helper()
	start := make(chan struct{})
	statuses := make([]int, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := range n {
		body := bookingBody(1, int64(i+1), slot.Start.Format(time.RFC3339), slot.End.Format(time.RFC3339))
		wg.Go(func() {
			<-start // wait so all n requests begin together
			resp, err := srv.Client().Post(srv.URL+"/appointments", "application/json", strings.NewReader(body))
			if err != nil {
				errs[i] = err
				return
			}
			statuses[i] = resp.StatusCode
			errs[i] = resp.Body.Close()
		})
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	return statuses
}
