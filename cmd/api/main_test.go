package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestMain runs one throwaway pgvector Postgres for the whole package. Tests
// stay independent by uploading unique content instead of resetting the DB.
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "pgvector/pgvector:pg16",
		tcpostgres.WithDatabase("raggman_test"),
		tcpostgres.WithUsername("raggman"),
		tcpostgres.WithPassword("raggman"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		os.Exit(1)
	}

	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "connection string:", err)
		os.Exit(1)
	}
	os.Setenv("DATABASE_URL", url)

	llm := httptest.NewServer(http.HandlerFunc(stubLLM))
	os.Setenv("OLLAMA_HOST", llm.URL)
	os.Setenv("LLM_BASE_URL", llm.URL+"/v1")
	// Tests read spans from an in-memory exporter, not a Jaeger.
	os.Setenv("OTEL_TRACES_EXPORTER", "none")

	code := m.Run()
	llm.Close()
	_ = testcontainers.TerminateContainer(container)
	os.Exit(code)
}

// newApp boots the fully wired app and serves its HTTP surface. Ingestion
// workers are not started; tests that need them call startIngestion.
func newApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()

	app, cleanup, err := InitializeApp(context.Background())
	if err != nil {
		t.Fatalf("initialize app: %v", err)
	}
	t.Cleanup(cleanup)

	server := httptest.NewServer(app.Handler)
	t.Cleanup(server.Close)
	return app, server
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	_, server := newApp(t)
	return server
}

// uniquePDF returns content that passes the PDF check and never repeats.
func uniquePDF(t *testing.T) []byte {
	t.Helper()
	return append([]byte("%PDF-1.7\n"), []byte(rand.Text())...)
}

type documentJSON struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
}

func submitPDF(t *testing.T, server *httptest.Server, filename string, content []byte) *http.Response {
	t.Helper()
	return submitFile(t, server, "/documents", filename, content)
}

// submitFile uploads content as the "file" field of a multipart request.
func submitFile(t *testing.T, server *httptest.Server, path, filename string, content []byte) *http.Response {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = form.Close()

	resp, err := http.Post(server.URL+path, form.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func del(t *testing.T, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, r io.Reader) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return v
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d, want %d (body: %s)", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, body)
	}
}

func TestAppBootsAndReportsHealthy(t *testing.T) {
	server := newServer(t)

	wantStatus(t, get(t, server.URL+"/healthz"), http.StatusOK)
}

func TestSubmittingCreatesPendingDocument(t *testing.T) {
	server := newServer(t)

	resp := submitPDF(t, server, "report.pdf", uniquePDF(t))
	wantStatus(t, resp, http.StatusCreated)
	created := decode[documentJSON](t, resp.Body)

	if created.Status != "PENDING" || created.Filename != "report.pdf" {
		t.Fatalf("created = %+v, want PENDING report.pdf", created)
	}

	resp = get(t, server.URL+"/documents/"+created.ID)
	wantStatus(t, resp, http.StatusOK)
	if got := decode[documentJSON](t, resp.Body); got != created {
		t.Fatalf("GET = %+v, want %+v", got, created)
	}
}
