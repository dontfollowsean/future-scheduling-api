package scheduling

import (
	"slices"
	"time"
)

// Interval is the half-open span of time [Start, End). Appointments that
// touch end to start, such as 10:00-10:30 and 10:30-11:00, do not overlap.
type Interval struct {
	Start time.Time
	End   time.Time
}

// Overlaps reports whether a and b share any instant.
func (a Interval) Overlaps(b Interval) bool {
	return a.Start.Before(b.End) && b.Start.Before(a.End)
}

// Slots returns the bookable slots in the window [from, to), in ascending
// order. A slot is returned only if it fits entirely inside the window,
// starts at or after now, and overlaps none of booked. Booked intervals
// may be any length and in any order. The result is never nil, and its
// times are in r.Location.
//
// Callers should check the window with ValidateWindow first; Slots does
// not limit its length.
func (r Rules) Slots(from, to, now time.Time, booked []Interval) []Interval {
	// Hide past slots with the same rule ValidateBooking uses.
	if now.After(from) {
		from = now
	}

	slots := []Interval{}
	// Walk calendar days in the business zone. AddDate keeps each day at
	// local midnight across DST changes, where adding 24h would drift.
	lastDay := r.at(to, ClockTime{})
	for day := r.at(from, ClockTime{}); !day.After(lastDay); day = day.AddDate(0, 0, 1) {
		if !slices.Contains(r.Workdays, day.Weekday()) {
			continue
		}
		// Stepping by SlotLength within one day is safe: US DST changes
		// happen at 02:00 on a Sunday, never inside business hours.
		closing := r.at(day, r.Close)
		for start := r.at(day, r.Open); !start.Add(r.SlotLength).After(closing); start = start.Add(r.SlotLength) {
			slot := Interval{Start: start, End: start.Add(r.SlotLength)}
			if slot.Start.Before(from) || slot.End.After(to) {
				continue
			}
			if overlapsAny(slot, booked) {
				continue
			}
			slots = append(slots, slot)
		}
	}
	return slots
}

// overlapsAny reports whether slot overlaps any of booked. A nested loop
// is enough: a window holds at most 414 slots against a few bookings.
func overlapsAny(slot Interval, booked []Interval) bool {
	for _, b := range booked {
		if slot.Overlaps(b) {
			return true
		}
	}
	return false
}
