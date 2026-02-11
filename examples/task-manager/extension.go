package main

import (
	"log/slog"
	"time"

	"github.com/pumped-fn/flux"
)

type RequestTimingExtension struct {
	log *slog.Logger
}

func (e *RequestTimingExtension) Name() string { return "request-timing" }

func (e *RequestTimingExtension) Init(s flux.Scope) error {
	e.log.Info("extension initialized", "name", e.Name())
	return nil
}

func (e *RequestTimingExtension) WrapExec(next func() (any, error), target flux.AnyExecTarget, ec *flux.ExecContext) (any, error) {
	start := time.Now()
	val, err := next()
	duration := time.Since(start)

	if err != nil {
		e.log.Debug("exec completed",
			"name", target.ExecTargetName(),
			"duration", duration,
			"error", err)
	} else {
		e.log.Debug("exec completed",
			"name", target.ExecTargetName(),
			"duration", duration)
	}
	return val, err
}

func (e *RequestTimingExtension) Dispose(s flux.Scope) error {
	e.log.Info("extension disposed", "name", e.Name())
	return nil
}
