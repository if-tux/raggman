package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func startIngestion(t *testing.T, app *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		app.Ingestion.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitForStatus(t *testing.T, server *httptest.Server, id, want string) documentJSON {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp := get(t, server.URL+"/documents/"+id)
		wantStatus(t, resp, http.StatusOK)
		doc := decode[documentJSON](t, resp.Body)
		if doc.Status == want {
			return doc
		}
		if time.Now().After(deadline) {
			t.Fatalf("Document %s still %s after 30s, want %s", id, doc.Status, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// testdata/three-pages.pdf: Page 1 holds one sentence, Page 2 about 3200
// characters (two Chunks), Page 3 is blank.
func TestIngestionMakesDocumentReadyWithChunksPerPage(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	// Earlier tests leave Jobs for fake PDFs; drop them so workers only see ours.
	if _, err := pool.Exec(context.Background(), "DELETE FROM job_queue"); err != nil {
		t.Fatal(err)
	}
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
