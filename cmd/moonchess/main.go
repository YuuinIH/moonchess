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

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/gateway"
	statepkg "github.com/yuuinih/moonchess/internal/state"
	"github.com/yuuinih/moonchess/internal/worker"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "gateway" && os.Args[1] != "worker") {
		fmt.Fprintln(os.Stderr, "usage: moonchess gateway|worker")
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	plane, err := openControlPlane(ctx, requiredEnv("DATABASE_URL"))
	if err != nil {
		logger.Error("control plane unavailable", "error", err)
		os.Exit(1)
	}
	defer plane.Close()
	if err := plane.Migrate(ctx); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}
	store := statepkg.NewMooncakeHTTPStore(requiredEnv("MOONCAKE_URL"), &http.Client{Timeout: 5 * time.Second})

	if os.Args[1] == "gateway" {
		endpoints, err := gateway.ParseWorkerEndpoints(requiredEnv("WORKER_ENDPOINTS"))
		if err != nil {
			logger.Error("invalid worker endpoints", "error", err)
			os.Exit(1)
		}
		app := (&gateway.Gateway{Plane: plane, Store: store, WorkerEndpoints: endpoints}).Handler()
		serve(ctx, envOr("HTTP_ADDR", ":8080"), app, logger)
		return
	}

	runtime := worker.NewRuntime(requiredEnv("WORKER_ID"), plane, store, logger)
	runtime.LeaseTTL = durationEnv("LEASE_TTL", 3*time.Second)
	runtime.PollInterval = durationEnv("POLL_INTERVAL", 500*time.Millisecond)
	go func() {
		if err := runtime.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("worker runtime stopped", "error", err)
			cancel()
		}
	}()
	serve(ctx, envOr("HTTP_ADDR", ":8081"), runtime.Handler(), logger)
}

func openControlPlane(ctx context.Context, dsn string) (*control.Postgres, error) {
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		plane, err := control.OpenPostgres(ctx, dsn)
		if err == nil {
			return plane, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil, last
}

func serve(ctx context.Context, address string, handler http.Handler, logger *slog.Logger) {
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "address", address)
		done <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
		}
	}
}

func requiredEnv(name string) string {
	value := os.Getenv(name)
	if value == "" {
		fmt.Fprintf(os.Stderr, "%s is required\n", name)
		os.Exit(2)
	}
	return value
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s must be a Go duration: %v\n", name, err)
		os.Exit(2)
	}
	return parsed
}
