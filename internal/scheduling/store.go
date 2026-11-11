package scheduling

import (
	"context"
	"time"
)

// Appointment is one booked video call between a trainer and a client.
// ID is assigned by the store.
type Appointment struct {
	ID        int64
	TrainerID int64
	UserID    int64
	StartsAt  time.Time
	EndsAt    time.Time
}

// Store is the storage the scheduling rules need. It is defined here, by
// its consumer, and implemented by the postgres package.
type Store interface {
	// CreateAppointment stores a and returns it with its ID set. It returns
	// ErrSlotTaken if the trainer already has an overlapping appointment.
	CreateAppointment(ctx context.Context, a Appointment) (Appointment, error)
	// ListByTrainer returns every appointment for the trainer, ordered by
	// start time.
	ListByTrainer(ctx context.Context, trainerID int64) ([]Appointment, error)
	// ListOverlapping returns the trainer's appointments that overlap the
	// half-open window [from, to), ordered by start time.
	ListOverlapping(ctx context.Context, trainerID int64, from, to time.Time) ([]Appointment, error)
}
