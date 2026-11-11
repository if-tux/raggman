package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// startPostgres runs a throwaway pgvector Postgres and points DATABASE_URL at it.
func startPostgres(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "pgvector/pgvector:pg16",
		tcpostgres.WithDatabase("raggman_test"),
		tcpostgres.WithUsername("raggman"),
		tcpostgres.WithPassword("raggman"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	t.Setenv("DATABASE_URL", url)
}

func TestAppBootsAndReportsHealthy(t *testing.T) {
	startPostgres(t)

	app, cleanup, err := InitializeApp(context.Background())
	if err != nil {
		t.Fatalf("initialize app: %v", err)
	}
	t.Cleanup(cleanup)

	server := httptest.NewServer(app.Handler)
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}
}
