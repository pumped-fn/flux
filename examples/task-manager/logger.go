package main

import (
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/pumped-fn/flux"
)

var (
	logLevelTag = flux.NewTagWithDefault("log-level", "info")
	logLevelDep = flux.Required(logLevelTag)
)

var logger = flux.NewAtomFrom(logLevelDep, func(rc *flux.ResolveContext, levelStr string) (*slog.Logger, error) {
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})), nil
}, flux.WithAtomName("logger"), flux.WithKeepAlive())

var reqCounter atomic.Uint64

var reqID = flux.NewResource("request-id", func(ec *flux.ExecContext) (string, error) {
	return fmt.Sprintf("req-%d", reqCounter.Add(1)), nil
})

var reqLogger = flux.NewResourceFrom2(reqID, logger, "req-logger", func(ec *flux.ExecContext, id string, base *slog.Logger) (*slog.Logger, error) {
	return base.With("request_id", id), nil
})
