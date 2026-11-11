package scheduling

import (
	"context"
	"time"
)

// Service is what the API calls: it applies the rules, using its own
// clock, and reads and writes appointments through a Store.
type Service struct {
	store Store
	rules Rules
	now   func() time.Time
}

// NewService returns a Service. now is time.Now in production and a fixed
// instant in tests.
func NewService(store Store, rules Rules, now func() time.Time) *Service {
	return &Service{store: store, rules: rules, now: now}
}

// Location is the business time zone, which responses render times in.
func (s *Service) Location() *time.Location {
	return s.rules.Location
}

// Book checks a against the booking rules and stores it. It returns a
// *ValidationError for the first rule a breaks, ErrSlotTaken if the
// trainer already has an overlapping appointment, or the created
// appointment with its ID set.
func (s *Service) Book(ctx context.Context, a Appointment) (Appointment, error) {
	if err := s.rules.ValidateBooking(a.StartsAt, a.EndsAt, s.now()); err != nil {
		return Appointment{}, err
	}
	return s.store.CreateAppointment(ctx, a)
}

// Availability returns the trainer's free slots in the window [from, to),
// in ascending order and never nil. Slots in the past are hidden. It
// returns a *ValidationError if the window is empty, backwards or too long.
func (s *Service) Availability(ctx context.Context, trainerID int64, from, to time.Time) ([]Interval, error) {
	if err := s.rules.ValidateWindow(from, to); err != nil {
		return nil, err
	}
	appts, err := s.store.ListOverlapping(ctx, trainerID, from, to)
	if err != nil {
		return nil, err
	}

	booked := make([]Interval, 0, len(appts))
	for _, a := range appts {
		booked = append(booked, Interval{Start: a.StartsAt, End: a.EndsAt})
	}
	return s.rules.Slots(from, to, s.now(), booked), nil
}

// ListForTrainer returns every appointment the trainer has, past ones
// included, ordered by start time.
func (s *Service) ListForTrainer(ctx context.Context, trainerID int64) ([]Appointment, error) {
	return s.store.ListByTrainer(ctx, trainerID)
}
