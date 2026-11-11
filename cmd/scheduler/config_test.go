package main

import (
	"log/slog"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	const dbURL = "postgres://scheduler:scheduler@localhost:5433/scheduler"

	tests := []struct {
		name    string
		env     map[string]string
		want    config
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"DATABASE_URL": dbURL},
			want: config{DatabaseURL: dbURL, HTTPAddr: ":8080", LogLevel: slog.LevelInfo},
		},
		{
			name: "everything set",
			env:  map[string]string{"DATABASE_URL": dbURL, "HTTP_ADDR": ":9090", "LOG_LEVEL": "debug", "SEED_FILE": "/data/appointments.json"},
			want: config{DatabaseURL: dbURL, HTTPAddr: ":9090", LogLevel: slog.LevelDebug, SeedFile: "/data/appointments.json"},
		},
		{
			name: "log level is case-insensitive",
			env:  map[string]string{"DATABASE_URL": dbURL, "LOG_LEVEL": "WARN"},
			want: config{DatabaseURL: dbURL, HTTPAddr: ":8080", LogLevel: slog.LevelWarn},
		},
		{
			name:    "missing DATABASE_URL",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": dbURL, "LOG_LEVEL": "verbose"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.env[key] }

			got, err := loadConfig(getenv)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("loadConfig() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("loadConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
