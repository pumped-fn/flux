package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/pumped-fn/flux"
	_ "modernc.org/sqlite"
)

var (
	dbPathTag = flux.NewTagWithDefault("db-path", "tasks.db")
	dbPathDep = flux.Required(dbPathTag)
)

var db = flux.NewAtomFrom(dbPathDep, func(rc *flux.ResolveContext, path string) (*sql.DB, error) {
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

var dbtx = flux.NewResourceFrom(db, "db-tx", func(ec *flux.ExecContext, pool *sql.DB) (DBTX, error) {
	// Use pool.Begin() (not BeginTx) because ExecContext.Close cancels the
	// context before running OnClose callbacks. sql.Tx auto-rolls back on
	// context cancellation, so BeginTx would conflict with our explicit
	// commit/rollback in OnClose.
	tx, err := pool.Begin()
	if err != nil {
		return nil, err
	}
	ec.OnClose(func(execErr error) error {
		if execErr != nil {
			return tx.Rollback()
		}
		return tx.Commit()
	})
	return tx, nil
})

type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func openDB(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := d.Exec("PRAGMA journal_mode=WAL"); err != nil {
		d.Close()
		return nil, err
	}
	if _, err := d.Exec("PRAGMA foreign_keys=ON"); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func migrateDB(d *sql.DB) error {
	_, err := d.Exec(`
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

func seedDB(d *sql.DB) error {
	var count int
	if err := d.QueryRow("SELECT count(*) FROM tasks").Scan(&count); err != nil {
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
		_, err := d.Exec(
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
