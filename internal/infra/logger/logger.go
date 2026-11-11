package logger

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/lmittmann/tint"
)

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
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = tint.NewHandler(os.Stdout, &tint.Options{
			Level:      level,
			TimeFormat: time.Kitchen,
		})
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	return logger
}
