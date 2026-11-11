// Command scheduler serves the trainer video-call scheduling API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dontfollowsean/future-scheduling-api/internal/httpapi"
	"github.com/dontfollowsean/future-scheduling-api/internal/postgres"
)

const (
	// connectTimeout is how long startup keeps retrying the database,
	// which covers Postgres still booting under docker compose.
	connectTimeout = 30 * time.Second
	// shutdownTimeout stays well inside ECS's default 30s stopTimeout.
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("scheduler stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	// ctx is cancelled on SIGINT or SIGTERM, which starts a graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	// Log where we connect without ever logging the password.
	logger.Info("starting",
		"http_addr", cfg.HTTPAddr,
		"log_level", cfg.LogLevel.String(),
		"db_host", poolCfg.ConnConfig.Host,
		"db_port", poolCfg.ConnConfig.Port,
		"db_name", poolCfg.ConnConfig.Database,
		"db_user", poolCfg.ConnConfig.User,
	)

	pool, err := connect(ctx, logger, poolCfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Migrate once the database answers. In production this would be a
	// one-off ECS task before rollout, so replicas never race to migrate.
	version, err := postgres.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	logger.Info("database migrated", "schema_version", version)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewHandler(logger, pool),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.HTTPAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	// Restore default signal handling, so a second Ctrl-C exits at once.
	stop()

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// connect opens a pool and pings it, retrying with backoff until the
// database answers or connectTimeout passes.
func connect(ctx context.Context, logger *slog.Logger, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	// NewWithConfig does not connect; the first Ping does.
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	backoff := 250 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err := pool.Ping(ctx)
		if err == nil {
			logger.Info("connected to database", "attempts", attempt)
			return pool, nil
		}
		logger.Warn("database not ready, retrying", "attempt", attempt, "retry_in", backoff.String(), "error", err)

		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("connect to database after %d attempts: %w", attempt, err)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 4*time.Second)
	}
}
