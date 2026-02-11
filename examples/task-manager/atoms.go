package main

import (
	"database/sql"
	"log/slog"
	"os"

	"github.com/pumped-fn/flux"
)

var dbAtom = flux.NewAtomFrom(dbPathDep, func(rc *flux.ResolveContext, path string) (*sql.DB, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := migrateDB(db); err != nil {
		db.Close()
		return nil, err
	}
	rc.Cleanup(func() error { return db.Close() })
	return db, nil
}, flux.WithAtomName("db"), flux.WithKeepAlive())

var loggerAtom = flux.NewAtomFrom(logLevelDep, func(rc *flux.ResolveContext, levelStr string) (*slog.Logger, error) {
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

var taskStatsAtom = flux.NewAtomFrom(dbAtom, func(rc *flux.ResolveContext, db *sql.DB) (*TaskStats, error) {
	tx, err := db.BeginTx(rc.Context(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

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
