package main

import (
	"errors"
	"fmt"
	"log/slog"
)

// config holds everything the process reads from its environment.
// Business rules (time zone, opening hours) are not configuration;
// they live in the scheduling package.
type config struct {
	DatabaseURL string
	HTTPAddr    string
	LogLevel    slog.Level
	// SeedFile is the appointments.json to load at startup. Empty skips
	// seeding, which is the production default.
	SeedFile string
}

// loadConfig reads the configuration through getenv, which is os.Getenv
// in production and a map lookup in tests.
func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		DatabaseURL: getenv("DATABASE_URL"),
		HTTPAddr:    getenv("HTTP_ADDR"),
		LogLevel:    slog.LevelInfo,
		SeedFile:    getenv("SEED_FILE"),
	}

	if cfg.DatabaseURL == "" {
		return config{}, errors.New("DATABASE_URL is required")
	}
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}
	if level := getenv("LOG_LEVEL"); level != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(level)); err != nil {
			return config{}, fmt.Errorf("LOG_LEVEL: want debug, info, warn or error, got %q", level)
		}
	}

	return cfg, nil
}
