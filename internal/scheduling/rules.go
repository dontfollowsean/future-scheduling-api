// Package scheduling holds the booking rules and slot generation. It is
// pure: no HTTP, no SQL, and no clock of its own; callers pass "now" in.
package scheduling

import (
	"fmt"
	"slices"
	"time"
)

// maxWindowDays is the longest availability window, in calendar days.
// 31 days hold at most 23 business days, so at most 805 slots.
const maxWindowDays = 31

// ClockTime is a wall-clock time of day, such as 08:00, with no date or
// time zone. Opening hours are stored this way, never as "midnight + 8h",
// which would be 09:00 on a spring-forward day.
type ClockTime struct {
	Hour   int
	Minute int
}

func (c ClockTime) String() string {
	return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute)
}

// Rules says when appointments may be booked. Every rule is evaluated in
// Location's wall-clock time, whatever offset the input was written in.
type Rules struct {
	Location *time.Location
	Open     ClockTime // the earliest start
	Close    ClockTime // the latest end
	// SlotLength is the length of an appointment.
	SlotLength time.Duration
	// Grid is the step that starts fall on: 15 minutes means starts at
	// :00, :15, :30 or :45.
	Grid     time.Duration
	Workdays []time.Weekday
}

// DefaultRules returns the rules from the spec: 30-minute appointments
// starting at :00, :15, :30 or :45, Monday to Friday, 08:00 to 17:00
// Pacific time.
func DefaultRules() (Rules, error) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return Rules{}, fmt.Errorf("load business time zone: %w", err)
	}
	return Rules{
		Location:   loc,
		Open:       ClockTime{Hour: 8, Minute: 0},
		Close:      ClockTime{Hour: 17, Minute: 0},
		SlotLength: 30 * time.Minute,
		Grid:       15 * time.Minute,
		Workdays:   []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
	}, nil
}

// Violations returns every rule that an appointment from start to end
// breaks, in the order the API checks them, or nil if it breaks none.
//
// It ignores the clock, so the seed loader can check past appointments
// with it. ValidateBooking adds the "not in the past" rule.
func (r Rules) Violations(start, end time.Time) []ValidationError {
	var violations []ValidationError
	local := start.In(r.Location)

	if d := end.Sub(start); d != r.SlotLength {
		violations = append(violations, ValidationError{
			Code:    CodeInvalidDuration,
			Field:   "ends_at",
			Message: fmt.Sprintf("an appointment must last %v, not %v", r.SlotLength, d),
		})
	}
	if !r.onGrid(local) {
		violations = append(violations, ValidationError{
			Code:    CodeOffGrid,
			Field:   "starts_at",
			Message: fmt.Sprintf("starts_at must be on a %d-minute boundary in %s, with zero seconds", int(r.Grid.Minutes()), r.Location),
		})
	}
	if !slices.Contains(r.Workdays, local.Weekday()) {
		violations = append(violations, ValidationError{
			Code:    CodeNotBusinessDay,
			Field:   "starts_at",
			Message: fmt.Sprintf("starts_at is on a %s in %s, which is not a business day", local.Weekday(), r.Location),
		})
	}
	if start.Before(r.at(local, r.Open)) || end.After(r.at(local, r.Close)) {
		violations = append(violations, ValidationError{
			Code:    CodeOutsideBusinessHours,
			Field:   "starts_at",
			Message: fmt.Sprintf("appointments must start at or after %v and end by %v %s", r.Open, r.Close, r.Location),
		})
	}
	return violations
}

// ValidateBooking checks a new booking against every rule and returns the
// first one it breaks as a *ValidationError, or nil. A start equal to now
// is allowed.
func (r Rules) ValidateBooking(start, end, now time.Time) error {
	if violations := r.Violations(start, end); len(violations) > 0 {
		return &violations[0]
	}
	if start.Before(now) {
		return &ValidationError{
			Code:    CodeInPast,
			Field:   "starts_at",
			Message: "starts_at is in the past",
		}
	}
	return nil
}

// ValidateWindow checks an availability window [from, to) and returns a
// *ValidationError if it is empty, backwards or too long, or nil.
func (r Rules) ValidateWindow(from, to time.Time) error {
	if !from.Before(to) {
		return &ValidationError{
			Code:    CodeInvalidRange,
			Message: "starts_at must be before ends_at",
		}
	}
	// Calendar days in the business zone, so a window that crosses a DST
	// change is not an hour short or long.
	if to.After(from.In(r.Location).AddDate(0, 0, maxWindowDays)) {
		return &ValidationError{
			Code:    CodeRangeTooLarge,
			Message: fmt.Sprintf("the window between starts_at and ends_at must be %d days or less", maxWindowDays),
		}
	}
	return nil
}

// onGrid reports whether local, a wall-clock time in r.Location, is a
// whole number of grid steps past midnight with no seconds.
func (r Rules) onGrid(local time.Time) bool {
	if local.Second() != 0 || local.Nanosecond() != 0 {
		return false
	}
	minutes := local.Hour()*60 + local.Minute()
	return minutes%int(r.Grid.Minutes()) == 0
}

// at returns the instant when the wall clock in r.Location reads c on t's
// date in r.Location.
func (r Rules) at(t time.Time, c ClockTime) time.Time {
	year, month, day := t.In(r.Location).Date()
	return time.Date(year, month, day, c.Hour, c.Minute, 0, 0, r.Location)
}
