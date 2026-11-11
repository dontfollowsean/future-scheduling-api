// Package postgres stores appointments in Postgres. It owns the SQL, the
// migrations, and the translation of database errors into domain errors.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
)

const (
	// exclusionViolation is the SQLSTATE Postgres raises when an insert
	// breaks an exclusion constraint.
	exclusionViolation = "23P01"
	// overlapConstraint is the exclusion constraint that stops a trainer's
	// appointments overlapping (see migrations/00001_create_appointments.sql).
	overlapConstraint = "appointments_no_trainer_overlap"
)

// Store implements scheduling.Store on a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

var _ scheduling.Store = (*Store)(nil)

// NewStore returns a Store that runs its queries on pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateAppointment inserts a and returns it with its new ID. The insert
// is the availability check: nothing is read first, and the exclusion
// constraint rejects an overlap atomically, even when requests race.
func (s *Store) CreateAppointment(ctx context.Context, a scheduling.Appointment) (scheduling.Appointment, error) {
	var created scheduling.Appointment
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Queue bookings for the same trainer until this transaction ends.
		// The constraint alone keeps overlaps out, but two racing inserts
		// of overlapping rows can each wait for the other, and Postgres
		// then aborts one with deadlock_detected (40P01) after a 1s
		// deadlock_timeout. With the lock, the later insert waits here,
		// sees the earlier row once it commits, and fails with 23P01.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", a.TrainerID); err != nil {
			return fmt.Errorf("lock trainer %d: %w", a.TrainerID, err)
		}

		row := tx.QueryRow(ctx, `
			INSERT INTO appointments (trainer_id, user_id, starts_at, ends_at)
			VALUES ($1, $2, $3, $4)
			RETURNING id, trainer_id, user_id, starts_at, ends_at`,
			a.TrainerID, a.UserID, a.StartsAt, a.EndsAt)
		var err error
		created, err = scanAppointment(row)
		return err
	})
	if isOverlap(err) {
		return scheduling.Appointment{}, scheduling.ErrSlotTaken
	}
	if err != nil {
		return scheduling.Appointment{}, fmt.Errorf("insert appointment: %w", err)
	}
	return created, nil
}

// ListByTrainer returns all of the trainer's appointments, past ones
// included, ordered by start time and then ID.
func (s *Store) ListByTrainer(ctx context.Context, trainerID int64) ([]scheduling.Appointment, error) {
	appts, err := s.list(ctx, `
		SELECT id, trainer_id, user_id, starts_at, ends_at
		FROM appointments
		WHERE trainer_id = $1
		ORDER BY starts_at, id`,
		trainerID)
	if err != nil {
		return nil, fmt.Errorf("list appointments for trainer %d: %w", trainerID, err)
	}
	return appts, nil
}

// ListOverlapping returns the trainer's appointments that overlap the
// half-open window [from, to), ordered by start time. An appointment that
// ends exactly at from, or starts exactly at to, does not overlap.
// from must not be after to.
func (s *Store) ListOverlapping(ctx context.Context, trainerID int64, from, to time.Time) ([]scheduling.Appointment, error) {
	// The range expression matches the exclusion constraint's, so its GiST
	// index serves this query.
	appts, err := s.list(ctx, `
		SELECT id, trainer_id, user_id, starts_at, ends_at
		FROM appointments
		WHERE trainer_id = $1
		  AND tstzrange(starts_at, ends_at, '[)') && tstzrange($2, $3, '[)')
		ORDER BY starts_at`,
		trainerID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list appointments for trainer %d overlapping [%s, %s): %w", trainerID, from, to, err)
	}
	return appts, nil
}

// list runs a query that selects appointment columns and returns the rows.
// The result is never nil, so an empty list encodes as [] in JSON.
func (s *Store) list(ctx context.Context, query string, args ...any) ([]scheduling.Appointment, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	appts := []scheduling.Appointment{}
	for rows.Next() {
		a, err := scanAppointment(rows)
		if err != nil {
			return nil, err
		}
		appts = append(appts, a)
	}
	return appts, rows.Err()
}

// scanAppointment reads one row of id, trainer_id, user_id, starts_at,
// ends_at. pgx returns timestamps in time.Local; callers that format them
// must convert to the business zone first.
func scanAppointment(row pgx.Row) (scheduling.Appointment, error) {
	var a scheduling.Appointment
	err := row.Scan(&a.ID, &a.TrainerID, &a.UserID, &a.StartsAt, &a.EndsAt)
	return a, err
}

// isOverlap reports whether err is the database rejecting an appointment
// that overlaps another one for the same trainer.
func isOverlap(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == exclusionViolation &&
		pgErr.ConstraintName == overlapConstraint
}
