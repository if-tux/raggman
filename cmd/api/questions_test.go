package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgvector/pgvector-go"
)

type sseEvent struct {
	Name string
	Data string
}

type citationJSON struct {
	DocumentID string `json:"document_id"`
	ChunkID    string `json:"chunk_id"`
	Filename   string `json:"filename"`
	Marker     int    `json:"marker"`
	Page       int    `json:"page"`
}

// ask posts a Question and reads the whole event stream.
func ask(t *testing.T, server *httptest.Server, question string) (*http.Response, []sseEvent) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"question": question})
	resp, err := http.Post(server.URL+"/questions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /questions: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}

	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			current.Name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.Data = strings.TrimPrefix(line, "data: ")
		case line == "" && current.Name != "":
			events = append(events, current)
			current = sseEvent{}
		}
	}
	return resp, events
}

// answerOf joins the token events and decodes the final citations event,
// which must come last. An optional thinking event may come first.
func answerOf(t *testing.T, events []sseEvent) (string, []citationJSON) {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Name != "citations" {
		t.Fatalf("events = %+v, want token events ending with a citations event", events)
	}
	if events[0].Name == "thinking" {
		events = events[1:]
	}
	var answer strings.Builder
	for _, e := range events[:len(events)-1] {
		if e.Name != "token" {
			t.Fatalf("unexpected event %+v before citations", e)
		}
		var token struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(e.Data), &token); err != nil {
			t.Fatal(err)
		}
		answer.WriteString(token.Text)
	}
	var final struct {
		Citations []citationJSON `json:"citations"`
	}
	if err := json.Unmarshal([]byte(events[len(events)-1].Data), &final); err != nil {
		t.Fatal(err)
	}
	return answer.String(), final.Citations
}

const notFound = "I could not find anything about this in the Corpus."

func TestQuestionStreamsAnswerCitingOnlyUsedSources(t *testing.T) {
	app, server := newApp(t)
	clearQueue(t)
	startIngestion(t, app)

	// Both Pages match the Question; Page 1 matches it word for word, so it is
	// source [1], the only one the stub model cites. The unique topic keeps
	// other tests' Documents from ranking first.
	topic := rand.Text()
	page1 := "Raggman answers Questions about " + topic
	content := makePDF(t, page1, "Raggman answers Questions about "+topic+" too")
	doc := decode[documentJSON](t, submitPDF(t, server, "guide.pdf", content).Body)
	waitForStatus(t, server, doc.ID, "READY")

	resp, events := ask(t, server, page1+"?")
	wantStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	answer, citations := answerOf(t, events)
	if answer != "The Corpus says Raggman answers Questions [1]." {
		t.Fatalf("answer = %q", answer)
	}
	if len(citations) != 1 {
		t.Fatalf("citations = %+v, want only source [1]", citations)
	}
	c := citations[0]
	if c.Marker != 1 || c.DocumentID != doc.ID || c.Filename != "guide.pdf" || c.Page != 1 || c.ChunkID == "" {
		t.Fatalf("citation = %+v, want [1] → guide.pdf Page 1", c)
	}
}

func TestUnrelatedQuestionIsNotFoundWithoutCallingModel(t *testing.T) {
	_, server := newApp(t)
	before := chatCalls.Load()

	resp, events := ask(t, server, "banana smoothie recipe "+rand.Text())
	wantStatus(t, resp, http.StatusOK)

	answer, citations := answerOf(t, events)
	if answer != notFound || len(citations) != 0 {
		t.Fatalf("got %q with %d citations, want the not-found Answer without Citations", answer, len(citations))
	}
	if chatCalls.Load() != before {
		t.Fatal("chat model was called although nothing relevant was found")
	}
}

func TestDocumentsThatAreNotReadyAreNeverSearched(t *testing.T) {
	_, server := newApp(t)
	pool := db(t)
	topic := "zebrafish " + rand.Text()

	// A Pending Document whose Chunk matches perfectly: it must stay invisible.
	doc := decode[documentJSON](t, submitPDF(t, server, "pending.pdf", makePDF(t, topic)).Body)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO chunks (id, document_id, page_number, content, embedding)
		 VALUES (gen_random_uuid(), $1, 1, $2, $3)`, doc.ID, topic, pgvector.NewVector(stubVector(topic)))
	if err != nil {
		t.Fatal(err)
	}

	resp, events := ask(t, server, topic)
	wantStatus(t, resp, http.StatusOK)
	if answer, _ := answerOf(t, events); answer != notFound {
		t.Fatalf("answer = %q, want the not-found Answer", answer)
	}
}

func TestEmptyQuestionIsRejected(t *testing.T) {
	_, server := newApp(t)

	resp, _ := ask(t, server, "   ")
	wantStatus(t, resp, http.StatusBadRequest)
}

// readyGuide submits a Document matching question word for word and waits
// until it is in the Corpus.
func readyGuide(t *testing.T, app *App, server *httptest.Server) (question string) {
	t.Helper()
	clearQueue(t)
	startIngestion(t, app)
	page := "Raggman answers Questions about " + rand.Text()
	doc := decode[documentJSON](t, submitPDF(t, server, "guide.pdf", makePDF(t, page)).Body)
	waitForStatus(t, server, doc.ID, "READY")
	return page + "?"
}

func TestThinkingModelSignalsThinkingBeforeAnswer(t *testing.T) {
	app, server := newApp(t)
	question := readyGuide(t, app, server)

	_, events := ask(t, server, question)

	if len(events) == 0 || events[0].Name != "thinking" {
		t.Fatalf("first event = %+v, want a thinking event while the model reasons", events)
	}
	if answer, _ := answerOf(t, events); answer != "The Corpus says Raggman answers Questions [1]." {
		t.Fatalf("answer = %q; reasoning text must not leak into the Answer", answer)
	}
}

func TestThinkingCanBeTurnedOff(t *testing.T) {
	t.Setenv("LLM_THINKING", "false")
	app, server := newApp(t)
	question := readyGuide(t, app, server)

	_, events := ask(t, server, question)

	for _, e := range events {
		if e.Name == "thinking" {
			t.Fatalf("got a thinking event with LLM_THINKING=false: %+v", events)
		}
	}
}

func TestChatFailureBeforeAnyTokenIsBadGateway(t *testing.T) {
	app, server := newApp(t)
	question := readyGuide(t, app, server)

	resp, _ := ask(t, server, question+" chat-outage")
	wantStatus(t, resp, http.StatusBadGateway)
}

func TestOverlongQuestionIsRejected(t *testing.T) {
	_, server := newApp(t)

	resp, _ := ask(t, server, strings.Repeat("a", 2001))
	wantStatus(t, resp, http.StatusBadRequest)
}

func TestThinkingSettingDoesNotApplyToCloudProviders(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-test")
	t.Setenv("LLM_THINKING", "false")
	app, server := newApp(t)
	question := readyGuide(t, app, server)

	_, events := ask(t, server, question)

	// With an API key, reasoning_effort is never sent, so the stub reasons.
	if len(events) == 0 || events[0].Name != "thinking" {
		t.Fatalf("events = %+v, want the provider default (thinking) despite LLM_THINKING=false", events)
	}
}
