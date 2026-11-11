package httpapi

import (
	"testing"
	"time"
	// Embed the tz database, so these tests do not depend on the host's
	// /usr/share/zoneinfo, just as the binary does not.
	_ "time/tzdata"
)

func TestFormatTimeUsesBusinessZone(t *testing.T) {
	pacific := mustLoadLocation(t, "America/Los_Angeles")
	kathmandu := mustLoadLocation(t, "Asia/Kathmandu")

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"UTC in winter", time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC), "2026-11-16T09:00:00-08:00"},
		{"UTC in summer", time.Date(2026, 7, 6, 16, 0, 0, 0, time.UTC), "2026-07-06T09:00:00-07:00"},
		{"an unrelated zone", time.Date(2026, 11, 16, 22, 45, 0, 0, kathmandu), "2026-11-16T09:00:00-08:00"},
		{"time.Local, as pgx scans it", time.Date(2026, 11, 16, 17, 0, 0, 0, time.UTC).Local(), "2026-11-16T09:00:00-08:00"},
		{"Friday's last slot is Saturday in UTC", time.Date(2026, 3, 7, 0, 30, 0, 0, time.UTC), "2026-03-06T16:30:00-08:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatTime(tt.in, pacific); got != tt.want {
				t.Errorf("formatTime = %s, want %s", got, tt.want)
			}
		})
	}
}

func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
