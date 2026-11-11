package postgres_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dontfollowsean/future-scheduling-api/internal/postgres"
	"github.com/dontfollowsean/future-scheduling-api/internal/postgres/postgrestest"
	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
)

func newStore(t *testing.T) *postgres.Store {
	t.Helper()
	return postgres.NewStore(postgrestest.Pool(t))
}

// at parses an RFC 3339 timestamp.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

// book creates an appointment that the test expects to succeed.
func book(t *testing.T, store *postgres.Store, trainerID int64, start, end string) scheduling.Appointment {
	t.Helper()
	a, err := store.CreateAppointment(t.Context(), scheduling.Appointment{
		TrainerID: trainerID,
		UserID:    1,
		StartsAt:  at(t, start),
		EndsAt:    at(t, end),
	})
	if err != nil {
		t.Fatalf("book trainer %d %s to %s: %v", trainerID, start, end, err)
	}
	return a
}

func ids(appts []scheduling.Appointment) []int64 {
	ids := []int64{}
	for _, a := range appts {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := postgrestest.Pool(t) // already migrated once

	version, err := postgres.Migrate(t.Context(), pool)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1 (00001_create_appointments.sql)", version)
	}
}

func TestCreateAppointment(t *testing.T) {
	store := newStore(t)
	in := scheduling.Appointment{
		TrainerID: 1,
		UserID:    42,
		StartsAt:  at(t, "2026-11-16T10:00:00-08:00"),
		EndsAt:    at(t, "2026-11-16T10:30:00-08:00"),
	}

	got, err := store.CreateAppointment(t.Context(), in)
	if err != nil {
		t.Fatalf("CreateAppointment: %v", err)
	}
	if got.ID != 1 {
		t.Errorf("ID = %d, want 1", got.ID)
	}
	if got.TrainerID != in.TrainerID || got.UserID != in.UserID {
		t.Errorf("trainer, user = %d, %d, want %d, %d", got.TrainerID, got.UserID, in.TrainerID, in.UserID)
	}
	// Equal compares instants; pgx returns them in time.Local, not -08:00.
	if !got.StartsAt.Equal(in.StartsAt) || !got.EndsAt.Equal(in.EndsAt) {
		t.Errorf("times = %s to %s, want %s to %s", got.StartsAt, got.EndsAt, in.StartsAt, in.EndsAt)
	}
}

func TestCreateAppointmentOverlap(t *testing.T) {
	// Every case starts from one 60-minute appointment for trainer 3, like
	// seed record id 9: 13:00 to 14:00 PST on 2019-01-26.
	tests := []struct {
		name      string
		trainerID int64
		start     string
		end       string
		wantErr   error
	}{
		{"same start", 3, "2019-01-26T13:00:00-08:00", "2019-01-26T13:30:00-08:00", scheduling.ErrSlotTaken},
		{"inside the 60-minute appointment", 3, "2019-01-26T13:30:00-08:00", "2019-01-26T14:00:00-08:00", scheduling.ErrSlotTaken},
		{"straddles its start", 3, "2019-01-26T12:30:00-08:00", "2019-01-26T13:30:00-08:00", scheduling.ErrSlotTaken},
		{"covers it entirely", 3, "2019-01-26T12:00:00-08:00", "2019-01-26T15:00:00-08:00", scheduling.ErrSlotTaken},
		{"13:30 PST written in UTC", 3, "2019-01-26T21:30:00Z", "2019-01-26T22:00:00Z", scheduling.ErrSlotTaken},
		{"13:30 PST written in +05:45", 3, "2019-01-27T03:15:00+05:45", "2019-01-27T03:45:00+05:45", scheduling.ErrSlotTaken},
		{"back-to-back before", 3, "2019-01-26T12:30:00-08:00", "2019-01-26T13:00:00-08:00", nil},
		{"back-to-back after", 3, "2019-01-26T14:00:00-08:00", "2019-01-26T14:30:00-08:00", nil},
		{"same time, different trainer", 1, "2019-01-26T13:00:00-08:00", "2019-01-26T13:30:00-08:00", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(t)
			book(t, store, 3, "2019-01-26T13:00:00-08:00", "2019-01-26T14:00:00-08:00")

			_, err := store.CreateAppointment(t.Context(), scheduling.Appointment{
				TrainerID: tt.trainerID,
				UserID:    2,
				StartsAt:  at(t, tt.start),
				EndsAt:    at(t, tt.end),
			})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCreateAppointmentCheckConstraints(t *testing.T) {
	start := "2026-11-16T10:00:00-08:00"
	end := "2026-11-16T10:30:00-08:00"
	tests := []struct {
		name           string
		trainerID      int64
		userID         int64
		start, end     string
		wantConstraint string
	}{
		{"zero length", 1, 1, start, start, "appointments_ends_after_starts"},
		{"ends before it starts", 1, 1, end, start, "appointments_ends_after_starts"},
		{"trainer id zero", 0, 1, start, end, "appointments_trainer_id_check"},
		{"negative user id", 1, -1, start, end, "appointments_user_id_check"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore(t)

			_, err := store.CreateAppointment(t.Context(), scheduling.Appointment{
				TrainerID: tt.trainerID,
				UserID:    tt.userID,
				StartsAt:  at(t, tt.start),
				EndsAt:    at(t, tt.end),
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) {
				t.Fatalf("err = %v, want a Postgres error", err)
			}
			if pgErr.Code != "23514" || pgErr.ConstraintName != tt.wantConstraint {
				t.Errorf("got %s %s, want 23514 (check_violation) %s", pgErr.Code, pgErr.ConstraintName, tt.wantConstraint)
			}
		})
	}
}

func TestListByTrainer(t *testing.T) {
	store := newStore(t)
	// Inserted out of order, so the IDs do not already sort by start.
	fri := book(t, store, 1, "2019-01-25T10:00:00-08:00", "2019-01-25T10:30:00-08:00")
	thu10 := book(t, store, 1, "2019-01-24T10:00:00-08:00", "2019-01-24T10:30:00-08:00")
	thu9 := book(t, store, 1, "2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00")
	book(t, store, 2, "2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00")

	got, err := store.ListByTrainer(t.Context(), 1)
	if err != nil {
		t.Fatalf("ListByTrainer: %v", err)
	}
	if want := []int64{thu9.ID, thu10.ID, fri.ID}; !slices.Equal(ids(got), want) {
		t.Errorf("IDs = %v, want %v", ids(got), want)
	}

	got, err = store.ListByTrainer(t.Context(), 99)
	if err != nil {
		t.Fatalf("ListByTrainer for a trainer with no appointments: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %#v, want an empty, non-nil slice", got)
	}
}

func TestListOverlapping(t *testing.T) {
	store := newStore(t)
	// Trainer 1 has 09:00-09:30, 10:00-11:00 and 11:00-11:30 on Monday
	// 2026-11-16, inserted out of order. Trainer 2's booking must never
	// appear.
	tenToEleven := book(t, store, 1, "2026-11-16T10:00:00-08:00", "2026-11-16T11:00:00-08:00")
	eleven := book(t, store, 1, "2026-11-16T11:00:00-08:00", "2026-11-16T11:30:00-08:00")
	nine := book(t, store, 1, "2026-11-16T09:00:00-08:00", "2026-11-16T09:30:00-08:00")
	book(t, store, 2, "2026-11-16T10:00:00-08:00", "2026-11-16T10:30:00-08:00")

	tests := []struct {
		name     string
		from, to string
		want     []int64
	}{
		{"whole morning, in start order", "2026-11-16T08:00:00-08:00", "2026-11-16T12:00:00-08:00", []int64{nine.ID, tenToEleven.ID, eleven.ID}},
		{"touching ends are not overlaps", "2026-11-16T09:30:00-08:00", "2026-11-16T11:00:00-08:00", []int64{tenToEleven.ID}},
		{"window inside the 60-minute booking", "2026-11-16T10:30:00-08:00", "2026-11-16T10:45:00-08:00", []int64{tenToEleven.ID}},
		{"gap between bookings", "2026-11-16T09:30:00-08:00", "2026-11-16T10:00:00-08:00", []int64{}},
		{"window written in UTC", "2026-11-16T17:00:00Z", "2026-11-16T17:30:00Z", []int64{nine.ID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.ListOverlapping(t.Context(), 1, at(t, tt.from), at(t, tt.to))
			if err != nil {
				t.Fatalf("ListOverlapping: %v", err)
			}
			if !slices.Equal(ids(got), tt.want) {
				t.Errorf("IDs = %v, want %v", ids(got), tt.want)
			}
		})
	}
}

// TestCreateAppointmentConcurrent races n inserts of the same slot and
// checks that the database lets exactly one through.
func TestCreateAppointmentConcurrent(t *testing.T) {
	// Repeating over distinct slots gives the inserts many chances to
	// interleave, rather than one lucky schedule.
	const slots = 25

	for _, n := range []int{2, 20} {
		t.Run(fmt.Sprintf("%d-way", n), func(t *testing.T) {
			store := newStore(t)
			first := at(t, "2026-11-16T08:00:00-08:00")

			for i := range slots {
				start := first.Add(time.Duration(i) * 30 * time.Minute)
				errs := insertAtOnce(t, store, n, scheduling.Appointment{
					TrainerID: 1,
					StartsAt:  start,
					EndsAt:    start.Add(30 * time.Minute),
				})

				created, taken := 0, 0
				for _, err := range errs {
					switch {
					case err == nil:
						created++
					case errors.Is(err, scheduling.ErrSlotTaken):
						taken++
					default:
						t.Fatalf("slot %s: unexpected error: %v", start, err)
					}
				}
				if created != 1 || taken != n-1 {
					t.Errorf("slot %s: %d created and %d taken, want 1 and %d", start, created, taken, n-1)
				}
			}

			got, err := store.ListByTrainer(t.Context(), 1)
			if err != nil {
				t.Fatalf("ListByTrainer: %v", err)
			}
			if len(got) != slots {
				t.Errorf("%d rows, want exactly one per slot (%d)", len(got), slots)
			}
		})
	}
}

// insertAtOnce starts n goroutines that each insert a, as a different
// user, and releases them together. It returns each insert's error.
func insertAtOnce(t *testing.T, store *postgres.Store, n int, a scheduling.Appointment) []error {
	start := make(chan struct{})
	errs := make([]error, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start // wait so all n inserts begin together
			b := a
			b.UserID = int64(i + 1)
			_, errs[i] = store.CreateAppointment(t.Context(), b)
		})
	}
	close(start)
	wg.Wait()
	return errs
}
