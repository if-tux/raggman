package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

// clearQueue drops Jobs left by earlier tests so workers only see this test's.
func clearQueue(t *testing.T) {
	t.Helper()
	if _, err := db(t).Exec(context.Background(), "DELETE FROM job_queue"); err != nil {
		t.Fatal(err)
	}
}

// startIngestion runs the workers until the test ends or stop is called.
func startIngestion(t *testing.T, app *App) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		app.Ingestion.Run(ctx)
		close(done)
	}()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return stop
}

// eventually polls cond until it holds or 30s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func getDocument(t *testing.T, server *httptest.Server, id string) documentJSON {
	t.Helper()
	resp := get(t, server.URL+"/documents/"+id)
	wantStatus(t, resp, http.StatusOK)
	return decode[documentJSON](t, resp.Body)
}

func waitForStatus(t *testing.T, server *httptest.Server, id, want string) documentJSON {
	t.Helper()
	var doc documentJSON
	eventually(t, "Document "+id+" to be "+want, func() bool {
		doc = getDocument(t, server, id)
		return doc.Status == want
	})
	return doc
}

// testdata/three-pages.pdf: Page 1 holds one sentence, Page 2 about 3200
// characters (two Chunks), Page 3 is blank.
func TestIngestionMakesDocumentReadyWithChunksPerPage(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	startIngestion(t, app)

	content, err := os.ReadFile("testdata/three-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode[documentJSON](t, submitPDF(t, server, "three-pages.pdf", content).Body)

	waitForStatus(t, server, doc.ID, "READY")

	rows, err := pool.Query(context.Background(),
		"SELECT page_number, count(*) FROM chunks WHERE document_id = $1 GROUP BY page_number ORDER BY page_number", doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[int32]int{}
	for rows.Next() {
		var page int32
		var n int
		if err := rows.Scan(&page, &n); err != nil {
			t.Fatal(err)
		}
		got[page] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("chunks per page = %v, want map[1:1 2:2]", got)
	}

	if n := ingestionJobs(t, pool, doc.ID); n != 0 {
		t.Fatalf("ingestion jobs after READY = %d, want 0 (completed Jobs are removed)", n)
	}
}
