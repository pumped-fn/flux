package main

import (
	"log/slog"

	"github.com/pumped-fn/flux"
)

func setupStats(scope flux.Scope, log *slog.Logger) (func(), error) {
	_, err := flux.Resolve(scope, taskStatsAtom)
	if err != nil {
		return func() {}, err
	}

	totalSelect := flux.Select(scope, taskStatsAtom, func(s *TaskStats) int {
		return s.Total
	})

	unsub := totalSelect.Subscribe(func() {
		log.Info("task stats updated", "total", totalSelect.Get())
	})

	return unsub, nil
}
