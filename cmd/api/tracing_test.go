package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/logger"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// syncBuffer collects logs written by concurrent workers.
type syncBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// logLines returns the JSON log lines whose msg is msg.
func (b *syncBuffer) logLines(t *testing.T, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var found []map[string]any
	for _, raw := range bytes.Split(b.buf.Bytes(), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) == nil && line["msg"] == msg {
			found = append(found, line)
		}
	}
	return found
}

// tracedApp boots the app with JSON logs captured and spans recorded in memory.
func tracedApp(t *testing.T) (*App, *httptest.Server, *syncBuffer, *tracetest.InMemoryExporter) {
	t.Helper()
	t.Setenv("APP_ENV", "production")
	logs := &syncBuffer{}
	logger.Output = logs
	t.Cleanup(func() { logger.Output = os.Stdout })

	app, server := newApp(t)
	spans := tracetest.NewInMemoryExporter()
	app.Tracing.RegisterSpanProcessor(sdktrace.NewSimpleSpanProcessor(spans))
	return app, server, logs, spans
}

func spanNamed(t *testing.T, spans *tracetest.InMemoryExporter, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range spans.GetSpans() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no span named %q among %v", name, spanNames(spans))
	return tracetest.SpanStub{}
}

func spanNames(spans *tracetest.InMemoryExporter) []string {
	var names []string
	for _, s := range spans.GetSpans() {
		names = append(names, s.Name)
	}
	return names
}

func wantSpansInTrace(t *testing.T, spans *tracetest.InMemoryExporter, traceID trace.TraceID, names ...string) {
	t.Helper()
	for _, name := range names {
		if got := spanNamed(t, spans, name).SpanContext.TraceID(); got != traceID {
			t.Fatalf("span %q is in trace %s, want %s", name, got, traceID)
		}
	}
}

func TestIngestionJoinsTheSubmissionTrace(t *testing.T) {
	app, server, logs, spans := tracedApp(t)
	clearQueue(t)
	startIngestion(t, app)

	doc := decode[documentJSON](t, submitPDF(t, server, "traced.pdf", makePDF(t, "traced "+rand.Text())).Body)
	waitForStatus(t, server, doc.ID, "READY")

	// READY commits inside the Ingestion span, which ends (and logs) just after.
	var finished map[string]any
	eventually(t, `the "Ingestion finished" log line`, func() bool {
		for _, line := range logs.logLines(t, "Ingestion finished") {
			if line["document_id"] == doc.ID {
				finished = line
			}
		}
		return finished != nil && slices.Contains(spanNames(spans), "ingestion")
	})

	submission := spanNamed(t, spans, "POST /documents").SpanContext.TraceID()
	wantSpansInTrace(t, spans, submission,
		"ingestion", "ingestion.extract", "ingestion.chunk", "ingestion.embed", "ingestion.store")
	if finished["trace_id"] != submission.String() {
		t.Fatalf("Ingestion logged trace_id %v, want the submission's %s", finished["trace_id"], submission)
	}
}

func TestQuestionTraceCoversRetrievalAndGeneration(t *testing.T) {
	app, server, _, spans := tracedApp(t)
	question := readyGuide(t, app, server)

	ask(t, server, question)

	trace := spanNamed(t, spans, "POST /questions").SpanContext.TraceID()
	wantSpansInTrace(t, spans, trace, "answering.embed", "answering.retrieve", "answering.generate")
}

func TestHealthChecksAreNotTraced(t *testing.T) {
	_, server, _, spans := tracedApp(t)

	get(t, server.URL+"/healthz")

	if slices.Contains(spanNames(spans), "GET /healthz") {
		t.Fatal("health checks would flood Jaeger every 5s")
	}
}

func TestRequestsContinueTheCallersTrace(t *testing.T) {
	_, server, _, spans := tracedApp(t)
	const callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/documents", nil)
	req.Header.Set("traceparent", "00-"+callerTrace+"-00f067aa0ba902b7-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := spanNamed(t, spans, "GET /documents").SpanContext.TraceID().String(); got != callerTrace {
		t.Fatalf("server span in trace %s, want the caller's %s", got, callerTrace)
	}
}
