package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
)

// shutdownTimeout bounds how long in-flight requests get after SIGTERM.
const shutdownTimeout = 30 * time.Second

type App struct {
	Env     *environment.Environment
	Handler http.Handler
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("Raggman stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	app, cleanup, err := InitializeApp(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	server := &http.Server{
		Addr:              app.Env.HTTPAddr,
		Handler:           app.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "addr", server.Addr)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("Shutting down gracefully", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
