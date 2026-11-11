package scheduling

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestSlotsOneBusinessDay(t *testing.T) {
	rules := testRules(t)
	slots := rules.Slots(mustTime("2026-11-16T00:00:00-08:00"), mustTime("2026-11-17T00:00:00-08:00"), testNow, nil)

	if len(slots) != 18 {
		t.Fatalf("got %d slots, want 18", len(slots))
	}
	if got := slots[0].Start.Format(time.RFC3339); got != "2026-11-16T08:00:00-08:00" {
		t.Errorf("first slot starts %s, want 2026-11-16T08:00:00-08:00", got)
	}
	if got := slots[17].Start.Format(time.RFC3339); got != "2026-11-16T16:30:00-08:00" {
		t.Errorf("last slot starts %s, want 2026-11-16T16:30:00-08:00", got)
	}
	for _, s := range slots {
		if s.End.Sub(s.Start) != 30*time.Minute {
			t.Errorf("slot %v lasts %v, want 30m", s.Start, s.End.Sub(s.Start))
		}
		if s.Start.Location() != rules.Location || s.End.Location() != rules.Location {
			t.Errorf("slot %v is not in %s", s.Start, rules.Location)
		}
	}
}

func TestSlotsWindows(t *testing.T) {
	// Monday 2026-11-16 to Friday 2026-11-20 is a plain PST week.
	tests := []struct {
		name      string
		from      string
		to        string
		now       string // empty means testNow
		wantCount int
		wantFirst string // Pacific wall clock of the first slot, when any
		wantLast  string
	}{
		{"Saturday and Sunday only", "2026-11-21T00:00:00-08:00", "2026-11-23T00:00:00-08:00", "", 0, "", ""},
		{"Friday to Monday skips the weekend", "2026-11-20T00:00:00-08:00", "2026-11-24T00:00:00-08:00", "", 36,
			"2026-11-20 08:00", "2026-11-23 16:30"},
		{"partial edges keep only whole slots", "2026-11-16T09:10:00-08:00", "2026-11-16T16:45:00-08:00", "", 14,
			"2026-11-16 09:30", "2026-11-16 16:00"},
		{"exactly one slot", "2026-11-16T08:00:00-08:00", "2026-11-16T08:30:00-08:00", "", 1,
			"2026-11-16 08:00", "2026-11-16 08:00"},
		{"a minute short of one slot", "2026-11-16T08:00:00-08:00", "2026-11-16T08:29:00-08:00", "", 0, "", ""},
		{"a slot straddling the start is dropped", "2026-11-16T08:15:00-08:00", "2026-11-16T09:00:00-08:00", "", 1,
			"2026-11-16 08:30", "2026-11-16 08:30"},
		{"window given in UTC", "2026-11-16T16:00:00Z", "2026-11-16T17:00:00Z", "", 2,
			"2026-11-16 08:00", "2026-11-16 08:30"},
		{"overnight between business days", "2026-11-16T17:00:00-08:00", "2026-11-17T08:00:00-08:00", "", 0, "", ""},
		{"empty window", "2026-11-16T09:00:00-08:00", "2026-11-16T09:00:00-08:00", "", 0, "", ""},
		{"backwards window", "2026-11-17T00:00:00-08:00", "2026-11-16T00:00:00-08:00", "", 0, "", ""},

		// Past slots are hidden using the same rule as ValidateBooking.
		{"now mid-morning hides earlier slots", "2026-11-16T00:00:00-08:00", "2026-11-17T00:00:00-08:00", "2026-11-16T10:10:00-08:00", 13,
			"2026-11-16 10:30", "2026-11-16 16:30"},
		{"a slot starting exactly now is kept", "2026-11-16T00:00:00-08:00", "2026-11-17T00:00:00-08:00", "2026-11-16T10:00:00-08:00", 14,
			"2026-11-16 10:00", "2026-11-16 16:30"},
		{"window entirely in the past", "2026-11-16T00:00:00-08:00", "2026-11-17T00:00:00-08:00", "2026-11-18T00:00:00-08:00", 0, "", ""},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := testNow
			if tt.now != "" {
				now = mustTime(tt.now)
			}
			slots := rules.Slots(mustTime(tt.from), mustTime(tt.to), now, nil)

			if slots == nil {
				t.Fatal("slots is nil, want a non-nil slice")
			}
			if len(slots) != tt.wantCount {
				t.Fatalf("got %d slots, want %d: %v", len(slots), tt.wantCount, wallClocks(slots))
			}
			if len(slots) == 0 {
				return
			}
			first, last := wallClock(slots[0].Start), wallClock(slots[len(slots)-1].Start)
			if first != tt.wantFirst || last != tt.wantLast {
				t.Errorf("first, last = %s, %s, want %s, %s", first, last, tt.wantFirst, tt.wantLast)
			}
		})
	}
}

func TestSlotsSpringForwardWeek(t *testing.T) {
	// DST starts Sunday 2026-03-08: Friday is PST, Monday is PDT.
	rules := testRules(t)
	slots := rules.Slots(mustTime("2026-03-06T00:00:00-08:00"), mustTime("2026-03-14T00:00:00-07:00"), testNow, nil)

	if len(slots) != 108 {
		t.Fatalf("got %d slots, want 108 (6 business days × 18)", len(slots))
	}
	byDay := slotsByDay(slots)
	checkUTC(t, "Friday's first slot", byDay["2026-03-06"][0].Start, "2026-03-06T16:00:00Z")
	monday := byDay["2026-03-09"]
	checkUTC(t, "Monday's first slot", monday[0].Start, "2026-03-09T15:00:00Z")
	checkUTC(t, "Monday's last slot", monday[len(monday)-1].Start, "2026-03-09T23:30:00Z")
	for _, weekend := range []string{"2026-03-07", "2026-03-08"} {
		if n := len(byDay[weekend]); n != 0 {
			t.Errorf("%s has %d slots, want 0", weekend, n)
		}
	}
	checkWallClocks(t, slots)
}

func TestSlotsFallBackWeek(t *testing.T) {
	// DST ends Sunday 2026-11-01: Friday is PDT, Monday is PST.
	rules := testRules(t)
	slots := rules.Slots(mustTime("2026-10-30T00:00:00-07:00"), mustTime("2026-11-03T00:00:00-08:00"), testNow, nil)

	if len(slots) != 36 {
		t.Fatalf("got %d slots, want 36 (Friday and Monday × 18)", len(slots))
	}
	byDay := slotsByDay(slots)
	checkUTC(t, "Friday's first slot", byDay["2026-10-30"][0].Start, "2026-10-30T15:00:00Z")
	checkUTC(t, "Monday's first slot", byDay["2026-11-02"][0].Start, "2026-11-02T16:00:00Z")
	checkWallClocks(t, slots)
}

func TestSlotsSubtractsBookings(t *testing.T) {
	// Every case is one business day, Monday 2026-11-16 (PST).
	tests := []struct {
		name        string
		booked      [][2]string
		wantMissing []string // Pacific start times removed from the 18
	}{
		{"no bookings", nil, nil},
		{"back-to-back bookings remove only their own slots",
			[][2]string{{"2026-11-16T10:00:00-08:00", "2026-11-16T10:30:00-08:00"}, {"2026-11-16T10:30:00-08:00", "2026-11-16T11:00:00-08:00"}},
			[]string{"10:00", "10:30"}},
		{"a 60-minute booking removes two slots",
			[][2]string{{"2026-11-16T13:00:00-08:00", "2026-11-16T14:00:00-08:00"}},
			[]string{"13:00", "13:30"}},
		{"an off-grid booking removes both slots it touches",
			[][2]string{{"2026-11-16T12:15:00-08:00", "2026-11-16T12:45:00-08:00"}},
			[]string{"12:00", "12:30"}},
		{"a booking written in UTC",
			[][2]string{{"2026-11-16T17:00:00Z", "2026-11-16T17:30:00Z"}},
			[]string{"09:00"}},
		{"bookings in any order",
			[][2]string{{"2026-11-16T16:30:00-08:00", "2026-11-16T17:00:00-08:00"}, {"2026-11-16T08:00:00-08:00", "2026-11-16T08:30:00-08:00"}},
			[]string{"08:00", "16:30"}},
		{"a booking ending at opening time removes nothing",
			[][2]string{{"2026-11-16T07:30:00-08:00", "2026-11-16T08:00:00-08:00"}},
			nil},
		{"a booking on another day removes nothing",
			[][2]string{{"2026-11-17T09:00:00-08:00", "2026-11-17T09:30:00-08:00"}},
			nil},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var booked []Interval
			for _, b := range tt.booked {
				booked = append(booked, Interval{Start: mustTime(b[0]), End: mustTime(b[1])})
			}
			slots := rules.Slots(mustTime("2026-11-16T00:00:00-08:00"), mustTime("2026-11-17T00:00:00-08:00"), testNow, booked)

			var missing []string
			got := wallClocks(slots)
			for _, want := range allStartsOn("2026-11-16") {
				if !slices.Contains(got, want) {
					missing = append(missing, want[len("2026-11-16 "):])
				}
			}
			if !slices.Equal(missing, tt.wantMissing) {
				t.Errorf("missing slots = %v, want %v", missing, tt.wantMissing)
			}
		})
	}
}

// TestSlotsSeedData uses the appointments in appointments.json, with the
// clock pinned to Monday 2019-01-21, as in PLAN.md section 5.
func TestSlotsSeedData(t *testing.T) {
	seed := map[int][][2]string{
		1: {
			{"2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00"},
			{"2019-01-24T10:00:00-08:00", "2019-01-24T10:30:00-08:00"},
			{"2019-01-25T10:00:00-08:00", "2019-01-25T10:30:00-08:00"},
			{"2019-01-25T10:30:00-08:00", "2019-01-25T11:00:00-08:00"},
			{"2019-01-26T10:00:00-08:00", "2019-01-26T10:30:00-08:00"},
		},
		2: {
			{"2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00"},
			{"2019-01-26T10:00:00-08:00", "2019-01-26T10:30:00-08:00"},
		},
		3: {
			{"2019-01-26T12:00:00-08:00", "2019-01-26T12:30:00-08:00"},
			{"2019-01-26T13:00:00-08:00", "2019-01-26T14:00:00-08:00"},
			{"2019-01-26T14:00:00-08:00", "2019-01-26T14:30:00-08:00"},
		},
	}
	tests := []struct {
		trainer   int
		wantCount int
	}{
		{1, 32}, // Thursday 09:00 and 10:00, Friday 10:00 and 10:30 are taken
		{2, 35}, // Thursday 09:00 is taken
		{3, 36}, // every appointment is on Saturday
	}

	rules := testRules(t)
	now := mustTime("2019-01-21T00:00:00-08:00")
	for _, tt := range tests {
		var booked []Interval
		for _, b := range seed[tt.trainer] {
			booked = append(booked, Interval{Start: mustTime(b[0]), End: mustTime(b[1])})
		}
		slots := rules.Slots(mustTime("2019-01-24T00:00:00-08:00"), mustTime("2019-01-27T00:00:00-08:00"), now, booked)
		if len(slots) != tt.wantCount {
			t.Errorf("trainer %d: got %d slots, want %d", tt.trainer, len(slots), tt.wantCount)
		}
	}
}

// TestEverySlotPassesValidateBooking checks that the generator and the
// validator agree: anything listed as available can be booked.
func TestEverySlotPassesValidateBooking(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
		now  time.Time
	}{
		{"spring-forward week", "2026-03-06T00:00:00-08:00", "2026-03-14T00:00:00-07:00", testNow},
		{"fall-back week", "2026-10-30T00:00:00-07:00", "2026-11-03T00:00:00-08:00", testNow},
		{"31 days across fall back", "2026-10-15T00:00:00-07:00", "2026-11-15T00:00:00-08:00", testNow},
		{"spring-forward week with now on Monday", "2026-03-06T00:00:00-08:00", "2026-03-14T00:00:00-07:00",
			mustTime("2026-03-09T12:10:00-07:00")},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slots := rules.Slots(mustTime(tt.from), mustTime(tt.to), tt.now, nil)
			if len(slots) == 0 {
				t.Fatal("got no slots")
			}
			for i, s := range slots {
				if err := rules.ValidateBooking(s.Start, s.End, tt.now); err != nil {
					t.Errorf("slot %s: ValidateBooking = %v", s.Start.Format(time.RFC3339), err)
				}
				if i > 0 && s.Start.Before(slots[i-1].End) {
					t.Errorf("slot %s starts before the previous slot ends", s.Start.Format(time.RFC3339))
				}
			}
		})
	}
}

func TestIntervalOverlaps(t *testing.T) {
	ten := mustTime("2026-11-16T10:00:00-08:00")
	at := func(minutes int) time.Time { return ten.Add(time.Duration(minutes) * time.Minute) }
	tests := []struct {
		name string
		a, b Interval
		want bool
	}{
		{"identical", Interval{at(0), at(30)}, Interval{at(0), at(30)}, true},
		{"touching end to start", Interval{at(0), at(30)}, Interval{at(30), at(60)}, false},
		{"touching start to end", Interval{at(30), at(60)}, Interval{at(0), at(30)}, false},
		{"partly overlapping", Interval{at(0), at(30)}, Interval{at(15), at(45)}, true},
		{"one inside the other", Interval{at(0), at(60)}, Interval{at(15), at(45)}, true},
		{"apart", Interval{at(0), at(30)}, Interval{at(60), at(90)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Overlaps(tt.b); got != tt.want {
				t.Errorf("Overlaps = %v, want %v", got, tt.want)
			}
		})
	}
}

// wallClock renders t as a Pacific date and wall-clock time, such as
// "2026-11-16 08:00". Slots are already in the business zone.
func wallClock(t time.Time) string {
	return t.Format("2006-01-02 15:04")
}

func wallClocks(slots []Interval) []string {
	var out []string
	for _, s := range slots {
		out = append(out, wallClock(s.Start))
	}
	return out
}

// allStartsOn lists the 18 slot starts of a business day, from 08:00 to 16:30.
func allStartsOn(date string) []string {
	var out []string
	for h := 8; h < 17; h++ {
		out = append(out, fmt.Sprintf("%s %02d:00", date, h), fmt.Sprintf("%s %02d:30", date, h))
	}
	return out
}

// slotsByDay groups slots by their Pacific calendar date, never by UTC date.
func slotsByDay(slots []Interval) map[string][]Interval {
	byDay := map[string][]Interval{}
	for _, s := range slots {
		day := s.Start.Format(time.DateOnly)
		byDay[day] = append(byDay[day], s)
	}
	return byDay
}

func checkUTC(t *testing.T, what string, got time.Time, want string) {
	t.Helper()
	if s := got.UTC().Format(time.RFC3339); s != want {
		t.Errorf("%s = %s, want %s", what, s, want)
	}
}

// checkWallClocks fails t if any slot starts outside 08:00-16:30 Pacific.
func checkWallClocks(t *testing.T, slots []Interval) {
	t.Helper()
	for _, s := range slots {
		if clock := s.Start.Format("15:04"); clock < "08:00" || clock > "16:30" {
			t.Errorf("slot %s starts at %s Pacific, want 08:00 to 16:30", s.Start.Format(time.RFC3339), clock)
		}
	}
}
