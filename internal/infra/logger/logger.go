package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/lmittmann/tint"
	"go.opentelemetry.io/otel/trace"
)

// Output is where logs go; tests point it at a buffer.
var Output io.Writer = os.Stdout

func InitAndProvide(env *environment.Environment) *slog.Logger {
	var level slog.Level

	switch strings.ToUpper(env.LogLevel) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler

	if strings.ToLower(env.Env) == "production" {
		handler = slog.NewJSONHandler(Output, &slog.HandlerOptions{Level: level, ReplaceAttr: readableDurations})
	} else {
		handler = tint.NewHandler(Output, &tint.Options{
			Level:      level,
			TimeFormat: time.Kitchen,
		})
	}

	logger := slog.New(traceHandler{handler})
	slog.SetDefault(logger)

	return logger
}

// readableDurations logs 30s instead of 30000000000 in JSON.
func readableDurations(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindDuration {
		return slog.String(a.Key, a.Value.Duration().String())
	}
	return a
}

// traceHandler adds the trace and span IDs of the span in the log call's
// context, so a log line leads straight to its trace in Jaeger.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

// ponytail: after WithGroup the IDs land inside the group (grp.trace_id);
// nothing uses groups yet. Track open groups here if that changes.
func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}
