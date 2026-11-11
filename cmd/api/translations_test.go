package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/BrunoPereiradosSantos/raggman/internal/epub/epubtest"
	"github.com/google/uuid"
)

type translationJSON struct {
	ID             string `json:"id"`
	BookID         string `json:"book_id"`
	TargetLanguage string `json:"target_language"`
	Level          string `json:"level"`
	Status         string `json:"status"`
	FailureReason  string `json:"failure_reason"`
}

type termJSON struct {
	Source      string `json:"source"`
	Rendering   string `json:"rendering"`
	Type        string `json:"type"`
	Occurrences int    `json:"occurrences"`
}

type termbaseJSON struct {
	TargetLanguage string     `json:"target_language"`
	Status         string     `json:"status"`
	Terms          []termJSON `json:"terms"`
}

// storyBook is a Book in two chapters naming Mara three times, Port Varen
// twice and the glimmerstone once. extra is added to the first chapter.
func storyBook(t *testing.T, extra string) []byte {
	t.Helper()
	return epubtest.Book("The Lighthouse Keeper", "en",
		`<h1>One</h1><p><em>Mara</em> lit the lamp above Port Varen.</p><p>`+extra+` `+rand.Text()+`</p>`,
		`<h1>Two</h1><p>Mara left Port Varen at dawn.</p><p>Mara kept the glimmerstone.</p>`)
}

func sendJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(method, url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func startTranslation(t *testing.T, server *httptest.Server, bookID, language, level string) *http.Response {
	t.Helper()
	return sendJSON(t, http.MethodPost, server.URL+"/books/"+bookID+"/translations",
		map[string]string{"target_language": language, "level": level})
}

// submitAndTranslate submits a Book and starts its Translation into pt-BR.
func submitAndTranslate(t *testing.T, server *httptest.Server, content []byte, level string) translationJSON {
	t.Helper()
	book := decode[bookJSON](t, submitBook(t, server, "story.epub", content).Body)
	resp := startTranslation(t, server, book.ID, "pt-BR", level)
	wantStatus(t, resp, http.StatusCreated)
	return decode[translationJSON](t, resp.Body)
}

func getTranslation(t *testing.T, server *httptest.Server, id string) translationJSON {
	t.Helper()
	resp := get(t, server.URL+"/translations/"+id)
	wantStatus(t, resp, http.StatusOK)
	return decode[translationJSON](t, resp.Body)
}

func waitForTranslation(t *testing.T, server *httptest.Server, id, want string) translationJSON {
	t.Helper()
	var tr translationJSON
	eventually(t, "Translation "+id+" to be "+want, func() bool {
		tr = getTranslation(t, server, id)
		return tr.Status == want
	})
	return tr
}

func getTermbase(t *testing.T, server *httptest.Server, translationID string) termbaseJSON {
	t.Helper()
	resp := get(t, server.URL+"/translations/"+translationID+"/termbase")
	wantStatus(t, resp, http.StatusOK)
	return decode[termbaseJSON](t, resp.Body)
}

func putTerms(t *testing.T, server *httptest.Server, translationID string, terms []termJSON) *http.Response {
	t.Helper()
	return sendJSON(t, http.MethodPut, server.URL+"/translations/"+translationID+"/termbase", map[string]any{"terms": terms})
}

func approve(t *testing.T, server *httptest.Server, translationID string) *http.Response {
	t.Helper()
	return sendJSON(t, http.MethodPost, server.URL+"/translations/"+translationID+"/termbase/approve", nil)
}

// queuedJobs counts the Jobs of a type queued for a target.
func queuedJobs(t *testing.T, jobType, targetID string) int {
	t.Helper()
	var n int
	err := db(t).QueryRow(context.Background(),
		"SELECT count(*) FROM job_queue WHERE target_id = $1 AND job_type = $2 AND status = 'QUEUED'", targetID, jobType).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func termbaseID(t *testing.T, translationID string) string {
	t.Helper()
	var id string
	if err := db(t).QueryRow(context.Background(), "SELECT termbase_id FROM translations WHERE id = $1", translationID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// reviewedTranslation takes a Translation of a fresh Book up to review.
func reviewedTranslation(t *testing.T, server *httptest.Server, app *App) translationJSON {
	t.Helper()
	clearQueue(t)
	startIngestion(t, app)
	tr := submitAndTranslate(t, server, storyBook(t, ""), "formal")
	return waitForTranslation(t, server, tr.ID, "IN_REVIEW")
}

var storyTerms = []termJSON{
	{Source: "Mara", Rendering: "Mara", Type: "name", Occurrences: 3},
	{Source: "Port Varen", Rendering: "Porto Varen", Type: "name", Occurrences: 2},
	{Source: "glimmerstone", Rendering: "pedra-lume", Type: "term", Occurrences: 1},
}

func TestStartingATranslationExtractsItsTermbaseForReview(t *testing.T) {
	app, server := newApp(t)
	clearQueue(t)
	book := decode[bookJSON](t, submitBook(t, server, "story.epub", storyBook(t, "")).Body)

	resp := startTranslation(t, server, book.ID, "pt-br", "formal")
	wantStatus(t, resp, http.StatusCreated)
	tr := decode[translationJSON](t, resp.Body)
	want := translationJSON{ID: tr.ID, BookID: book.ID, TargetLanguage: "pt-BR", Level: "formal", Status: "EXTRACTING"}
	if tr != want {
		t.Fatalf("started = %+v, want %+v", tr, want)
	}
	if tb := getTermbase(t, server, tr.ID); tb.Status != "EXTRACTING" || len(tb.Terms) != 0 {
		t.Fatalf("Termbase before extraction = %+v, want EXTRACTING without Terms", tb)
	}

	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "IN_REVIEW")

	// The stub also proposes "Zorblat", which the Book never mentions, and
	// renders Mara as "Mára".
	tb := getTermbase(t, server, tr.ID)
	if tb.Status != "IN_REVIEW" || tb.TargetLanguage != "pt-BR" || !slices.Equal(tb.Terms, storyTerms) {
		t.Fatalf("Termbase = %+v, want IN_REVIEW in pt-BR with %+v", tb, storyTerms)
	}
	if n := queuedJobs(t, "TERMBASE_EXTRACTION", termbaseID(t, tr.ID)); n != 0 {
		t.Fatalf("extraction Jobs left = %d, want 0", n)
	}
}

func TestBookListsItsTranslations(t *testing.T) {
	server := newServer(t)
	book := decode[bookJSON](t, submitBook(t, server, "story.epub", storyBook(t, "")).Body)
	first := decode[translationJSON](t, startTranslation(t, server, book.ID, "pt-BR", "formal").Body)
	second := decode[translationJSON](t, startTranslation(t, server, book.ID, "fr", "children").Body)

	type bookWithTranslations struct {
		ID           string            `json:"id"`
		Translations []translationJSON `json:"translations"`
	}
	resp := get(t, server.URL+"/books/"+book.ID)
	wantStatus(t, resp, http.StatusOK)
	if got := decode[bookWithTranslations](t, resp.Body); !slices.Equal(got.Translations, []translationJSON{first, second}) {
		t.Fatalf("GET /books/{id} translations = %+v, want %+v then %+v", got.Translations, first, second)
	}

	resp = get(t, server.URL+"/books")
	wantStatus(t, resp, http.StatusOK)
	for _, b := range decode[[]bookWithTranslations](t, resp.Body) {
		if b.ID == book.ID {
			if !slices.Equal(b.Translations, []translationJSON{first, second}) {
				t.Fatalf("GET /books translations = %+v", b.Translations)
			}
			return
		}
	}
	t.Fatal("GET /books does not list the Book")
}

func TestRejectsTranslationsThatCannotStart(t *testing.T) {
	server := newServer(t)
	book := decode[bookJSON](t, submitBook(t, server, "story.epub", storyBook(t, "")).Body)

	for name, c := range map[string]struct {
		bookID, language, level string
		want                    int
	}{
		"the Book's own language": {book.ID, "en", "formal", http.StatusBadRequest},
		"a variant of it":         {book.ID, "en-GB", "formal", http.StatusBadRequest},
		"not a language tag":      {book.ID, "brazilian", "formal", http.StatusBadRequest},
		"no language":             {book.ID, "", "formal", http.StatusBadRequest},
		"an unknown Level":        {book.ID, "pt-BR", "poetic", http.StatusBadRequest},
		"an unknown Book":         {uuid.NewString(), "pt-BR", "formal", http.StatusNotFound},
	} {
		if resp := startTranslation(t, server, c.bookID, c.language, c.level); resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d", name, resp.StatusCode, c.want)
		}
	}
	resp := get(t, server.URL+"/books/"+book.ID)
	if got := decode[struct {
		Translations []translationJSON `json:"translations"`
	}](t, resp.Body); len(got.Translations) != 0 {
		t.Fatalf("rejected requests left Translations behind: %+v", got.Translations)
	}
}

func TestUnknownTranslationIsNotFound(t *testing.T) {
	server := newServer(t)
	id := uuid.NewString()

	wantStatus(t, get(t, server.URL+"/translations/"+id), http.StatusNotFound)
	wantStatus(t, get(t, server.URL+"/translations/not-a-uuid"), http.StatusNotFound)
	wantStatus(t, get(t, server.URL+"/translations/"+id+"/termbase"), http.StatusNotFound)
	wantStatus(t, putTerms(t, server, id, []termJSON{}), http.StatusNotFound)
	wantStatus(t, approve(t, server, id), http.StatusNotFound)
}

func TestTermsCanBeEditedAddedAndRemovedInReview(t *testing.T) {
	app, server := newApp(t)
	tr := reviewedTranslation(t, server, app)

	// Port Varen is renamed, the glimmerstone dropped, the lamp added; the
	// occurrences sent are wrong on purpose.
	resp := putTerms(t, server, tr.ID, []termJSON{
		{Source: "lamp", Rendering: "lampião", Type: "term", Occurrences: 99},
		{Source: "Port Varen", Rendering: "Porto de Varen", Type: "name"},
		{Source: " Mara ", Rendering: "Mara", Type: "name"},
		{Source: "Tobias", Rendering: "Tobias", Type: "name"},
	})
	wantStatus(t, resp, http.StatusOK)
	want := []termJSON{
		{Source: "Mara", Rendering: "Mara", Type: "name", Occurrences: 3},
		{Source: "Port Varen", Rendering: "Porto de Varen", Type: "name", Occurrences: 2},
		{Source: "lamp", Rendering: "lampião", Type: "term", Occurrences: 1},
		{Source: "Tobias", Rendering: "Tobias", Type: "name", Occurrences: 0},
	}
	if got := decode[termbaseJSON](t, resp.Body); got.Status != "IN_REVIEW" || !slices.Equal(got.Terms, want) {
		t.Fatalf("PUT returned %+v, want IN_REVIEW with %+v", got, want)
	}
	if got := getTermbase(t, server, tr.ID); !slices.Equal(got.Terms, want) {
		t.Fatalf("GET after PUT = %+v, want %+v", got.Terms, want)
	}

	for name, terms := range map[string][]termJSON{
		"no rendering": {{Source: "Mara", Rendering: "", Type: "name"}},
		"unknown type": {{Source: "Mara", Rendering: "Mara", Type: "person"}},
		"listed twice": {{Source: "Mara", Rendering: "Mara", Type: "name"}, {Source: "Mara", Rendering: "M", Type: "name"}},
	} {
		if resp := putTerms(t, server, tr.ID, terms); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	wantStatus(t, sendJSON(t, http.MethodPut, server.URL+"/translations/"+tr.ID+"/termbase", map[string]string{"oops": "x"}), http.StatusBadRequest)
	if got := getTermbase(t, server, tr.ID); !slices.Equal(got.Terms, want) {
		t.Fatalf("rejected edits changed the Termbase: %+v", got.Terms)
	}
}

func TestApprovingLocksTheTermbaseAndStartsTranslating(t *testing.T) {
	app, server := newApp(t)
	tr := reviewedTranslation(t, server, app)

	resp := approve(t, server, tr.ID)
	wantStatus(t, resp, http.StatusOK)
	if got := decode[translationJSON](t, resp.Body); got.Status != "TRANSLATING" || got.ID != tr.ID {
		t.Fatalf("approved = %+v, want %s TRANSLATING", got, tr.ID)
	}
	if got := getTranslation(t, server, tr.ID); got.Status != "TRANSLATING" {
		t.Fatalf("Translation after approval = %s, want TRANSLATING", got.Status)
	}
	if n := queuedJobs(t, "TRANSLATION", tr.ID); n != 1 {
		t.Fatalf("translation Jobs = %d, want 1", n)
	}

	// A locked Termbase rejects edits and a second approval.
	if tb := getTermbase(t, server, tr.ID); tb.Status != "LOCKED" || !slices.Equal(tb.Terms, storyTerms) {
		t.Fatalf("Termbase after approval = %+v, want LOCKED with its Terms", tb)
	}
	wantStatus(t, putTerms(t, server, tr.ID, []termJSON{{Source: "Mara", Rendering: "Maria", Type: "name"}}), http.StatusConflict)
	wantStatus(t, approve(t, server, tr.ID), http.StatusConflict)
	if tb := getTermbase(t, server, tr.ID); !slices.Equal(tb.Terms, storyTerms) {
		t.Fatalf("locked Termbase changed: %+v", tb.Terms)
	}
	if n := queuedJobs(t, "TRANSLATION", tr.ID); n != 1 {
		t.Fatalf("translation Jobs after a second approval = %d, want 1", n)
	}
}

func TestTranslationThatIsNoLongerInReviewCannotChangeTheSharedTermbase(t *testing.T) {
	app, server := newApp(t)
	formal := reviewedTranslation(t, server, app)
	children := decode[translationJSON](t, startTranslation(t, server, formal.BookID, "pt-BR", "children").Body)

	// Cancelling has no API yet; the status is set directly.
	if _, err := db(t).Exec(context.Background(), "UPDATE translations SET status = 'CANCELLED' WHERE id = $1", formal.ID); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, putTerms(t, server, formal.ID, []termJSON{{Source: "Mara", Rendering: "Maria", Type: "name"}}), http.StatusConflict)
	wantStatus(t, approve(t, server, formal.ID), http.StatusConflict)
	if tb := getTermbase(t, server, children.ID); tb.Status != "IN_REVIEW" || !slices.Equal(tb.Terms, storyTerms) {
		t.Fatalf("shared Termbase = %+v, want it untouched and IN_REVIEW", tb)
	}
	if got := getTranslation(t, server, children.ID); got.Status != "IN_REVIEW" {
		t.Fatalf("the other Translation = %s, want IN_REVIEW", got.Status)
	}
}

func TestDatabaseRejectsUnknownStatesAndLevels(t *testing.T) {
	app, server := newApp(t)
	tr := reviewedTranslation(t, server, app)
	pool := db(t)

	for name, query := range map[string]string{
		"Translation status": "UPDATE translations SET status = 'PAUSED' WHERE id = $1",
		"Translation Level":  "UPDATE translations SET level = 'poetic' WHERE id = $1",
		"Termbase status":    "UPDATE termbases SET status = 'DRAFT' WHERE id = (SELECT termbase_id FROM translations WHERE id = $1)",
	} {
		if _, err := pool.Exec(context.Background(), query, tr.ID); err == nil || !strings.Contains(err.Error(), "check constraint") {
			t.Errorf("%s: err = %v, want a check constraint violation", name, err)
		}
	}
}

func TestTermbaseCannotBeEditedOrApprovedBeforeExtractionFinishes(t *testing.T) {
	server := newServer(t) // no workers: extraction never runs
	tr := submitAndTranslate(t, server, storyBook(t, ""), "formal")

	wantStatus(t, putTerms(t, server, tr.ID, []termJSON{{Source: "Mara", Rendering: "Mara", Type: "name"}}), http.StatusConflict)
	wantStatus(t, approve(t, server, tr.ID), http.StatusConflict)
	if got := getTranslation(t, server, tr.ID); got.Status != "EXTRACTING" {
		t.Fatalf("Translation = %s, want EXTRACTING", got.Status)
	}
}

func TestLaterTranslationsShareTheTermbase(t *testing.T) {
	app, server := newApp(t)
	formal := reviewedTranslation(t, server, app)

	// A second Level joins the review without a second extraction.
	resp := startTranslation(t, server, formal.BookID, "pt-BR", "children")
	wantStatus(t, resp, http.StatusCreated)
	children := decode[translationJSON](t, resp.Body)
	if children.Status != "IN_REVIEW" {
		t.Fatalf("second Translation = %s, want IN_REVIEW", children.Status)
	}
	if got := getTermbase(t, server, children.ID); !slices.Equal(got.Terms, storyTerms) {
		t.Fatalf("second Translation's Termbase = %+v, want the shared Terms", got.Terms)
	}

	// Approving through either starts both.
	wantStatus(t, approve(t, server, children.ID), http.StatusOK)
	for _, id := range []string{formal.ID, children.ID} {
		if got := getTranslation(t, server, id); got.Status != "TRANSLATING" {
			t.Fatalf("Translation %s after approval = %s, want TRANSLATING", id, got.Status)
		}
		if n := queuedJobs(t, "TRANSLATION", id); n != 1 {
			t.Fatalf("translation Jobs for %s = %d, want 1", id, n)
		}
	}

	// With the Termbase locked, a third starts translating at once.
	resp = startTranslation(t, server, formal.BookID, "pt-BR", "informal")
	wantStatus(t, resp, http.StatusCreated)
	informal := decode[translationJSON](t, resp.Body)
	if informal.Status != "TRANSLATING" || queuedJobs(t, "TRANSLATION", informal.ID) != 1 {
		t.Fatalf("third Translation = %+v with %d Jobs, want TRANSLATING with 1", informal, queuedJobs(t, "TRANSLATION", informal.ID))
	}
	var extractions int
	err := db(t).QueryRow(context.Background(), "SELECT count(*) FROM job_queue WHERE job_type = 'TERMBASE_EXTRACTION'").Scan(&extractions)
	if err != nil || extractions != 0 {
		t.Fatalf("extraction Jobs = %d (%v), want 0: the Termbase is reused", extractions, err)
	}

	// Another language gets a Termbase of its own.
	french := decode[translationJSON](t, startTranslation(t, server, formal.BookID, "fr", "formal").Body)
	if french.Status != "EXTRACTING" || termbaseID(t, french.ID) == termbaseID(t, formal.ID) {
		t.Fatalf("French Translation = %+v, want EXTRACTING on its own Termbase", french)
	}
}

func TestUnreadableModelReplyIsAskedAgain(t *testing.T) {
	app, server := newApp(t)
	clearQueue(t)
	startIngestion(t, app)

	// One chapter, so one Window: were it skipped after the first unreadable
	// reply, the extraction would have nothing and fail.
	content := epubtest.Book("T", "en", `<p>Mara unreadable-twice `+rand.Text()+`</p>`)
	tr := submitAndTranslate(t, server, content, "formal")
	waitForTranslation(t, server, tr.ID, "IN_REVIEW")

	want := []termJSON{{Source: "Mara", Rendering: "Mara", Type: "name", Occurrences: 1}}
	if got := getTermbase(t, server, tr.ID); !slices.Equal(got.Terms, want) {
		t.Fatalf("Termbase = %+v, want %+v", got.Terms, want)
	}
	if j, ok := job(t, db(t), termbaseID(t, tr.ID)); ok {
		t.Fatalf("extraction Job left behind: %+v, want it done in one attempt", j)
	}
}

func TestPartOfTheBookTheModelCannotAnswerForIsSkipped(t *testing.T) {
	app, server := newApp(t)
	clearQueue(t)
	startIngestion(t, app)

	// The first chapter never gets a readable reply; the second does.
	tr := submitAndTranslate(t, server, storyBook(t, "always-unreadable"), "formal")
	waitForTranslation(t, server, tr.ID, "IN_REVIEW")

	// Terms found in the second chapter are still counted over the whole Book.
	if got := getTermbase(t, server, tr.ID); !slices.Equal(got.Terms, storyTerms) {
		t.Fatalf("Termbase = %+v, want %+v", got.Terms, storyTerms)
	}
}

func TestExtractionFailsWhenTheModelNeverAnswersReadably(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	startIngestion(t, app)

	content := epubtest.Book("T", "en", `<p>Mara always-unreadable `+rand.Text()+`</p>`)
	tr := submitAndTranslate(t, server, content, "formal")
	target := termbaseID(t, tr.ID)

	for attempt := int32(1); attempt <= 2; attempt++ {
		eventually(t, "a failed extraction to be re-queued", func() bool {
			j, ok := job(t, pool, target)
			return ok && j.Status == "QUEUED" && j.Attempts == attempt
		})
		if got := getTranslation(t, server, tr.ID); got.Status != "EXTRACTING" {
			t.Fatalf("Translation between attempts = %s, want EXTRACTING", got.Status)
		}
		// Skip the backoff wait.
		if _, err := pool.Exec(context.Background(), "UPDATE job_queue SET run_after = NOW() WHERE target_id = $1", target); err != nil {
			t.Fatal(err)
		}
	}

	failed := waitForTranslation(t, server, tr.ID, "FAILED")
	if !strings.Contains(failed.FailureReason, "3 attempts") || !strings.Contains(failed.FailureReason, "no readable list of Terms") {
		t.Fatalf("failure reason = %q, want it to mention 3 attempts and the cause", failed.FailureReason)
	}
	if tb := getTermbase(t, server, tr.ID); tb.Status != "FAILED" {
		t.Fatalf("Termbase = %s, want FAILED", tb.Status)
	}
	wantStatus(t, approve(t, server, tr.ID), http.StatusConflict)

	// Asking again retries the extraction.
	again := decode[translationJSON](t, startTranslation(t, server, tr.BookID, "pt-BR", "formal").Body)
	if again.Status != "EXTRACTING" || queuedJobs(t, "TERMBASE_EXTRACTION", target) != 1 {
		t.Fatalf("retry = %+v with %d queued extractions, want EXTRACTING with 1", again, queuedJobs(t, "TERMBASE_EXTRACTION", target))
	}
}

func TestModelOutageDuringExtractionIsRetried(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	clearQueue(t)
	startIngestion(t, app)

	tr := submitAndTranslate(t, server, storyBook(t, "extraction-outage"), "formal")
	target := termbaseID(t, tr.ID)

	eventually(t, "the failed extraction to be re-queued", func() bool {
		j, ok := job(t, pool, target)
		return ok && j.Status == "QUEUED" && j.Attempts == 1
	})
	if got := getTranslation(t, server, tr.ID); got.Status != "EXTRACTING" {
		t.Fatalf("Translation after one failed attempt = %s, want EXTRACTING", got.Status)
	}
}

func TestRemovingABookRemovesItsTranslations(t *testing.T) {
	app, server := newApp(t)
	tr := reviewedTranslation(t, server, app)

	wantStatus(t, del(t, server.URL+"/books/"+tr.BookID), http.StatusNoContent)

	wantStatus(t, get(t, server.URL+"/translations/"+tr.ID), http.StatusNotFound)
	wantStatus(t, get(t, server.URL+"/translations/"+tr.ID+"/termbase"), http.StatusNotFound)
	var terms int
	err := db(t).QueryRow(context.Background(),
		"SELECT count(*) FROM terms JOIN termbases ON termbases.id = terms.termbase_id WHERE termbases.book_id = $1", tr.BookID).Scan(&terms)
	if err != nil || terms != 0 {
		t.Fatalf("Terms left after removal = %d (%v), want 0", terms, err)
	}
}
