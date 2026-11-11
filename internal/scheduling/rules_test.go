package scheduling

import (
	"errors"
	"slices"
	"testing"
	"time"
	// Embed the tz database, so these tests do not depend on the host's
	// /usr/share/zoneinfo, just as the binary does not.
	_ "time/tzdata"
)

// testNow is the fixed clock for every test that is not about the past.
// The DST weeks below are in 2026 and already past, so the real clock
// would reject them as in_past.
var testNow = mustTime("2026-01-01T00:00:00Z")

// mustTime parses an RFC 3339 timestamp or panics. Test inputs only.
func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func testRules(t *testing.T) Rules {
	t.Helper()
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

func TestValidateBooking(t *testing.T) {
	// Monday 2026-11-16 is in PST (-08:00) unless a case says otherwise.
	tests := []struct {
		name      string
		start     string
		end       string
		wantCode  string // empty means the booking is accepted
		wantField string
	}{
		// Business hours, from the first start to the last.
		{"08:00 is the first start", "2026-11-16T08:00:00-08:00", "2026-11-16T08:30:00-08:00", "", ""},
		{"07:30 is before opening", "2026-11-16T07:30:00-08:00", "2026-11-16T08:00:00-08:00", CodeOutsideBusinessHours, "starts_at"},
		{"16:30 is the last start", "2026-11-16T16:30:00-08:00", "2026-11-16T17:00:00-08:00", "", ""},
		{"17:00 ends after closing", "2026-11-16T17:00:00-08:00", "2026-11-16T17:30:00-08:00", CodeOutsideBusinessHours, "starts_at"},

		// The :00/:30 grid, checked on the Pacific wall clock.
		{"16:45 is off the grid", "2026-11-16T16:45:00-08:00", "2026-11-16T17:15:00-08:00", CodeOffGrid, "starts_at"},
		{":15 is off the grid", "2026-11-16T09:15:00-08:00", "2026-11-16T09:45:00-08:00", CodeOffGrid, "starts_at"},
		{"whole seconds are off the grid", "2026-11-16T09:00:30-08:00", "2026-11-16T09:30:30-08:00", CodeOffGrid, "starts_at"},
		{"fractional seconds are off the grid", "2026-11-16T09:00:00.5-08:00", "2026-11-16T09:30:00.5-08:00", CodeOffGrid, "starts_at"},

		// Business days.
		{"Saturday", "2026-11-21T09:00:00-08:00", "2026-11-21T09:30:00-08:00", CodeNotBusinessDay, "starts_at"},
		{"Sunday", "2026-11-22T09:00:00-08:00", "2026-11-22T09:30:00-08:00", CodeNotBusinessDay, "starts_at"},

		// Duration must be exactly 30 minutes.
		{"0 minutes", "2026-11-16T09:00:00-08:00", "2026-11-16T09:00:00-08:00", CodeInvalidDuration, "ends_at"},
		{"29 minutes", "2026-11-16T09:00:00-08:00", "2026-11-16T09:29:00-08:00", CodeInvalidDuration, "ends_at"},
		{"31 minutes", "2026-11-16T09:00:00-08:00", "2026-11-16T09:31:00-08:00", CodeInvalidDuration, "ends_at"},
		{"60 minutes", "2026-11-16T09:00:00-08:00", "2026-11-16T10:00:00-08:00", CodeInvalidDuration, "ends_at"},
		{"negative duration", "2026-11-16T09:00:00-08:00", "2026-11-16T08:30:00-08:00", CodeInvalidDuration, "ends_at"},

		// Any offset is accepted; the rules apply to the Pacific wall clock.
		{"Z is 09:00 PST", "2026-11-16T17:00:00Z", "2026-11-16T17:30:00Z", "", ""},
		{"+05:45 on the hour there is 08:45 PST", "2026-11-16T22:30:00+05:45", "2026-11-16T23:00:00+05:45", CodeOffGrid, "starts_at"},
		{"+05:45 at :45 there is 09:00 PST", "2026-11-16T22:45:00+05:45", "2026-11-16T23:15:00+05:45", "", ""},
		{"-07:00 in winter is 08:00 PST", "2026-11-16T09:00:00-07:00", "2026-11-16T09:30:00-07:00", "", ""},
		{"-07:00 in winter is 07:30 PST", "2026-11-16T08:30:00-07:00", "2026-11-16T09:00:00-07:00", CodeOutsideBusinessHours, "starts_at"},

		// The spring-forward week: DST starts Sunday 2026-03-08.
		{"Monday after spring forward, 08:00 PDT", "2026-03-09T08:00:00-07:00", "2026-03-09T08:30:00-07:00", "", ""},
		{"Monday after spring forward, 08:00-08:00 is 09:00 PDT", "2026-03-09T08:00:00-08:00", "2026-03-09T08:30:00-08:00", "", ""},
		{"Monday after spring forward, 16:30-08:00 is 17:30 PDT", "2026-03-09T16:30:00-08:00", "2026-03-09T17:00:00-08:00", CodeOutsideBusinessHours, "starts_at"},
		{"Friday before spring forward, 16:30 PST is Saturday in UTC", "2026-03-06T16:30:00-08:00", "2026-03-06T17:00:00-08:00", "", ""},
		{"the spring-forward Sunday itself", "2026-03-08T09:00:00-07:00", "2026-03-08T09:30:00-07:00", CodeNotBusinessDay, "starts_at"},

		// The first broken rule wins, in the order of PLAN.md section 3.
		{"Saturday for 60 minutes reports duration first", "2026-11-21T09:00:00-08:00", "2026-11-21T10:00:00-08:00", CodeInvalidDuration, "ends_at"},
		{"Saturday at 07:30 reports the day before the hours", "2026-11-21T07:30:00-08:00", "2026-11-21T08:00:00-08:00", CodeNotBusinessDay, "starts_at"},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rules.ValidateBooking(mustTime(tt.start), mustTime(tt.end), testNow)
			checkValidationError(t, err, tt.wantCode, tt.wantField)
		})
	}
}

func TestValidateBookingInPast(t *testing.T) {
	now := mustTime("2026-11-16T10:10:00-08:00")
	tests := []struct {
		name     string
		start    string
		end      string
		wantCode string
	}{
		{"earlier today", "2026-11-16T09:30:00-08:00", "2026-11-16T10:00:00-08:00", CodeInPast},
		{"the slot already under way", "2026-11-16T10:00:00-08:00", "2026-11-16T10:30:00-08:00", CodeInPast},
		{"the next slot today", "2026-11-16T10:30:00-08:00", "2026-11-16T11:00:00-08:00", ""},
		{"a seed record from 2019", "2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00", CodeInPast},
		{"a past Saturday breaks a rule before the clock", "2026-11-14T09:00:00-08:00", "2026-11-14T09:30:00-08:00", CodeNotBusinessDay},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rules.ValidateBooking(mustTime(tt.start), mustTime(tt.end), now)
			wantField := ""
			if tt.wantCode != "" {
				wantField = "starts_at"
			}
			checkValidationError(t, err, tt.wantCode, wantField)
		})
	}

	t.Run("a start equal to now is accepted", func(t *testing.T) {
		start := mustTime("2026-11-16T10:00:00-08:00")
		if err := rules.ValidateBooking(start, start.Add(30*time.Minute), start); err != nil {
			t.Errorf("ValidateBooking = %v, want nil", err)
		}
	})
}

func TestViolationsReportsEveryBrokenRule(t *testing.T) {
	tests := []struct {
		name      string
		start     string
		end       string
		wantCodes []string
	}{
		{"seed id 1, valid apart from being past", "2019-01-24T09:00:00-08:00", "2019-01-24T09:30:00-08:00", nil},
		{"seed id 9, Saturday for 60 minutes", "2019-01-26T13:00:00-08:00", "2019-01-26T14:00:00-08:00",
			[]string{CodeInvalidDuration, CodeNotBusinessDay}},
		{"every rule at once", "2026-11-21T07:15:30-08:00", "2026-11-21T08:15:30-08:00",
			[]string{CodeInvalidDuration, CodeOffGrid, CodeNotBusinessDay, CodeOutsideBusinessHours}},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var codes []string
			for _, v := range rules.Violations(mustTime(tt.start), mustTime(tt.end)) {
				codes = append(codes, v.Code)
			}
			if !slices.Equal(codes, tt.wantCodes) {
				t.Errorf("codes = %v, want %v", codes, tt.wantCodes)
			}
		})
	}
}

func TestValidateWindow(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		to       string
		wantCode string
	}{
		{"one minute", "2026-11-16T08:00:00-08:00", "2026-11-16T08:01:00-08:00", ""},
		{"empty", "2026-11-16T08:00:00-08:00", "2026-11-16T08:00:00-08:00", CodeInvalidRange},
		{"backwards", "2026-11-16T09:00:00-08:00", "2026-11-16T08:00:00-08:00", CodeInvalidRange},
		{"exactly 31 days", "2026-11-02T00:00:00-08:00", "2026-12-03T00:00:00-08:00", ""},
		{"31 days and a second", "2026-11-02T00:00:00-08:00", "2026-12-03T00:00:01-08:00", CodeRangeTooLarge},
		{"60 days", "2026-11-02T00:00:00-08:00", "2027-01-01T00:00:00-08:00", CodeRangeTooLarge},
		// 31 calendar days, not 31×24 hours: across fall back they are 745 hours.
		{"31 days across fall back", "2026-10-15T00:00:00-07:00", "2026-11-15T00:00:00-08:00", ""},
		{"31 days across spring forward", "2026-03-01T00:00:00-08:00", "2026-04-01T00:00:00-07:00", ""},
		{"31 days and a second across spring forward", "2026-03-01T00:00:00-08:00", "2026-04-01T00:00:01-07:00", CodeRangeTooLarge},
	}

	rules := testRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rules.ValidateWindow(mustTime(tt.from), mustTime(tt.to))
			checkValidationError(t, err, tt.wantCode, "")
		})
	}
}

// checkValidationError fails t unless err is nil when wantCode is empty,
// or a *ValidationError with wantCode and wantField otherwise.
func checkValidationError(t *testing.T, err error, wantCode, wantField string) {
	t.Helper()
	if wantCode == "" {
		if err != nil {
			t.Errorf("error = %v, want nil", err)
		}
		return
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want a *ValidationError with code %q", err, wantCode)
	}
	if verr.Code != wantCode || verr.Field != wantField {
		t.Errorf("code, field = %q, %q, want %q, %q (message %q)", verr.Code, verr.Field, wantCode, wantField, verr.Message)
	}
	if verr.Message == "" {
		t.Error("message is empty")
	}
}
