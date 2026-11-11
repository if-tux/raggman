package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// db opens a direct connection for arranging states the API cannot reach yet
// (Failed needs Ingestion) and for counting queued Jobs, which have no API.
func db(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func ingestionJobs(t *testing.T, pool *pgxpool.Pool, documentID string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM job_queue WHERE target_id = $1 AND job_type = 'INGESTION'", documentID).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func remove(t *testing.T, server *httptest.Server, id string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/documents/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /documents/%s: %v", id, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestSubmittingEnqueuesOneIngestion(t *testing.T) {
	server := newServer(t)

	resp := submitPDF(t, server, "a.pdf", uniquePDF(t))
	wantStatus(t, resp, http.StatusCreated)
	doc := decode[documentJSON](t, resp.Body)

	if n := ingestionJobs(t, db(t), doc.ID); n != 1 {
		t.Fatalf("ingestion jobs = %d, want 1", n)
	}
}

func TestIdenticalContentReturnsExistingDocument(t *testing.T) {
	server := newServer(t)
	content := uniquePDF(t)

	first := decode[documentJSON](t, submitPDF(t, server, "original.pdf", content).Body)

	resp := submitPDF(t, server, "renamed.pdf", content)
	wantStatus(t, resp, http.StatusOK)
	second := decode[documentJSON](t, resp.Body)

	if second.ID != first.ID || second.Filename != "original.pdf" {
		t.Fatalf("second submission = %+v, want existing Document %s named original.pdf", second, first.ID)
	}
	if n := ingestionJobs(t, db(t), first.ID); n != 1 {
		t.Fatalf("ingestion jobs = %d, want 1 (resubmitting must not enqueue)", n)
	}
}

func TestResubmittingFailedDocumentRestartsIngestion(t *testing.T) {
	server := newServer(t)
	pool := db(t)
	content := uniquePDF(t)

	doc := decode[documentJSON](t, submitPDF(t, server, "a.pdf", content).Body)
	err := sqlc.New(pool).UpdateDocumentStatus(context.Background(), sqlc.UpdateDocumentStatusParams{
		ID:            uuid.MustParse(doc.ID),
		Status:        "FAILED",
		FailureReason: pgtype.Text{String: "embedding timed out", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := get(t, server.URL+"/documents/"+doc.ID)
	wantStatus(t, resp, http.StatusOK)
	if failed := decode[documentJSON](t, resp.Body); failed.Status != "FAILED" || failed.FailureReason != "embedding timed out" {
		t.Fatalf("GET failed Document = %+v, want FAILED with its reason", failed)
	}

	resp = submitPDF(t, server, "a.pdf", content)
	wantStatus(t, resp, http.StatusOK)
	got := decode[documentJSON](t, resp.Body)

	if got.Status != "PENDING" || got.FailureReason != "" {
		t.Fatalf("resubmitted = %+v, want PENDING without failure reason", got)
	}
	if n := ingestionJobs(t, pool, doc.ID); n != 2 {
		t.Fatalf("ingestion jobs = %d, want 2", n)
	}
}

func TestRejectsNonPDFEvenWithPDFExtension(t *testing.T) {
	server := newServer(t)

	resp := submitPDF(t, server, "fake.pdf", []byte("PK\x03\x04 actually a zip"))
	wantStatus(t, resp, http.StatusUnsupportedMediaType)
}

func TestRejectsDocumentsOver50MB(t *testing.T) {
	server := newServer(t)
	content := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 50<<20)...)

	resp := submitPDF(t, server, "huge.pdf", content)
	wantStatus(t, resp, http.StatusRequestEntityTooLarge)
}

func TestListIncludesSubmittedDocuments(t *testing.T) {
	server := newServer(t)
	doc := decode[documentJSON](t, submitPDF(t, server, "listed.pdf", uniquePDF(t)).Body)

	resp := get(t, server.URL+"/documents")
	wantStatus(t, resp, http.StatusOK)

	for _, d := range decode[[]documentJSON](t, resp.Body) {
		if d == doc {
			return
		}
	}
	t.Fatalf("list does not contain %+v", doc)
}

func TestRemovedDocumentIsGoneAndCanBeSubmittedAgain(t *testing.T) {
	server := newServer(t)
	content := uniquePDF(t)
	doc := decode[documentJSON](t, submitPDF(t, server, "a.pdf", content).Body)
	pool := db(t)
	// Chunks have no API until Ingestion exists; insert one directly.
	_, err := pool.Exec(context.Background(),
		`INSERT INTO chunks (id, document_id, page_number, content, embedding)
		 VALUES (gen_random_uuid(), $1, 1, 'text', array_fill(0.1, ARRAY[1024])::vector)`, doc.ID)
	if err != nil {
		t.Fatal(err)
	}

	wantStatus(t, remove(t, server, doc.ID), http.StatusNoContent)

	var chunks int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM chunks WHERE document_id = $1", doc.ID).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 {
		t.Fatalf("chunks after removal = %d, want 0", chunks)
	}
	wantStatus(t, get(t, server.URL+"/documents/"+doc.ID), http.StatusNotFound)
	wantStatus(t, remove(t, server, doc.ID), http.StatusNotFound)

	resp := submitPDF(t, server, "a.pdf", content)
	wantStatus(t, resp, http.StatusCreated)
	if again := decode[documentJSON](t, resp.Body); again.ID == doc.ID {
		t.Fatalf("resubmission after removal reused id %s, want a new Document", doc.ID)
	}
}

func TestUnknownDocumentIsNotFound(t *testing.T) {
	server := newServer(t)

	wantStatus(t, get(t, server.URL+"/documents/"+uuid.NewString()), http.StatusNotFound)
	wantStatus(t, get(t, server.URL+"/documents/not-a-uuid"), http.StatusNotFound)
}

func TestRejectsSubmissionWithoutFilename(t *testing.T) {
	server := newServer(t)

	resp := submitPDF(t, server, "", uniquePDF(t))
	wantStatus(t, resp, http.StatusBadRequest)
}
