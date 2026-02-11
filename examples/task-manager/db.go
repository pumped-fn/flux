package main

import (
	"context"
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrateDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			title       TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'pending',
			priority    TEXT NOT NULL DEFAULT 'medium',
			created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
			updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
		)
	`)
	return err
}

func seedDB(db *sql.DB) error {
	var count int
	if err := db.QueryRow("SELECT count(*) FROM tasks").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	seeds := []struct {
		title    string
		desc     string
		status   TaskStatus
		priority TaskPriority
	}{
		{"Set up CI pipeline", "Configure GitHub Actions for the project", TaskStatusDone, TaskPriorityHigh},
		{"Write API documentation", "Document all REST endpoints", TaskStatusInProgress, TaskPriorityMedium},
		{"Add input validation", "Validate all user-facing inputs", TaskStatusPending, TaskPriorityHigh},
		{"Refactor database layer", "Extract common query patterns", TaskStatusPending, TaskPriorityLow},
		{"Add rate limiting", "Protect API from abuse", TaskStatusPending, TaskPriorityMedium},
	}

	for _, s := range seeds {
		_, err := db.Exec(
			"INSERT INTO tasks (title, description, status, priority) VALUES (?, ?, ?, ?)",
			s.title, s.desc, s.status, s.priority,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

const taskColumns = "id, title, description, status, priority, created_at, updated_at"

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var createdAt, updatedAt string
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Priority, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	return &t, nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
