package seed_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dontfollowsean/future-scheduling-api/internal/postgres"
	"github.com/dontfollowsean/future-scheduling-api/internal/postgres/postgrestest"
	"github.com/dontfollowsean/future-scheduling-api/internal/scheduling"
	"github.com/dontfollowsean/future-scheduling-api/internal/seed"
)

// realFile is the seed file docker compose mounts.
const realFile = "../../appointments.json"

func TestRead(t *testing.T) {
	const valid = `{"id": 1, "trainer_id": 2, "user_id": 3,
		"started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T09:30:00-08:00"}`

	tests := []struct {
		name    string
		file    string
		wantErr string // empty means success
	}{
		{name: "one record", file: `[` + valid + `]`},
		{name: "empty array", file: `[]`},
		{name: "invalid JSON", file: `[{"id": 1,`, wantErr: "decode JSON"},
		{name: "not an array", file: valid, wantErr: "decode JSON"},
		{name: "data after the array", file: `[] []`, wantErr: "unexpected data after the array"},
		{
			name:    "unknown field",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "starts_at": "2019-01-24T09:00:00-08:00"}]`,
			wantErr: `unknown field "starts_at"`,
		},
		{
			name:    "fractional id",
			file:    `[{"id": 1.5, "trainer_id": 2, "user_id": 3}]`,
			wantErr: "decode JSON",
		},
		{
			name:    "timestamp without offset",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "started_at": "2019-01-24T09:00:00", "ended_at": "2019-01-24T09:30:00-08:00"}]`,
			wantErr: "decode JSON",
		},
		{
			name:    "unparseable timestamp",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "started_at": "Jan 24 2019 9am", "ended_at": "2019-01-24T09:30:00-08:00"}]`,
			wantErr: "decode JSON",
		},
		{
			name:    "missing id",
			file:    `[{"trainer_id": 2, "user_id": 3, "started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T09:30:00-08:00"}]`,
			wantErr: "id must be a positive integer",
		},
		{
			name:    "negative trainer_id",
			file:    `[{"id": 1, "trainer_id": -2, "user_id": 3, "started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T09:30:00-08:00"}]`,
			wantErr: "trainer_id must be a positive integer",
		},
		{
			name:    "zero user_id",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 0, "started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T09:30:00-08:00"}]`,
			wantErr: "user_id must be a positive integer",
		},
		{
			name:    "missing started_at",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "ended_at": "2019-01-24T09:30:00-08:00"}]`,
			wantErr: "started_at is required",
		},
		{
			name:    "missing ended_at",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "started_at": "2019-01-24T09:00:00-08:00"}]`,
			wantErr: "ended_at is required",
		},
		{
			name:    "ends when it starts",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T09:00:00-08:00"}]`,
			wantErr: "ended_at must be after started_at",
		},
		{
			name:    "ends before it starts",
			file:    `[{"id": 1, "trainer_id": 2, "user_id": 3, "started_at": "2019-01-24T09:30:00-08:00", "ended_at": "2019-01-24T09:00:00-08:00"}]`,
			wantErr: "ended_at must be after started_at",
		},
		{
			name:    "duplicate id",
			file:    `[` + valid + `,` + strings.Replace(valid, "09:", "11:", 2) + `]`,
			wantErr: "id 1 appears more than once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := seed.Read(strings.NewReader(tt.file))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Read() error = %v", err)
				}
				if got == nil {
					t.Error("Read() = nil, want a non-nil slice")
				}
				return
			}
			if err == nil {
				t.Fatalf("Read() = %+v, want an error containing %q", got, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Read() error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestReadMapsFields(t *testing.T) {
	got, err := seed.Read(strings.NewReader(`[{"id": 9, "trainer_id": 3, "user_id": 10,
		"started_at": "2019-01-26T13:00:00-08:00", "ended_at": "2019-01-26T14:00:00-08:00"}]`))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := scheduling.Appointment{
		ID:        9,
		TrainerID: 3,
		UserID:    10,
		StartsAt:  time.Date(2019, 1, 26, 21, 0, 0, 0, time.UTC),
		EndsAt:    time.Date(2019, 1, 26, 22, 0, 0, 0, time.UTC),
	}
	if len(got) != 1 {
		t.Fatalf("Read() returned %d appointments, want 1", len(got))
	}
	a := got[0]
	if a.ID != want.ID || a.TrainerID != want.TrainerID || a.UserID != want.UserID ||
		!a.StartsAt.Equal(want.StartsAt) || !a.EndsAt.Equal(want.EndsAt) {
		t.Errorf("Read() = %+v, want %+v", a, want)
	}
}

func TestRunRealFile(t *testing.T) {
	pool := postgrestest.Pool(t)
	store := postgres.NewStore(pool)
	var logs bytes.Buffer

	got, err := seed.Run(t.Context(), jsonLogger(&logs), store, defaultRules(t), realFile)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (seed.Result{Inserted: 10, Skipped: 0, Warnings: 5}); got != want {
		t.Errorf("Run() = %+v, want %+v", got, want)
	}
	if n := countRows(t, pool); n != 10 {
		t.Errorf("table has %d rows, want 10", n)
	}

	// One warning for each Saturday record, and id 9 also lasts 60 minutes.
	warnings := warningsByID(t, &logs)
	if ids := sortedKeys(warnings); !slices.Equal(ids, []int64{5, 7, 8, 9, 10}) {
		t.Errorf("warned about ids %v, want [5 7 8 9 10]", ids)
	}
	if reasons := strings.Split(warnings[9], "; "); len(reasons) != 2 {
		t.Errorf("id 9 reasons = %q, want 2 (duration and weekday)", warnings[9])
	}

	// A second run, as on every restart, inserts nothing.
	logs.Reset()
	got, err = seed.Run(t.Context(), jsonLogger(&logs), store, defaultRules(t), realFile)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if want := (seed.Result{Inserted: 0, Skipped: 10, Warnings: 5}); got != want {
		t.Errorf("second Run() = %+v, want %+v", got, want)
	}
	if n := countRows(t, pool); n != 10 {
		t.Errorf("after second run table has %d rows, want 10", n)
	}

	// The identity sequence was moved past the seed's explicit ids.
	next, err := store.CreateAppointment(t.Context(), scheduling.Appointment{
		TrainerID: 1,
		UserID:    42,
		StartsAt:  time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC),
		EndsAt:    time.Date(2026, 11, 16, 18, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateAppointment after seeding: %v", err)
	}
	if next.ID != 11 {
		t.Errorf("first booking after seeding got id %d, want 11", next.ID)
	}
}

func TestRunFailsWithoutWriting(t *testing.T) {
	tests := []struct {
		name      string
		file      string
		wantTaken bool // the error wraps scheduling.ErrSlotTaken
	}{
		{
			// id 2 starts inside id 1, for the same trainer.
			name: "overlap within the file",
			file: `[
				{"id": 1, "trainer_id": 1, "user_id": 1, "started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T10:00:00-08:00"},
				{"id": 2, "trainer_id": 1, "user_id": 2, "started_at": "2019-01-24T09:30:00-08:00", "ended_at": "2019-01-24T10:00:00-08:00"}
			]`,
			wantTaken: true,
		},
		{
			name: "corrupt record after a valid one",
			file: `[
				{"id": 1, "trainer_id": 1, "user_id": 1, "started_at": "2019-01-24T09:00:00-08:00", "ended_at": "2019-01-24T09:30:00-08:00"},
				{"id": 2, "trainer_id": 1, "user_id": 2, "started_at": "2019-01-24T10:00:00-08:00", "ended_at": "2019-01-24T09:30:00-08:00"}
			]`,
		},
		{
			name: "truncated file",
			file: `[{"id": 1, "trainer_id": 1, "user_id": 1, "started_at": "2019-01-24T09:00:00-08:00"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := postgrestest.Pool(t)
			path := writeFile(t, tt.file)

			_, err := seed.Run(t.Context(), discardLogger(), postgres.NewStore(pool), defaultRules(t), path)
			if err == nil {
				t.Fatal("Run() succeeded, want an error")
			}
			if got := errors.Is(err, scheduling.ErrSlotTaken); got != tt.wantTaken {
				t.Errorf("errors.Is(%v, ErrSlotTaken) = %v, want %v", err, got, tt.wantTaken)
			}
			if n := countRows(t, pool); n != 0 {
				t.Errorf("table has %d rows after a failed seed, want 0", n)
			}
		})
	}
}

func TestRunOverlapWithStoredRow(t *testing.T) {
	pool := postgrestest.Pool(t)
	store := postgres.NewStore(pool)
	if _, err := seed.Run(t.Context(), discardLogger(), store, defaultRules(t), realFile); err != nil {
		t.Fatalf("seed real file: %v", err)
	}

	// id 11 is new, but overlaps id 1 (trainer 1, Thursday 09:00 to 09:30).
	// id 12 is fine on its own and must not be written either.
	path := writeFile(t, `[
		{"id": 12, "trainer_id": 2, "user_id": 12, "started_at": "2019-01-25T09:00:00-08:00", "ended_at": "2019-01-25T09:30:00-08:00"},
		{"id": 11, "trainer_id": 1, "user_id": 11, "started_at": "2019-01-24T09:15:00-08:00", "ended_at": "2019-01-24T09:45:00-08:00"}
	]`)
	_, err := seed.Run(t.Context(), discardLogger(), store, defaultRules(t), path)
	if !errors.Is(err, scheduling.ErrSlotTaken) {
		t.Fatalf("Run() error = %v, want ErrSlotTaken", err)
	}
	if n := countRows(t, pool); n != 10 {
		t.Errorf("table has %d rows, want the original 10", n)
	}
}

func TestRunMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	_, err := seed.Run(t.Context(), discardLogger(), nil, defaultRules(t), missing)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Run() error = %v, want os.ErrNotExist", err)
	}
}

func defaultRules(t *testing.T) scheduling.Rules {
	t.Helper()
	rules, err := scheduling.DefaultRules()
	if err != nil {
		t.Fatalf("DefaultRules: %v", err)
	}
	return rules
}

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// warningsByID reads JSON log lines and returns the reasons logged for
// each seed record that breaks the booking rules, keyed by id.
func warningsByID(t *testing.T, logs *bytes.Buffer) map[int64]string {
	t.Helper()
	warnings := map[int64]string{}
	lines := bufio.NewScanner(logs)
	for lines.Scan() {
		var line struct {
			Level   string `json:"level"`
			Msg     string `json:"msg"`
			ID      int64  `json:"id"`
			Reasons string `json:"reasons"`
		}
		if err := json.Unmarshal(lines.Bytes(), &line); err != nil {
			t.Fatalf("parse log line %q: %v", lines.Text(), err)
		}
		if line.Level == "WARN" && line.Msg == "seed record breaks booking rules" {
			warnings[line.ID] = line.Reasons
		}
	}
	return warnings
}

func sortedKeys(m map[int64]string) []int64 {
	keys := []int64{}
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func countRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM appointments").Scan(&n); err != nil {
		t.Fatalf("count appointments: %v", err)
	}
	return n
}

// writeFile writes contents to a seed file in a temporary directory and
// returns its path.
func writeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "appointments.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	return path
}
