// Package telemetry sets up OpenTelemetry tracing, exported over OTLP to
// Jaeger, and carries trace context across the Job queue.
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// NewTracerProvider exports spans to env.OTLPEndpoint, unless
// OTEL_TRACES_EXPORTER is "none". The cleanup flushes pending spans.
func NewTracerProvider(ctx context.Context, env *environment.Environment, logger *slog.Logger) (*sdktrace.TracerProvider, func(), error) {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "raggman"))),
	}
	switch env.TracesExporter {
	case "none":
	case "otlp":
		exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(env.OTLPEndpoint))
		if err != nil {
			return nil, nil, fmt.Errorf("creating OTLP exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
		logger.Info("Exporting traces over OTLP", "endpoint", env.OTLPEndpoint)
	default:
		return nil, nil, fmt.Errorf(`OTEL_TRACES_EXPORTER must be "otlp" or "none", got %q`, env.TracesExporter)
	}
	tp := sdktrace.NewTracerProvider(opts...)

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			logger.Warn("Flushing traces failed", "error", err)
		}
	}
	return tp, cleanup, nil
}

// Propagator carries W3C trace context (traceparent) across processes.
var Propagator = propagation.TraceContext{}

// Carrier serializes ctx's trace context (W3C traceparent) for a Job payload.
func Carrier(ctx context.Context) []byte {
	carrier := propagation.MapCarrier{}
	Propagator.Inject(ctx, carrier)
	payload, _ := json.Marshal(carrier) // a map of strings always marshals
	return payload
}

// FromCarrier restores the trace context stored by Carrier, so work done for
// a Job continues the trace of the request that enqueued it.
func FromCarrier(ctx context.Context, payload []byte) context.Context {
	carrier := propagation.MapCarrier{}
	if json.Unmarshal(payload, &carrier) != nil {
		return ctx
	}
	return Propagator.Extract(ctx, carrier)
}

// Span runs fn in a child span named name, recording any error on it.
func Span[T any](ctx context.Context, tracer trace.Tracer, name string, fn func(context.Context) (T, error)) (T, error) {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	v, err := fn(ctx)
	RecordError(ctx, span, err)
	return v, err
}

// RecordError marks span failed for err, unless the work was cancelled (a
// client that left, or shutdown), which is noted without counting as a failure.
func RecordError(ctx context.Context, span trace.Span, err error) {
	switch {
	case err == nil:
	case ctx.Err() != nil:
		span.AddEvent("cancelled", trace.WithAttributes(attribute.String("cause", context.Cause(ctx).Error())))
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}
