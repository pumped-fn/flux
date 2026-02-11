package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pumped-fn/flux"
)

var createTaskFlow = flux.NewFlowFrom2(dbTxDep, reqLoggerDep,
	func(ec *flux.ExecContext, input CreateTaskInput, dbtx DBTX, log *slog.Logger) (*Task, error) {
		log.Info("creating task", "title", input.Title, "priority", input.Priority)

		task, err := flux.ExecFn(ec, func(child *flux.ExecContext) (*Task, error) {
			if input.Priority == "" {
				input.Priority = TaskPriorityMedium
			}
			result, err := dbtx.ExecContext(child.Context(),
				"INSERT INTO tasks (title, description, priority) VALUES (?, ?, ?)",
				input.Title, input.Description, input.Priority)
			if err != nil {
				return nil, err
			}
			id, err := result.LastInsertId()
			if err != nil {
				return nil, err
			}
			return scanTask(dbtx.QueryRowContext(child.Context(),
				"SELECT " + taskColumns + " FROM tasks WHERE id = ?", id))
		}, flux.WithExecName("insert-task"))
		if err != nil {
			return nil, err
		}

		log.Info("task created", "id", task.ID)
		return task, nil
	},
	flux.WithFlowName("create-task"),
	flux.WithParse(func(raw any) (CreateTaskInput, error) {
		input := raw.(CreateTaskInput)
		if input.Title == "" {
			return input, fmt.Errorf("title is required")
		}
		return input, nil
	}),
)

var listTasksFlow = flux.NewFlowFrom2(dbTxDep, reqLoggerDep,
	func(ec *flux.ExecContext, filter ListFilter, dbtx DBTX, log *slog.Logger) ([]*Task, error) {
		log.Debug("listing tasks", "filter", filter)

		return flux.ExecFn(ec, func(child *flux.ExecContext) ([]*Task, error) {
			query := "SELECT " + taskColumns + " FROM tasks"
			var conditions []string
			var args []any

			if filter.Status != nil {
				conditions = append(conditions, "status = ?")
				args = append(args, *filter.Status)
			}
			if filter.Priority != nil {
				conditions = append(conditions, "priority = ?")
				args = append(args, *filter.Priority)
			}
			if len(conditions) > 0 {
				query += " WHERE " + strings.Join(conditions, " AND ")
			}
			query += " ORDER BY created_at DESC"

			rows, err := dbtx.QueryContext(child.Context(), query, args...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()

			var tasks []*Task
			for rows.Next() {
				t, err := scanTask(rows)
				if err != nil {
					return nil, err
				}
				tasks = append(tasks, t)
			}
			return tasks, rows.Err()
		}, flux.WithExecName("query-tasks"))
	},
	flux.WithFlowName("list-tasks"),
)

var getTaskFlow = flux.NewFlowFrom2(dbTxDep, reqLoggerDep,
	func(ec *flux.ExecContext, id int64, dbtx DBTX, log *slog.Logger) (*Task, error) {
		log.Debug("getting task", "id", id)

		return flux.ExecFn(ec, func(child *flux.ExecContext) (*Task, error) {
			row := dbtx.QueryRowContext(child.Context(),
				"SELECT " + taskColumns + " FROM tasks WHERE id = ?", id)
			t, err := scanTask(row)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("task %d not found: %w", id, err)
				}
				return nil, fmt.Errorf("query task %d: %w", id, err)
			}
			return t, nil
		}, flux.WithExecName("query-task"))
	},
	flux.WithFlowName("get-task"),
)

var updateTaskFlow = flux.NewFlowFrom2(dbTxDep, reqLoggerDep,
	func(ec *flux.ExecContext, req UpdateTaskRequest, dbtx DBTX, log *slog.Logger) (*Task, error) {
		_, err := flux.ExecFlow(ec, getTaskFlow, req.ID, flux.WithExecName("verify-task-exists"))
		if err != nil {
			return nil, err
		}

		log.Info("updating task", "id", req.ID)

		updated, err := flux.ExecFn(ec, func(child *flux.ExecContext) (*Task, error) {
			var setClauses []string
			var args []any

			if req.Input.Title != nil {
				setClauses = append(setClauses, "title = ?")
				args = append(args, *req.Input.Title)
			}
			if req.Input.Description != nil {
				setClauses = append(setClauses, "description = ?")
				args = append(args, *req.Input.Description)
			}
			if req.Input.Status != nil {
				setClauses = append(setClauses, "status = ?")
				args = append(args, *req.Input.Status)
			}
			if req.Input.Priority != nil {
				setClauses = append(setClauses, "priority = ?")
				args = append(args, *req.Input.Priority)
			}

			if len(setClauses) == 0 {
				return scanTask(dbtx.QueryRowContext(child.Context(),
					"SELECT " + taskColumns + " FROM tasks WHERE id = ?", req.ID))
			}

			setClauses = append(setClauses, "updated_at = datetime('now')")
			args = append(args, req.ID)

			_, err := dbtx.ExecContext(child.Context(),
				fmt.Sprintf("UPDATE tasks SET %s WHERE id = ?", strings.Join(setClauses, ", ")),
				args...)
			if err != nil {
				return nil, err
			}

			return scanTask(dbtx.QueryRowContext(child.Context(),
				"SELECT " + taskColumns + " FROM tasks WHERE id = ?", req.ID))
		}, flux.WithExecName("exec-update"))
		if err != nil {
			return nil, err
		}

		log.Info("task updated", "id", req.ID)
		return updated, nil
	},
	flux.WithFlowName("update-task"),
)

var deleteTaskFlow = flux.NewFlowFrom2(dbTxDep, reqLoggerDep,
	func(ec *flux.ExecContext, id int64, dbtx DBTX, log *slog.Logger) (bool, error) {
		log.Info("deleting task", "id", id)

		deleted, err := flux.ExecFn(ec, func(child *flux.ExecContext) (bool, error) {
			result, err := dbtx.ExecContext(child.Context(), "DELETE FROM tasks WHERE id = ?", id)
			if err != nil {
				return false, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return false, err
			}
			return n > 0, nil
		}, flux.WithExecName("delete-task"))
		if err != nil {
			return false, err
		}

		log.Info("task deleted", "id", id, "found", deleted)
		return deleted, nil
	},
	flux.WithFlowName("delete-task"),
)
