package main

import (
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/BrunoPereiradosSantos/raggman/internal/epub/epubtest"
)

type reportedJSON struct {
	Source      string `json:"source"`
	Translation string `json:"translation"`
	Reason      string `json:"reason"`
	Position    int    `json:"position"`
	Chapter     int    `json:"chapter"`
}

type warningJSON struct {
	Warning   string       `json:"warning"`
	Positions []int        `json:"positions"`
	Example   reportedJSON `json:"example"`
	Blocks    int          `json:"blocks"`
}

type reportJSON struct {
	Flagged  []reportedJSON `json:"flagged"`
	Warnings []warningJSON  `json:"warnings"`
}

func getReport(t *testing.T, server *httptest.Server, translationID string) reportJSON {
	t.Helper()
	resp := get(t, server.URL+"/translations/"+translationID+"/report")
	wantStatus(t, resp, http.StatusOK)
	return decode[reportJSON](t, resp.Body)
}

// mistake is one way the stub translator gets a Block wrong, and what
// Raggman must make of it.
type mistake struct {
	// attempt is the stub's wrong translation of the Block: what stays when
	// the Block ends up Flagged. It is given the Block's source.
	attempt func(source string) string
	// word asks the stub for the mistake; "-once", "-twice" or "-always" is
	// added to it.
	word string
	// reason is part of the reason Raggman gives for rejecting the Block.
	reason string
	// wholeWindow is true when the mistake breaks the reply as a whole, so
	// every Block of the Window fails with it, not just one.
	wholeWindow bool
}

var mistakes = []mistake{
	{word: "wrong-name", reason: `the name "Port Varen" must be written "Porto Varen"`,
		attempt: func(source string) string { return "pt:" + source }},
	{word: "lose-marker", reason: "the formatting mark ⟦1⟧ is missing",
		attempt: func(source string) string {
			return "pt:" + strings.NewReplacer("⟦1⟧", "", "⟦/1⟧", "", "Port Varen", "Porto Varen").Replace(source)
		}},
	{word: "echo-source", reason: "the block was not translated",
		attempt: func(source string) string { return source }},
	{word: "too-short", reason: "the translation is too short for its source",
		attempt: func(string) string { return "pt:⟦1⟧x⟦/1⟧" }},
	{word: "drop-last", reason: "the reply does not hold each numbered block exactly once", wholeWindow: true,
		// The model never returned the Block, so its source is kept.
		attempt: func(source string) string { return source }},
}

// troubled is a Book whose only chapter is one Window of three Blocks; the
// last one carries word, which makes the stub get it wrong. It returns the
// Book, the first Block's unique text and the last Block's source.
func troubled(word string) (content []byte, first, last string) {
	first = "Mara lit the lamp " + rand.Text() + "."
	place := "Port Varen"
	if strings.HasPrefix(word, "echo-source") || strings.HasPrefix(word, "too-short") {
		// With a name in it, such a Block would fail on the name first.
		place = "the old harbour"
	}
	body := "The boat left " + place + " <em>slowly</em> that night, " + word + " " + rand.Text() + "."
	last = strings.NewReplacer("<em>", "⟦1⟧", "</em>", "⟦/1⟧").Replace(body)
	content = epubtest.Book("T", "en",
		`<p>`+first+`</p><p>Mara slept in Port Varen `+rand.Text()+`.</p><p>`+body+`</p>`)
	return content, first, last
}

// good is the stub's correct translation of a Block's source.
func good(source string) string {
	return "pt:" + strings.ReplaceAll(source, "Port Varen", "Porto Varen")
}

func blocksToTranslate(req translationRequest) string {
	_, blocks, _ := strings.Cut(req.user, "Translate these blocks:")
	blocks, _, _ = strings.Cut(blocks, "\nYour previous answer was rejected")
	return blocks
}

func TestRejectedWindowIsAskedAgainWithTheFailedCheck(t *testing.T) {
	app, server := newApp(t)
	for _, m := range mistakes {
		content, first, last := troubled(m.word + "-once")
		tr := locked(t, server, app, content, "formal")
		stop := startIngestion(t, app)
		waitForTranslation(t, server, tr.ID, "COMPLETE")
		stop()

		requests := translationRequestsWith(last)
		if len(requests) != 2 {
			t.Errorf("%s: the Window was sent %d times, want 2", m.word, len(requests))
			continue
		}
		if strings.Contains(requests[0].user, "rejected") {
			t.Errorf("%s: the first request already carries feedback", m.word)
		}
		// The retry is the same Window, told what was wrong.
		if blocksToTranslate(requests[1]) != blocksToTranslate(requests[0]) || strings.Count(blocksToTranslate(requests[1]), "<block ") != 3 {
			t.Errorf("%s: the retry is not the same Window of three Blocks", m.word)
		}
		wantFeedback := "- block 3: " + m.reason
		if m.wholeWindow {
			wantFeedback = "- " + m.reason
		}
		if !strings.Contains(requests[1].user, "Your previous answer was rejected:\n") || !strings.Contains(requests[1].user, wantFeedback) {
			t.Errorf("%s: the retry does not describe the failed check %q:\n%s", m.word, wantFeedback, requests[1].user)
		}
		if got := translatedBlocks(t, tr.ID); got[len(got)-1] != good(last) || !slices.Contains(got, good(first)) {
			t.Errorf("%s: stored Blocks = %q, want the corrected translations", m.word, got)
		}
		if p := getProgress(t, server, tr.ID); p.BlocksFlagged != 0 {
			t.Errorf("%s: Flagged = %d, want 0", m.word, p.BlocksFlagged)
		}
	}
}

func TestBlockThatPassedKeepsItsTranslationWhenTheWindowIsAskedAgain(t *testing.T) {
	app, server := newApp(t)
	// The first Block is right at once and wrong in the retry; the second is
	// the other way round.
	steady := "Mara ⟦1⟧lit⟦/1⟧ the lamp, lose-marker-second " + rand.Text() + "."
	late := "The boat left ⟦1⟧slowly⟦/1⟧ that night, lose-marker-once " + rand.Text() + "."
	html := strings.NewReplacer("⟦1⟧", "<em>", "⟦/1⟧", "</em>")
	tr := locked(t, server, app, epubtest.Book("T", "en", `<p>`+html.Replace(steady)+`</p><p>`+html.Replace(late)+`</p>`), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	if got := translatedBlocks(t, tr.ID); !slices.Equal(got[len(got)-2:], []string{"pt:" + steady, "pt:" + late}) {
		t.Fatalf("stored Blocks = %q, want each Block's passing translation", got[len(got)-2:])
	}
	// Window and retry, and no more: neither Block had to be asked alone.
	if n, m := len(translationRequestsWith(steady)), len(translationRequestsWith(late)); n != 2 || m != 2 {
		t.Fatalf("requests = %d and %d, want 2 and 2", n, m)
	}
	if p := getProgress(t, server, tr.ID); p.BlocksFlagged != 0 {
		t.Fatalf("Flagged = %d, want 0", p.BlocksFlagged)
	}
}

func TestBlockThatFailsTwiceIsAskedAlone(t *testing.T) {
	app, server := newApp(t)
	for _, m := range mistakes {
		content, first, last := troubled(m.word + "-twice")
		tr := locked(t, server, app, content, "formal")
		stop := startIngestion(t, app)
		waitForTranslation(t, server, tr.ID, "COMPLETE")
		stop()

		requests := translationRequestsWith(last)
		if len(requests) != 3 {
			t.Errorf("%s: the Block was sent %d times, want 3 (Window, retry, alone)", m.word, len(requests))
			continue
		}
		alone := blocksToTranslate(requests[2])
		if alone != "\n<block 1>\n"+last+"\n</block>\n" {
			t.Errorf("%s: the third request is not the Block alone:\n%s", m.word, alone)
		}
		if !strings.Contains(requests[2].user, m.reason) {
			t.Errorf("%s: the Block alone is not told what was wrong", m.word)
		}
		// A Block that passed is not asked again; one that failed with the
		// whole Window is.
		wantFirst := 2
		if m.wholeWindow {
			wantFirst = 3
		}
		if n := len(translationRequestsWith(first)); n != wantFirst {
			t.Errorf("%s: the first Block was sent %d times, want %d", m.word, n, wantFirst)
		}
		if got := translatedBlocks(t, tr.ID); got[len(got)-1] != good(last) || !slices.Contains(got, good(first)) {
			t.Errorf("%s: stored Blocks = %q, want the corrected translations", m.word, got)
		}
		if p := getProgress(t, server, tr.ID); p.BlocksFlagged != 0 {
			t.Errorf("%s: Flagged = %d, want 0", m.word, p.BlocksFlagged)
		}
	}
}

func TestBlockThatFailsEvenAloneIsFlaggedAndTheTranslationCompletes(t *testing.T) {
	app, server := newApp(t)
	for _, m := range mistakes {
		content, first, last := troubled(m.word + "-always")
		tr := locked(t, server, app, content, "formal")
		stop := startIngestion(t, app)
		waitForTranslation(t, server, tr.ID, "COMPLETE")
		stop()

		if n := len(translationRequestsWith(last)); n != 3 {
			t.Errorf("%s: the Block was sent %d times, want 3 and no more", m.word, n)
		}
		// Title, table-of-contents entry and three Blocks; one is Flagged.
		if p := getProgress(t, server, tr.ID); p.BlocksTranslated != 5 || p.BlocksTotal != 5 || p.BlocksFlagged != 1 {
			t.Errorf("%s: progress = %+v, want 5 of 5 with 1 Flagged", m.word, p)
		}
		report := getReport(t, server, tr.ID)
		if len(report.Flagged) != 1 {
			t.Errorf("%s: Flagged Blocks in the report = %d, want 1", m.word, len(report.Flagged))
			continue
		}
		flagged := report.Flagged[0]
		want := reportedJSON{Position: 4, Chapter: 1, Source: last, Translation: m.attempt(last)}
		if flagged.Reason, want.Reason = "", ""; !strings.Contains(report.Flagged[0].Reason, m.reason) || flagged != want {
			t.Errorf("%s: Flagged Block =\n  %+v\nwant, with a reason saying %q,\n  %+v", m.word, report.Flagged[0], m.reason, want)
		}
		// The best attempt is what the Book gets; its neighbours are fine.
		got := translatedBlocks(t, tr.ID)
		if got[len(got)-1] != m.attempt(last) || !slices.Contains(got, good(first)) {
			t.Errorf("%s: stored Blocks = %q", m.word, got)
		}
	}
}

func TestFlaggedBlockKeepsTheReasonOfTheTranslationItKeeps(t *testing.T) {
	app, server := newApp(t)
	// The Block's name is wrong in the first reply; every later reply leaves
	// the Block out altogether.
	last := "The boat left Port Varen that night, wrong-name-once drop-last-later " + rand.Text() + "."
	content := epubtest.Book("T", "en", `<p>Mara lit the lamp `+rand.Text()+`.</p><p>`+last+`</p>`)
	tr := locked(t, server, app, content, "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	report := getReport(t, server, tr.ID)
	if len(report.Flagged) != 1 {
		t.Fatalf("Flagged Blocks = %d, want 1", len(report.Flagged))
	}
	flagged := report.Flagged[0]
	// The translation kept is the one with the wrong name, so that is the
	// reason kept, not the numbering of the replies that came after.
	if flagged.Translation != "pt:"+last || !strings.Contains(flagged.Reason, `the name "Port Varen" must be written "Porto Varen"`) {
		t.Fatalf("Flagged Block = %+v, want the first translation with the reason it failed for", flagged)
	}
	// Alone, the Block is still told about the name.
	requests := translationRequestsWith(last)
	if len(requests) != 3 || !strings.Contains(requests[2].user, `the name "Port Varen" must be written "Porto Varen"`) {
		t.Fatalf("the Block was sent %d times; alone, it should be told about the name", len(requests))
	}
}

func TestBlockAloneInItsWindowIsTriedTwiceThenFlaggedAndKeptOutOfTheContext(t *testing.T) {
	app, server := newApp(t)
	alone := "The boat left ⟦1⟧slowly⟦/1⟧ that night, lose-marker-always " + rand.Text() + "."
	next := "A gull cried " + rand.Text() + "."
	html := strings.NewReplacer("⟦1⟧", "<em>", "⟦/1⟧", "</em>")
	tr := locked(t, server, app, epubtest.Book("T", "en", `<p>`+html.Replace(alone)+`</p>`, `<p>`+next+`</p>`), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	// It is already alone: there is no third rung to climb.
	if n := len(translationRequestsWith(alone)); n != 2 {
		t.Fatalf("the Block was sent %d times, want 2", n)
	}
	if report := getReport(t, server, tr.ID); len(report.Flagged) != 1 || report.Flagged[0].Source != alone {
		t.Fatalf("report = %+v, want the Block Flagged", report)
	}
	// A translation that failed its checks is not shown to the model as an
	// example of what comes before.
	following := translationRequestsWith(next)
	if len(following) != 1 {
		t.Fatalf("the next chapter was sent %d times, want 1", len(following))
	}
	context, _, _ := strings.Cut(following[0].user, "Translate these blocks:")
	if strings.Contains(context, "lose-marker-always") {
		t.Fatalf("the Flagged Block is in the next Window's context:\n%s", context)
	}
	if !strings.Contains(context, "<before>") {
		t.Fatalf("the next Window has no context at all; the table of contents should still be there:\n%s", context)
	}
}

func TestResumedTranslationStillKeepsFlaggedBlocksOutOfTheContext(t *testing.T) {
	app, server := newApp(t)
	flagged := "The boat left ⟦1⟧slowly⟦/1⟧ that night, lose-marker-always " + rand.Text() + "."
	next := "slow-translation A gull cried " + rand.Text() + "."
	html := strings.NewReplacer("⟦1⟧", "<em>", "⟦/1⟧", "</em>")
	tr := locked(t, server, app, epubtest.Book("T", "en", `<p>`+html.Replace(flagged)+`</p>`, `<p>`+next+`</p>`), "formal")

	// Interrupt while the second chapter is with the model: the title, two
	// table-of-contents entries and the Flagged Block are stored.
	app.Workers.ShutdownGrace = 0
	stop := startIngestion(t, app)
	eventually(t, "the first chapter to be stored", func() bool { return getProgress(t, server, tr.ID).BlocksTranslated == 4 })
	stop()
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	requests := translationRequestsWith(next)
	if len(requests) != 2 {
		t.Fatalf("the second chapter was sent %d times, want 2 (interrupted, then resumed)", len(requests))
	}
	// The resumed attempt reads what is stored, and still leaves the Flagged
	// Block out of what it shows the model.
	context, _, _ := strings.Cut(requests[1].user, "Translate these blocks:")
	if strings.Contains(context, "lose-marker-always") || !strings.Contains(context, "<before>") {
		t.Fatalf("the resumed Window's context should hold the table of contents and not the Flagged Block:\n%s", context)
	}
	if p := getProgress(t, server, tr.ID); p.BlocksFlagged != 1 || p.BlocksTranslated != 5 {
		t.Fatalf("progress = %+v, want 5 Blocks with 1 Flagged", p)
	}
}

func TestWarningsAreReportedOncePerWordWithoutRejectingAnyBlock(t *testing.T) {
	app, server := newApp(t)
	mark := rand.Text()
	// The stub leaves the glimmerstone untranslated in the first Block and
	// renames Annabel in the last three.
	skipped := "Mara kept the glimmerstone in Port Varen, skip-term-always " + mark + "."
	met := "Then she met Annabel at the quay " + mark + "."
	said := "It was late, and Annabel said nothing " + mark + "."
	both := "By morning Annabel and Tobias were gone " + mark + "."
	tr := locked(t, server, app, epubtest.Book("T", "en",
		`<p>`+skipped+`</p><p>`+met+`</p>`, `<p>`+said+`</p><p>`+both+`</p>`), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	// No Block was asked again: a warning never rejects.
	for _, source := range []string{skipped, said} {
		if n := len(translationRequestsWith(source)); n != 1 {
			t.Fatalf("a Window was sent %d times, want 1", n)
		}
	}
	if p := getProgress(t, server, tr.ID); p.BlocksFlagged != 0 || p.BlocksTranslated != p.BlocksTotal {
		t.Fatalf("progress = %+v, want everything translated and nothing Flagged", p)
	}

	// One line per warning, the most frequent first, each with the Blocks it
	// was raised for and the first of them as an example.
	report := getReport(t, server, tr.ID)
	if len(report.Flagged) != 0 || len(report.Warnings) != 2 {
		t.Fatalf("report = %+v, want no Flagged Block and two warnings", report)
	}
	annabel, term := report.Warnings[0], report.Warnings[1]
	wantExample := reportedJSON{Position: 4, Chapter: 1, Source: met, Translation: "pt:" + strings.ReplaceAll(met, "Annabel", "Ana")}
	if annabel.Warning != `"Annabel" is not in the translation; if it is a name, add it to the Termbase` ||
		annabel.Blocks != 3 || !slices.Equal(annabel.Positions, []int{4, 5, 6}) || annabel.Example != wantExample {
		t.Errorf("first warning =\n  %+v\nwant Annabel, in Blocks 4, 5 and 6, with this example\n  %+v", annabel, wantExample)
	}
	wantExample = reportedJSON{Position: 3, Chapter: 1, Source: skipped, Translation: "pt:" + strings.ReplaceAll(skipped, "Port Varen", "Porto Varen")}
	if term.Warning != `the Term "glimmerstone" was not rendered as "pedra-lume"` ||
		term.Blocks != 1 || !slices.Equal(term.Positions, []int{3}) || term.Example != wantExample {
		t.Errorf("second warning =\n  %+v\nwant the Term, in Block 3, with this example\n  %+v", term, wantExample)
	}
}

func TestReportOfACleanTranslationIsEmpty(t *testing.T) {
	app, server := newApp(t)
	tr := locked(t, server, app, novel("A gull cried "+rand.Text()+".", "It glowed."), "formal")
	startIngestion(t, app)
	waitForTranslation(t, server, tr.ID, "COMPLETE")

	resp := get(t, server.URL+"/translations/"+tr.ID+"/report")
	wantStatus(t, resp, http.StatusOK)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != `{"flagged":[],"warnings":[]}` {
		t.Fatalf("report = %s, want empty lists", got)
	}
	wantStatus(t, get(t, server.URL+"/translations/00000000-0000-0000-0000-000000000000/report"), http.StatusNotFound)
}
