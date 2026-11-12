package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
)

// maxBodyBytes caps a request body, so one request cannot exhaust memory.
const maxBodyBytes = 1 << 20

// handlers serves the scheduling endpoints. Each one parses its input,
// answering 400 when it cannot, calls the Service, and encodes the result.
type handlers struct {
	logger *slog.Logger
	svc    *scheduling.Service
}

type appointmentJSON struct {
	ID        int64  `json:"id"`
	TrainerID int64  `json:"trainer_id"`
	UserID    int64  `json:"user_id"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
}

type slotJSON struct {
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`
}

type availabilityResponse struct {
	TrainerID int64      `json:"trainer_id"`
	Slots     []slotJSON `json:"slots"`
}

type appointmentsResponse struct {
	TrainerID    int64             `json:"trainer_id"`
	Appointments []appointmentJSON `json:"appointments"`
}

// bookingRequest is the body of POST /appointments. The times are strings
// so that a bad one is a 400 naming the field, not a generic JSON error.
type bookingRequest struct {
	TrainerID int64  `json:"trainer_id"`
	UserID    int64  `json:"user_id"`
	StartsAt  string `json:"starts_at"`
	EndsAt    string `json:"ends_at"`
}

// availability serves GET /trainers/{trainer_id}/availability?starts_at=&ends_at=.
func (h *handlers) availability(w http.ResponseWriter, r *http.Request) {
	trainerID, err := parseTrainerID(r)
	if err != nil {
		writeInvalidField(w, "trainer_id", err.Error())
		return
	}
	query := r.URL.Query()
	from, err := parseTime("starts_at", query.Get("starts_at"))
	if err != nil {
		writeInvalidField(w, "starts_at", err.Error())
		return
	}
	to, err := parseTime("ends_at", query.Get("ends_at"))
	if err != nil {
		writeInvalidField(w, "ends_at", err.Error())
		return
	}

	slots, err := h.svc.Availability(r.Context(), trainerID, from, to)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	resp := availabilityResponse{TrainerID: trainerID, Slots: make([]slotJSON, 0, len(slots))}
	for _, s := range slots {
		resp.Slots = append(resp.Slots, slotJSON{
			StartsAt: formatTime(s.Start, h.svc.Location()),
			EndsAt:   formatTime(s.End, h.svc.Location()),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// book serves POST /appointments. Its checks run in the order of
// PLAN.md section 3: the body, the ids, the times, then the Service's
// rules, and finally the database's overlap check.
func (h *handlers) book(w http.ResponseWriter, r *http.Request) {
	var req bookingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.TrainerID <= 0 {
		writeInvalidField(w, "trainer_id", "trainer_id must be a positive integer")
		return
	}
	if req.UserID <= 0 {
		writeInvalidField(w, "user_id", "user_id must be a positive integer")
		return
	}
	start, err := parseTime("starts_at", req.StartsAt)
	if err != nil {
		writeInvalidField(w, "starts_at", err.Error())
		return
	}
	end, err := parseTime("ends_at", req.EndsAt)
	if err != nil {
		writeInvalidField(w, "ends_at", err.Error())
		return
	}

	appt, err := h.svc.Book(r.Context(), scheduling.Appointment{
		TrainerID: req.TrainerID,
		UserID:    req.UserID,
		StartsAt:  start,
		EndsAt:    end,
	})
	if errors.Is(err, scheduling.ErrSlotTaken) {
		// Say which time is taken, but not who holds it.
		writeError(w, http.StatusConflict, "slot_unavailable", fmt.Sprintf(
			"trainer %d already has an appointment overlapping %s to %s",
			req.TrainerID, formatTime(start, h.svc.Location()), formatTime(end, h.svc.Location())))
		return
	}
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, h.appointmentJSON(appt))
}

// listAppointments serves GET /trainers/{trainer_id}/appointments.
func (h *handlers) listAppointments(w http.ResponseWriter, r *http.Request) {
	trainerID, err := parseTrainerID(r)
	if err != nil {
		writeInvalidField(w, "trainer_id", err.Error())
		return
	}

	appts, err := h.svc.ListForTrainer(r.Context(), trainerID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	resp := appointmentsResponse{TrainerID: trainerID, Appointments: make([]appointmentJSON, 0, len(appts))}
	for _, a := range appts {
		resp.Appointments = append(resp.Appointments, h.appointmentJSON(a))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) appointmentJSON(a scheduling.Appointment) appointmentJSON {
	return appointmentJSON{
		ID:        a.ID,
		TrainerID: a.TrainerID,
		UserID:    a.UserID,
		StartsAt:  formatTime(a.StartsAt, h.svc.Location()),
		EndsAt:    formatTime(a.EndsAt, h.svc.Location()),
	}
}

// writeServiceError answers 422 for an error that breaks a scheduling
// rule. Any other error is unexpected: it is logged and answered with 500,
// so its details never reach the client.
func (h *handlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *scheduling.ValidationError
	if errors.As(err, &verr) {
		writeFieldError(w, http.StatusUnprocessableEntity, verr.Code, verr.Field, verr.Message)
		return
	}
	h.logger.ErrorContext(r.Context(), "request failed",
		"request_id", RequestID(r.Context()), "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}

// writeInvalidField answers 400 for an input that is missing or malformed.
func writeInvalidField(w http.ResponseWriter, field, message string) {
	writeFieldError(w, http.StatusBadRequest, "invalid_field", field, message)
}

// parseTrainerID reads the {trainer_id} path value, which must be a
// positive integer.
func parseTrainerID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("trainer_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("trainer_id must be a positive integer")
	}
	return id, nil
}

// parseTime parses value, the input named field, as RFC 3339 with an
// explicit offset. Its error message is meant for the client.
func parseTime(field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required (RFC 3339 with offset)", field)
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC 3339 with an offset, such as 2026-11-16T09:00:00-08:00", field)
	}
	return t, nil
}

// decodeJSON decodes the request body into v. The body must be a single
// JSON object of at most maxBodyBytes, with no fields that v lacks. Its
// error message is meant for the client.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		var tooLarge *http.MaxBytesError
		var wrongType *json.UnmarshalTypeError
		switch {
		case errors.As(err, &tooLarge):
			return fmt.Errorf("request body must be at most %d bytes", tooLarge.Limit)
		case errors.Is(err, io.EOF):
			return errors.New("request body must be a JSON object")
		case errors.As(err, &wrongType) && wrongType.Field != "":
			return fmt.Errorf("json: %s cannot be a %s", wrongType.Field, wrongType.Value)
		case errors.As(err, &wrongType):
			return errors.New("request body must be a JSON object")
		default:
			// Syntax errors and unknown fields, such as
			// json: unknown field "started_at", read well as they are.
			return err
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must be a single JSON object")
	}
	return nil
}
