// Package seed loads existing appointments from a JSON file, such as
// appointments.json, into the store at startup.
//
// The file is a record of appointments that already exist, so records that
// break the booking rules (a Saturday, a 60-minute call) are loaded as-is
// and logged as warnings. The seed fails, writing nothing, only when the
// file is corrupt or a record overlaps another appointment for its trainer.
package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
)

// Store is the storage the seed needs. postgres.Store implements it.
type Store interface {
	// SeedAppointments inserts appts with their own IDs in one transaction,
	// skipping IDs that are already stored, and returns how many it
	// inserted. It returns an error wrapping scheduling.ErrSlotTaken on an
	// overlap, and then writes nothing.
	SeedAppointments(ctx context.Context, appts []scheduling.Appointment) (int, error)
}

// Result summarises one seed run.
type Result struct {
	Inserted int // records written by this run
	Skipped  int // records whose ID was already stored
	Warnings int // records that break the booking rules
}

// record is one appointment as the seed file writes it. The file says
// started_at and ended_at where the API says starts_at and ends_at.
type record struct {
	ID        int64     `json:"id"`
	TrainerID int64     `json:"trainer_id"`
	UserID    int64     `json:"user_id"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Run reads the seed file at path, logs a warning for each record that
// breaks rules, and stores every record. Running it again with the same
// file inserts nothing.
func Run(ctx context.Context, logger *slog.Logger, store Store, rules scheduling.Rules, path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read seed file: %w", err)
	}

	appts, err := Read(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("read seed file %s: %w", path, err)
	}

	warnings := 0
	for _, a := range appts {
		violations := rules.Violations(a.StartsAt, a.EndsAt)
		if len(violations) == 0 {
			continue
		}
		warnings++
		var reasons []string
		for _, v := range violations {
			reasons = append(reasons, v.Message)
		}
		logger.Warn("seed record breaks booking rules",
			"id", a.ID,
			"trainer_id", a.TrainerID,
			"reasons", strings.Join(reasons, "; "),
		)
	}

	inserted, err := store.SeedAppointments(ctx, appts)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Inserted: inserted,
		Skipped:  len(appts) - inserted,
		Warnings: warnings,
	}, nil
}

// Read decodes a seed file: a JSON array of records with the fields id,
// trainer_id, user_id, started_at and ended_at. It returns an error if the
// file is not exactly that, or if any record is structurally impossible:
// a missing or non-positive ID, a missing timestamp or one without an
// offset, an end that is not after the start, or a repeated ID.
//
// Read does not check the booking rules; Run only warns about those.
func Read(r io.Reader) ([]scheduling.Appointment, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var records []record
	if err := dec.Decode(&records); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode JSON: unexpected data after the array")
	}

	appts := []scheduling.Appointment{}
	seen := map[int64]bool{}
	for i, rec := range records {
		if err := rec.check(); err != nil {
			return nil, fmt.Errorf("record %d (id %d): %w", i, rec.ID, err)
		}
		if seen[rec.ID] {
			return nil, fmt.Errorf("record %d: id %d appears more than once", i, rec.ID)
		}
		seen[rec.ID] = true

		appts = append(appts, scheduling.Appointment{
			ID:        rec.ID,
			TrainerID: rec.TrainerID,
			UserID:    rec.UserID,
			StartsAt:  rec.StartedAt,
			EndsAt:    rec.EndedAt,
		})
	}
	return appts, nil
}

// check reports the first structural problem with rec. JSON decoding has
// already rejected timestamps that are not RFC 3339 with an offset, so a
// zero time here means the field was missing.
func (rec record) check() error {
	switch {
	case rec.ID <= 0:
		return errors.New("id must be a positive integer")
	case rec.TrainerID <= 0:
		return errors.New("trainer_id must be a positive integer")
	case rec.UserID <= 0:
		return errors.New("user_id must be a positive integer")
	case rec.StartedAt.IsZero():
		return errors.New("started_at is required")
	case rec.EndedAt.IsZero():
		return errors.New("ended_at is required")
	case !rec.EndedAt.After(rec.StartedAt):
		return errors.New("ended_at must be after started_at")
	}
	return nil
}
