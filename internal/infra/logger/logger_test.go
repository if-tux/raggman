package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func jsonLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	dec := json.NewDecoder(buf)
	for dec.More() {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			t.Fatalf("log output is not JSON lines: %v", err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestLogLinesInsideASpanCarryItsIDs(t *testing.T) {
	var buf bytes.Buffer
	Output = &buf
	t.Cleanup(func() { Output = os.Stdout })
	log := InitAndProvide(&environment.Environment{Env: "production"}).With("component", "test")

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "work")
	log.InfoContext(ctx, "inside")
	span.End()
	log.InfoContext(context.Background(), "outside")

	lines := jsonLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2", len(lines))
	}
	sc := span.SpanContext()
	if lines[0]["trace_id"] != sc.TraceID().String() || lines[0]["span_id"] != sc.SpanID().String() {
		t.Fatalf("line inside span = %v, want trace_id %s and span_id %s", lines[0], sc.TraceID(), sc.SpanID())
	}
	if _, ok := lines[1]["trace_id"]; ok {
		t.Fatalf("line outside any span = %v, want no trace_id", lines[1])
	}
}

func TestDurationsAreLoggedReadably(t *testing.T) {
	var buf bytes.Buffer
	Output = &buf
	t.Cleanup(func() { Output = os.Stdout })
	log := InitAndProvide(&environment.Environment{Env: "production"})

	log.Info("shutting down", "timeout", 30*time.Second)

	if got := jsonLines(t, &buf)[0]["timeout"]; got != "30s" {
		t.Fatalf("timeout logged as %v, want \"30s\"", got)
	}
}

func TestTextLogsCarryTraceIDsToo(t *testing.T) {
	var buf bytes.Buffer
	Output = &buf
	t.Cleanup(func() { Output = os.Stdout })
	log := InitAndProvide(&environment.Environment{Env: "development"})

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "work")
	log.InfoContext(ctx, "inside")
	span.End()

	if want := span.SpanContext().TraceID().String(); !strings.Contains(buf.String(), want) {
		t.Fatalf("text log %q does not carry trace_id %s", buf.String(), want)
	}
}
