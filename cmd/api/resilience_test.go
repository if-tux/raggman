package main

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

type jobState struct {
	LockedBy  *string
	Status    string
	DueInSecs float64
	Attempts  int32
}

// job reads the Document's latest Ingestion Job; Jobs have no API.
func job(t *testing.T, pool *pgxpool.Pool, documentID string) (jobState, bool) {
	t.Helper()
	var j jobState
	err := pool.QueryRow(context.Background(), `
		SELECT status, locked_by, attempts, EXTRACT(EPOCH FROM run_after - NOW())::float8
		FROM job_queue WHERE target_id = $1 ORDER BY created_at DESC LIMIT 1`, documentID).
		Scan(&j.Status, &j.LockedBy, &j.Attempts, &j.DueInSecs)
	if err != nil {
		return jobState{}, false
	}
	return j, true
}

func TestTransientFailureRetriesWithBackoffThenFails(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	startIngestion(t, app)

	content := makePDF(t, "embedding-outage "+rand.Text())
	doc := decode[documentJSON](t, submitPDF(t, server, "outage.pdf", content).Body)

	wantBackoffSecs := map[int32]float64{1: 10, 2: 40}
	for attempt := int32(1); attempt <= 2; attempt++ {
		var j jobState
		eventually(t, "a failed attempt to be re-queued", func() bool {
			var ok bool
			j, ok = job(t, pool, doc.ID)
			return ok && j.Status == "QUEUED" && j.Attempts == attempt
		})
		if want := wantBackoffSecs[attempt]; j.DueInSecs < want-3 || j.DueInSecs > want || j.LockedBy != nil {
			t.Fatalf("attempt %d re-queued as %+v, want unlocked and due in ~%.0fs", attempt, j, want)
		}
		if got := getDocument(t, server, doc.ID); got.Status != "INGESTING" {
			t.Fatalf("Document between attempts = %s, want INGESTING", got.Status)
		}
		// Skip the backoff wait.
		if _, err := pool.Exec(context.Background(), "UPDATE job_queue SET run_after = NOW() WHERE target_id = $1", doc.ID); err != nil {
			t.Fatal(err)
		}
	}

	failed := waitForStatus(t, server, doc.ID, "FAILED")
	if !strings.Contains(failed.FailureReason, "3 attempts") || !strings.Contains(failed.FailureReason, "model unavailable") {
		t.Fatalf("failure reason = %q, want it to mention 3 attempts and the cause", failed.FailureReason)
	}
	if j, _ := job(t, pool, doc.ID); j.Status != "FAILED" || j.Attempts != 3 {
		t.Fatalf("final Job = %+v, want FAILED after 3 attempts", j)
	}
}

func TestDocumentWithoutTextFailsWithoutRetry(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	startIngestion(t, app)

	doc := decode[documentJSON](t, submitPDF(t, server, "scan.pdf", makePDF(t, "", "")).Body)

	failed := waitForStatus(t, server, doc.ID, "FAILED")
	if failed.FailureReason != "no extractable text" {
		t.Fatalf("failure reason = %q, want %q", failed.FailureReason, "no extractable text")
	}
	if j, _ := job(t, pool, doc.ID); j.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry)", j.Attempts)
	}
}

func TestRemovingDocumentMidIngestionAbortsJob(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	startIngestion(t, app)

	doc := decode[documentJSON](t, submitPDF(t, server, "slow.pdf", makePDF(t, "slow-embedding "+rand.Text())).Body)
	waitForStatus(t, server, doc.ID, "INGESTING")

	wantStatus(t, remove(t, server, doc.ID), http.StatusNoContent)

	eventually(t, "the Job to be dropped", func() bool {
		_, exists := job(t, pool, doc.ID)
		return !exists
	})
}

func TestShutdownReleasesUnfinishedJob(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	app.Workers.ShutdownGrace = 0
	clearQueue(t)
	stop := startIngestion(t, app)

	doc := decode[documentJSON](t, submitPDF(t, server, "slow.pdf", makePDF(t, "slow-embedding "+rand.Text())).Body)
	waitForStatus(t, server, doc.ID, "INGESTING")

	stop()

	j, _ := job(t, pool, doc.ID)
	if j.Status != "QUEUED" || j.LockedBy != nil || j.Attempts != 0 || j.DueInSecs > 1 {
		t.Fatalf("Job after shutdown = %+v, want QUEUED, unlocked, no attempt counted, due now", j)
	}
}

func TestStaleJobIsReclaimed(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)

	doc := decode[documentJSON](t, submitPDF(t, server, "stale.pdf", makePDF(t, "reclaimed "+rand.Text())).Body)
	// A worker that died mid-Ingestion: RUNNING with a heartbeat long gone.
	_, err := pool.Exec(context.Background(), `
		UPDATE job_queue SET status = 'RUNNING', locked_by = 'dead-worker', heartbeat_at = NOW() - INTERVAL '10 minutes'
		WHERE target_id = $1`, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), "UPDATE documents SET status = 'INGESTING' WHERE id = $1", doc.ID); err != nil {
		t.Fatal(err)
	}

	startIngestion(t, app)

	waitForStatus(t, server, doc.ID, "READY")
}

func TestShutdownLetsJobFinishWithinGrace(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	stop := startIngestion(t, app) // default grace outlasts the stub's 3s delay

	doc := decode[documentJSON](t, submitPDF(t, server, "slow.pdf", makePDF(t, "slow-embedding "+rand.Text())).Body)
	waitForStatus(t, server, doc.ID, "INGESTING")

	stop()

	if got := getDocument(t, server, doc.ID); got.Status != "READY" {
		t.Fatalf("Document after shutdown = %s, want READY (Job finished within grace)", got.Status)
	}
	if _, exists := job(t, pool, doc.ID); exists {
		t.Fatal("Job still queued after finishing within grace")
	}
}
