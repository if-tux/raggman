package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/ingestion"
)

// shutdownTimeout bounds how long in-flight requests get after SIGTERM.
const shutdownTimeout = 30 * time.Second

type App struct {
	Env       *environment.Environment
	Handler   http.Handler
	Ingestion *ingestion.Workers
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

	ctx, stopIngestion := context.WithCancel(ctx)
	defer stopIngestion()
	ingestionDone := make(chan struct{})
	go func() {
		app.Ingestion.Run(ctx)
		close(ingestionDone)
	}()

	// Requests get their own context so long Answer streams can be stopped
	// shortly before the shutdown deadline instead of being cut mid-write.
	requestsCtx, stopRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer stopRequests()
	server := &http.Server{
		Addr:              app.Env.HTTPAddr,
		Handler:           app.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestsCtx },
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "addr", server.Addr)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		// Let workers finish their transactions before cleanup closes the pool.
		stopIngestion()
		<-ingestionDone
		return err
	case <-ctx.Done():
	}

	slog.Info("Shutting down gracefully", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	stopStreams := time.AfterFunc(shutdownTimeout-3*time.Second, stopRequests)
	defer stopStreams.Stop()

	err = server.Shutdown(shutdownCtx)
	select {
	case <-ingestionDone:
	case <-shutdownCtx.Done():
		slog.Warn("Ingestion workers did not stop before the shutdown timeout")
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
