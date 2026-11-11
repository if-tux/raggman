package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/epub/epubtest"
)

type progressJSON struct {
	Status           string `json:"status"`
	FailureReason    string `json:"failure_reason"`
	BlocksTranslated int    `json:"blocks_translated"`
	BlocksFlagged    int    `json:"blocks_flagged"`
	BlocksTotal      int    `json:"blocks_total"`
}

func getProgress(t *testing.T, server *httptest.Server, id string) progressJSON {
	t.Helper()
	resp := get(t, server.URL+"/translations/"+id)
	wantStatus(t, resp, http.StatusOK)
	return decode[progressJSON](t, resp.Body)
}

// translatedBlocks returns a Translation's stored Blocks, in reading order.
func translatedBlocks(t *testing.T, translationID string) []string {
	t.Helper()
	rows, err := db(t).Query(context.Background(),
		"SELECT translation FROM translated_blocks WHERE translation_id = $1 ORDER BY position", translationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// novel is a Book of 11 Blocks: the title, two table-of-contents entries, and
// two chapters. one and two are unique sentences, one per chapter; markers in
// them script the stub translator.
func novel(one, two string) []byte {
	return epubtest.Book("The Lighthouse Keeper", "en",
		`<h1>One</h1><p><em>Mara</em> lit the lamp above Port Varen.</p><p>***</p><p>Mara</p><p>`+one+`</p>`,
		`<h1>Two</h1><p>Mara left Port Varen at dawn.</p><p>Mara kept the glimmerstone. `+two+`</p>`)
}

// novelTranslated is what the stub makes of novel: "pt:" before every Block
// the model sees, with Names and Terms as the Termbase renders them; the
// scene break and the bare name are copied without it.
func novelTranslated(one, two string) []string {
	return []string{
		"pt:The Lighthouse Keeper", "pt:⟦1⟧Chapter 1⟦/1⟧", "pt:⟦1⟧Chapter 2⟦/1⟧",
		"pt:One", "pt:⟦1⟧Mara⟦/1⟧ lit the lamp above Porto Varen.", "***", "Mara", "pt:" + one,
		"pt:Two", "pt:Mara left Porto Varen at dawn.", "pt:Mara kept the pedra-lume. " + two,
	}
}

// locked takes a Translation of content to a Locked Termbase, with the
// workers stopped: the translation Job waits in the queue.
func locked(t *testing.T, server *httptest.Server, app *App, content []byte, level string) translationJSON {
	t.Helper()
	tr := reviewed(t, server, app, content, level)
	wantStatus(t, approve(t, server, tr.ID), http.StatusOK)
	return tr
}

func TestTranslationTranslatesEveryBlockAndCompletes(t *testing.T) {
	app, server := newApp(t)
	one, two := "A gull cried "+rand.Text()+".", "It glowed "+rand.Text()+"."
	tr := locked(t, server, app, novel(one, two), "children")

	if p := getProgress(t, server, tr.ID); p.Status != "TRANSLATING" || p.BlocksTranslated != 0 || p.BlocksTotal != 11 {
		t.Fatalf("progress before translating = %+v, want TRANSLATING 0 of 11", p)
	}

	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	if p := getProgress(t, server, tr.ID); p.BlocksTranslated != 11 || p.BlocksTotal != 11 {
		t.Fatalf("progress = %+v, want 11 of 11", p)
	}
	if got, want := translatedBlocks(t, tr.ID), novelTranslated(one, two); !slices.Equal(got, want) {
		t.Fatalf("translated Blocks =\n  %q\nwant\n  %q", got, want)
	}
	if n := queuedJobs(t, "TRANSLATION", tr.ID); n != 0 {
		t.Fatalf("translation Jobs left = %d, want 0", n)
	}
}

func TestEachWindowCarriesLevelTermsContextAndNumberedBlocks(t *testing.T) {
	app, server := newApp(t)
	one, two := "A gull cried "+rand.Text()+".", "It glowed "+rand.Text()+"."
	tr := locked(t, server, app, novel(one, two), "children")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	// Chapter one is one Window.
	requests := translationRequestsWith(one)
	if len(requests) != 1 {
		t.Fatalf("calls for chapter one = %d, want 1", len(requests))
	}
	first := requests[0]
	for _, want := range []string{"Brazilian Portuguese (pt-BR)", "Write for children", "Never add, remove or summarise"} {
		if !strings.Contains(first.system, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	_, blocks, _ := strings.Cut(first.user, "Translate these blocks:")
	wantBlocks := "\n<block 1>\nOne\n</block>\n<block 2>\n⟦1⟧Mara⟦/1⟧ lit the lamp above Port Varen.\n</block>\n<block 3>\n" + one + "\n</block>\n"
	if blocks != wantBlocks {
		t.Errorf("blocks to translate =\n%q\nwant the three that need the model, numbered:\n%q", blocks, wantBlocks)
	}
	// Only the Terms this Window mentions.
	if !strings.Contains(first.user, "- Mara => Mara\n") || !strings.Contains(first.user, "- Port Varen => Porto Varen\n") || strings.Contains(first.user, "glimmerstone") {
		t.Errorf("chapter one's Terms are wrong:\n%s", first.user)
	}
	// Its context is what came before: the title and the table of contents.
	if !strings.Contains(first.user, "<source>The Lighthouse Keeper</source>\n<translation>pt:The Lighthouse Keeper</translation>") {
		t.Errorf("chapter one's context lacks the title:\n%s", first.user)
	}

	// Chapter two sees the last three translated Blocks of chapter one.
	requests = translationRequestsWith(two)
	if len(requests) != 1 {
		t.Fatalf("calls for chapter two = %d, want 1", len(requests))
	}
	second := requests[0].user
	context, _, _ := strings.Cut(second, "Translate these blocks:")
	wantContext := "<before>\n<source>***</source>\n<translation>***</translation>\n</before>\n" +
		"<before>\n<source>Mara</source>\n<translation>Mara</translation>\n</before>\n" +
		"<before>\n<source>" + one + "</source>\n<translation>pt:" + one + "</translation>\n</before>\n"
	if !strings.Contains(context, wantContext) || strings.Count(context, "<before>") != 3 {
		t.Errorf("chapter two's context =\n%s\nwant exactly the last three Blocks:\n%s", context, wantContext)
	}
	// A name is fixed to the letter; another Term may inflect (ADR-0006).
	names := "Names (write each one exactly as given, every time):\n- Mara => Mara\n- Port Varen => Porto Varen\n\n"
	inflecting := "Terms (always use this rendering; it may inflect to fit the sentence):\n- glimmerstone => pedra-lume\n\n"
	if !strings.Contains(second, names+inflecting) {
		t.Errorf("chapter two's Names and Terms are not listed apart:\n%s", second)
	}
}

func TestEveryLevelForbidsChangingContent(t *testing.T) {
	app, server := newApp(t)
	instructions := map[string]string{"formal": "formal register", "informal": "informal, conversational register", "children": "Write for children"}
	for level, instruction := range instructions {
		mark := "A gull cried " + rand.Text() + "."
		tr := locked(t, server, app, novel(mark, "It glowed."), level)
		stop := startIngestion(t, app)
		waitForTranslation(t, server, tr.ID, "COMPLETE")
		stop()

		requests := translationRequestsWith(mark)
		if len(requests) != 1 {
			t.Fatalf("%s: calls = %d, want 1", level, len(requests))
		}
		if system := requests[0].system; !strings.Contains(system, instruction) || !strings.Contains(system, "Never add, remove or summarise content, whatever the register") {
			t.Errorf("%s: system prompt lacks its instruction or the content rule:\n%s", level, system)
		}
	}
}

func TestBlockOverTheCeilingIsTranslatedInPartsAndJoined(t *testing.T) {
	app, server := newApp(t)
	var long strings.Builder
	mark := rand.Text()
	for i := range 140 {
		fmt.Fprintf(&long, "Wave %d of %s broke on the shore. ", i, mark)
	}
	source := strings.TrimSpace(long.String()) // well over the 4800-character ceiling
	tr := locked(t, server, app, epubtest.Book("T", "en", `<p>`+source+`</p>`), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	parts := domain.SplitSentences(source, 2400)
	if len(parts) < 3 {
		t.Fatalf("the fixture splits into %d parts, want at least 3", len(parts))
	}
	want := make([]string, len(parts))
	for i, part := range parts {
		want[i] = "pt:" + strings.TrimSpace(part)
	}
	got := translatedBlocks(t, tr.ID)
	if long := got[len(got)-1]; long != strings.Join(want, " ") {
		t.Fatalf("the long Block's translation is not its parts joined in order (%d characters, want %d)", len(long), len(strings.Join(want, " ")))
	}

	requests := translationRequestsWith(mark)
	if len(requests) != len(parts) {
		t.Fatalf("calls for the long Block = %d, want one per part (%d)", len(requests), len(parts))
	}
	// The second part sees the first as context.
	if !strings.Contains(requests[1].user, "<translation>"+want[0]+"</translation>") {
		t.Error("the second part's context lacks the first part's translation")
	}
}

func TestChapterOverTheBudgetIsTranslatedInSeveralWindows(t *testing.T) {
	app, server := newApp(t)
	// Three Blocks of about 1000 characters: two fit a Window, the third
	// starts the next.
	marks := []string{rand.Text(), rand.Text(), rand.Text()}
	var chapter strings.Builder
	for _, mark := range marks {
		chapter.WriteString("<p>" + mark + strings.Repeat(" The tide came in and went out.", 32) + "</p>")
	}
	tr := locked(t, server, app, epubtest.Book("T", "en", chapter.String()), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	first, third := translationRequestsWith(marks[0]), translationRequestsWith(marks[2])
	if len(first) != 1 || len(third) != 1 {
		t.Fatalf("calls with the first paragraph = %d, with the third = %d; want 1 and 1", len(first), len(third))
	}
	_, blocks, _ := strings.Cut(first[0].user, "Translate these blocks:")
	if !strings.Contains(blocks, marks[1]) || strings.Contains(blocks, marks[2]) || strings.Count(blocks, "<block ") != 2 {
		t.Errorf("the first Window should hold the first two Blocks only")
	}
	_, blocks, _ = strings.Cut(third[0].user, "Translate these blocks:")
	if !strings.HasPrefix(blocks, "\n<block 1>\n"+marks[2]) || strings.Count(blocks, "<block ") != 1 {
		t.Errorf("the second Window should hold the third Block alone, numbered from 1")
	}
}

func TestWorkerThatLostItsJobStoresNothing(t *testing.T) {
	app, server := newApp(t)
	one, two := "A gull cried "+rand.Text()+".", "slow-translation "+rand.Text()
	tr := locked(t, server, app, novel(one, two), "formal")
	startIngestion(t, app)
	eventually(t, "chapter one to be stored", func() bool { return getProgress(t, server, tr.ID).BlocksTranslated == 8 })

	// Chapter two is with the model for 3 seconds. Meanwhile another worker
	// takes the Job over.
	if _, err := db(t).Exec(context.Background(), "UPDATE job_queue SET locked_by = 'another-worker' WHERE target_id = $1", tr.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the model to be asked for chapter two", func() bool { return len(translationRequestsWith(two)) == 1 })
	time.Sleep(4 * time.Second) // the reply arrives and would be stored by now

	if p := getProgress(t, server, tr.ID); p.Status != "TRANSLATING" || p.BlocksTranslated != 8 {
		t.Fatalf("after losing the Job = %+v, want TRANSLATING with chapter two not stored", p)
	}
}

func TestBlockOverTheBudgetButUnderTheCeilingIsSentWhole(t *testing.T) {
	app, server := newApp(t)
	mark := rand.Text()
	source := mark + strings.Repeat(" The tide came in and went out.", 100) // about 3100 characters
	tr := locked(t, server, app, epubtest.Book("T", "en", `<p>`+source+`</p><p>Short.</p>`), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	requests := translationRequestsWith(mark)
	if len(requests) != 1 {
		t.Fatalf("calls for the long Block = %d, want 1", len(requests))
	}
	_, blocks, _ := strings.Cut(requests[0].user, "Translate these blocks:")
	if blocks != "\n<block 1>\n"+source+"\n</block>\n" {
		t.Errorf("the long Block should be alone in its Window, whole")
	}
}

func TestInterruptedTranslationResumesWithoutRetranslating(t *testing.T) {
	app, server := newApp(t)
	one, two := "A gull cried "+rand.Text()+".", "slow-translation "+rand.Text()
	tr := locked(t, server, app, novel(one, two), "formal")

	// Shutdown gives the in-flight Window no grace: it is abandoned.
	app.Workers.ShutdownGrace = 0
	stop := startIngestion(t, app)
	eventually(t, "chapter one to be stored", func() bool { return getProgress(t, server, tr.ID).BlocksTranslated == 8 })
	stop()

	if p := getProgress(t, server, tr.ID); p.Status != "TRANSLATING" || p.BlocksTranslated != 8 {
		t.Fatalf("after the interruption = %+v, want TRANSLATING with 8 Blocks kept", p)
	}
	if n := queuedJobs(t, "TRANSLATION", tr.ID); n != 1 {
		t.Fatalf("translation Jobs queued after the interruption = %d, want 1 (released)", n)
	}

	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	if got, want := translatedBlocks(t, tr.ID), novelTranslated(one, two); !slices.Equal(got, want) {
		t.Fatalf("translated Blocks =\n  %q\nwant\n  %q", got, want)
	}
	if n := len(translationRequestsWith(one)); n != 1 {
		t.Fatalf("chapter one was sent to the model %d times, want 1: it was already stored", n)
	}
	again := translationRequestsWith(two)
	if len(again) != 2 {
		t.Fatalf("chapter two was sent %d times, want 2 (interrupted, then resumed)", len(again))
	}
	// The resumed attempt reads its context from what was stored.
	if !strings.Contains(again[1].user, "<translation>pt:"+one+"</translation>") {
		t.Error("the resumed Window's context lacks the Block translated before the interruption")
	}
}

func TestModelOutageRetriesTheJobThenFailsTheTranslation(t *testing.T) {
	app, server := newApp(t)
	pool := db(t)
	tr := locked(t, server, app, novel("translation-outage "+rand.Text(), "It glowed."), "formal")
	startIngestion(t, app)

	for attempt := int32(1); attempt <= 2; attempt++ {
		eventually(t, "a failed translation attempt to be re-queued", func() bool {
			j, ok := job(t, pool, tr.ID)
			return ok && j.Status == "QUEUED" && j.Attempts == attempt
		})
		// The title and table of contents were stored before the outage.
		if p := getProgress(t, server, tr.ID); p.Status != "TRANSLATING" || p.BlocksTranslated != 3 {
			t.Fatalf("between attempts = %+v, want TRANSLATING with 3 Blocks kept", p)
		}
		// Skip the backoff wait.
		if _, err := pool.Exec(context.Background(), "UPDATE job_queue SET run_after = NOW() WHERE target_id = $1", tr.ID); err != nil {
			t.Fatal(err)
		}
	}

	waitForTranslation(t, server, tr.ID, "FAILED")
	p := getProgress(t, server, tr.ID)
	if !strings.Contains(p.FailureReason, "3 attempts") || !strings.Contains(p.FailureReason, "model overloaded") {
		t.Fatalf("failure reason = %q, want it to mention 3 attempts and the cause", p.FailureReason)
	}
	if p.BlocksTranslated != 3 || p.BlocksFlagged != 0 {
		t.Fatalf("after failing = %+v, want the 3 stored Blocks kept and none Flagged", p)
	}
}
