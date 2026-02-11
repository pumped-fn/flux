package main

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/pumped-fn/flux"
)

type TaskStats struct {
	Total      int                  `json:"total"`
	ByStatus   map[TaskStatus]int   `json:"by_status"`
	ByPriority map[TaskPriority]int `json:"by_priority"`
}

var taskStats = flux.NewAtomFrom(db, func(rc *flux.ResolveContext, pool *sql.DB) (*TaskStats, error) {
	tx, err := pool.BeginTx(rc.Context(), nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	stats := &TaskStats{
		ByStatus:   make(map[TaskStatus]int),
		ByPriority: make(map[TaskPriority]int),
	}

	rows, err := tx.QueryContext(rc.Context(), "SELECT status, count(*) FROM tasks GROUP BY status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status TaskStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats.ByStatus[status] = count
		stats.Total += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows2, err := tx.QueryContext(rc.Context(), "SELECT priority, count(*) FROM tasks GROUP BY priority")
	if err != nil {
		return nil, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var priority TaskPriority
		var count int
		if err := rows2.Scan(&priority, &count); err != nil {
			return nil, err
		}
		stats.ByPriority[priority] = count
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	return stats, nil
}, flux.WithAtomName("task-stats"))

func setupStats(scope flux.Scope, log *slog.Logger) (func(), error) {
	_, err := flux.Resolve(scope, taskStats)
	if err != nil {
		return func() {}, err
	}

	totalSelect := flux.Select(scope, taskStats, func(s *TaskStats) int {
		return s.Total
	})

	unsub := totalSelect.Subscribe(func() {
		log.Info("task stats updated", "total", totalSelect.Get())
	})

	return unsub, nil
}

func handleStats(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := scope.Flush(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats, err := flux.Resolve(scope, taskStats)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	}
}
