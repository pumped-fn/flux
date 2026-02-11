package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pumped-fn/flux"
)

type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusDone       TaskStatus = "done"
)

type TaskPriority string

const (
	TaskPriorityLow    TaskPriority = "low"
	TaskPriorityMedium TaskPriority = "medium"
	TaskPriorityHigh   TaskPriority = "high"
)

type Task struct {
	ID          int64        `json:"id"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Status      TaskStatus   `json:"status"`
	Priority    TaskPriority `json:"priority"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

type CreateTaskInput struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Priority    TaskPriority `json:"priority"`
}

type UpdateTaskInput struct {
	Title       *string       `json:"title,omitempty"`
	Description *string       `json:"description,omitempty"`
	Status      *TaskStatus   `json:"status,omitempty"`
	Priority    *TaskPriority `json:"priority,omitempty"`
}

type UpdateTaskRequest struct {
	ID    int64
	Input UpdateTaskInput
}

type ListFilter struct {
	Status   *TaskStatus   `json:"status,omitempty"`
	Priority *TaskPriority `json:"priority,omitempty"`
}

var createTask = flux.NewFlowFrom2(dbtx, reqLogger,
	func(ec *flux.ExecContext, input CreateTaskInput, tx DBTX, log *slog.Logger) (*Task, error) {
		log.Info("creating task", "title", input.Title, "priority", input.Priority)

		task, err := flux.ExecFn(ec, func(child *flux.ExecContext) (*Task, error) {
			if input.Priority == "" {
				input.Priority = TaskPriorityMedium
			}
			result, err := tx.ExecContext(child.Context(),
				"INSERT INTO tasks (title, description, priority) VALUES (?, ?, ?)",
				input.Title, input.Description, input.Priority)
			if err != nil {
				return nil, err
			}
			id, err := result.LastInsertId()
			if err != nil {
				return nil, err
			}
			return scanTask(tx.QueryRowContext(child.Context(),
				"SELECT "+taskColumns+" FROM tasks WHERE id = ?", id))
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

var listTasks = flux.NewFlowFrom2(dbtx, reqLogger,
	func(ec *flux.ExecContext, filter ListFilter, tx DBTX, log *slog.Logger) ([]*Task, error) {
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

			rows, err := tx.QueryContext(child.Context(), query, args...)
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

var getTask = flux.NewFlowFrom2(dbtx, reqLogger,
	func(ec *flux.ExecContext, id int64, tx DBTX, log *slog.Logger) (*Task, error) {
		log.Debug("getting task", "id", id)

		return flux.ExecFn(ec, func(child *flux.ExecContext) (*Task, error) {
			row := tx.QueryRowContext(child.Context(),
				"SELECT "+taskColumns+" FROM tasks WHERE id = ?", id)
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

var updateTask = flux.NewFlowFrom2(dbtx, reqLogger,
	func(ec *flux.ExecContext, req UpdateTaskRequest, tx DBTX, log *slog.Logger) (*Task, error) {
		// getTask runs in a child ExecContext — it inherits dbtx
		// from this parent via SeekTag parent chain (same transaction!)
		_, err := flux.ExecFlow(ec, getTask, req.ID, flux.WithExecName("verify-task-exists"))
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
				return scanTask(tx.QueryRowContext(child.Context(),
					"SELECT "+taskColumns+" FROM tasks WHERE id = ?", req.ID))
			}

			setClauses = append(setClauses, "updated_at = datetime('now')")
			args = append(args, req.ID)

			_, err := tx.ExecContext(child.Context(),
				fmt.Sprintf("UPDATE tasks SET %s WHERE id = ?", strings.Join(setClauses, ", ")),
				args...)
			if err != nil {
				return nil, err
			}

			return scanTask(tx.QueryRowContext(child.Context(),
				"SELECT "+taskColumns+" FROM tasks WHERE id = ?", req.ID))
		}, flux.WithExecName("exec-update"))
		if err != nil {
			return nil, err
		}

		log.Info("task updated", "id", req.ID)
		return updated, nil
	},
	flux.WithFlowName("update-task"),
)

var deleteTask = flux.NewFlowFrom2(dbtx, reqLogger,
	func(ec *flux.ExecContext, id int64, tx DBTX, log *slog.Logger) (bool, error) {
		log.Info("deleting task", "id", id)

		deleted, err := flux.ExecFn(ec, func(child *flux.ExecContext) (bool, error) {
			result, err := tx.ExecContext(child.Context(), "DELETE FROM tasks WHERE id = ?", id)
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

func parseTaskID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid task id")
	}
	return id, nil
}

func handleCreateTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input CreateTaskInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ec := scope.CreateContext()
		task, err := flux.ExecFlow(ec, createTask, input)
		ec.Close(err)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		flux.GetController(scope, taskStats).Invalidate()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(task)
	}
}

func handleListTasks(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var filter ListFilter
		if s := r.URL.Query().Get("status"); s != "" {
			status := TaskStatus(s)
			filter.Status = &status
		}
		if p := r.URL.Query().Get("priority"); p != "" {
			priority := TaskPriority(p)
			filter.Priority = &priority
		}

		ec := scope.CreateContext()
		tasks, err := flux.ExecFlow(ec, listTasks, filter)
		ec.Close(err)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		if tasks == nil {
			tasks = []*Task{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tasks)
	}
}

func handleGetTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseTaskID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ec := scope.CreateContext()
		task, err := flux.ExecFlow(ec, getTask, id)
		ec.Close(err)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}
}

func handleUpdateTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseTaskID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var input UpdateTaskInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ec := scope.CreateContext()
		task, err := flux.ExecFlow(ec, updateTask, UpdateTaskRequest{ID: id, Input: input})
		ec.Close(err)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		flux.GetController(scope, taskStats).Invalidate()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(task)
	}
}

func handleDeleteTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseTaskID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		ec := scope.CreateContext()
		deleted, err := flux.ExecFlow(ec, deleteTask, id)
		ec.Close(err)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		if !deleted {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		flux.GetController(scope, taskStats).Invalidate()
		w.WriteHeader(http.StatusNoContent)
	}
}
