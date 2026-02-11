package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/pumped-fn/flux"
)

var reqCounter atomic.Uint64

func newHandler(scope flux.Scope) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /tasks", handleCreateTask(scope))
	mux.HandleFunc("GET /tasks", handleListTasks(scope))
	mux.HandleFunc("GET /tasks/{id}", handleGetTask(scope))
	mux.HandleFunc("PATCH /tasks/{id}", handleUpdateTask(scope))
	mux.HandleFunc("DELETE /tasks/{id}", handleDeleteTask(scope))
	mux.HandleFunc("GET /stats", handleStats(scope))

	return mux
}

func withAmbient(scope flux.Scope, r *http.Request, mutating bool, fn func(ec *flux.ExecContext) error) error {
	reqID := fmt.Sprintf("req-%d", reqCounter.Add(1))
	db := flux.MustResolve(scope, dbAtom)
	baseLogger := flux.MustResolve(scope, loggerAtom)
	reqLogger := baseLogger.With("request_id", reqID)

	if mutating {
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			return err
		}

		ec := scope.CreateContext(flux.WithContextTags(
			requestIDTag.Value(reqID),
			dbTxTag.Value(tx),
			reqLoggerTag.Value(reqLogger),
		))

		execErr := fn(ec)

		if execErr != nil {
			rbErr := tx.Rollback()
			closeErr := ec.Close(execErr)
			return errors.Join(execErr, rbErr, closeErr)
		}

		if err := tx.Commit(); err != nil {
			tx.Rollback()
			ec.Close(err)
			return err
		}
		ec.Close(nil)
		flux.GetController(scope, taskStatsAtom).Invalidate()
		return nil
	}

	ec := scope.CreateContext(flux.WithContextTags(
		requestIDTag.Value(reqID),
		dbTxTag.Value(db),
		reqLoggerTag.Value(reqLogger),
	))

	execErr := fn(ec)
	ec.Close(execErr)
	return execErr
}

func writeError(w http.ResponseWriter, err error, fallbackStatus int) {
	var pe *flux.ParseError
	if errors.As(err, &pe) {
		http.Error(w, pe.Error(), http.StatusBadRequest)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), fallbackStatus)
}

func handleCreateTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input CreateTaskInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var task *Task
		err := withAmbient(scope, r, true, func(ec *flux.ExecContext) error {
			var execErr error
			task, execErr = flux.ExecFlow(ec, createTaskFlow, input)
			return execErr
		})
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(task)
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

		var tasks []*Task
		err := withAmbient(scope, r, false, func(ec *flux.ExecContext) error {
			var execErr error
			tasks, execErr = flux.ExecFlow(ec, listTasksFlow, filter)
			return execErr
		})
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		if tasks == nil {
			tasks = []*Task{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tasks)
	}
}

func parseTaskID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid task id")
	}
	return id, nil
}

func handleGetTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseTaskID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var task *Task
		err = withAmbient(scope, r, false, func(ec *flux.ExecContext) error {
			var execErr error
			task, execErr = flux.ExecFlow(ec, getTaskFlow, id)
			return execErr
		})
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task)
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

		var task *Task
		err = withAmbient(scope, r, true, func(ec *flux.ExecContext) error {
			var execErr error
			task, execErr = flux.ExecFlow(ec, updateTaskFlow, UpdateTaskRequest{ID: id, Input: input})
			return execErr
		})
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task)
	}
}

func handleDeleteTask(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseTaskID(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var deleted bool
		err = withAmbient(scope, r, true, func(ec *flux.ExecContext) error {
			var execErr error
			deleted, execErr = flux.ExecFlow(ec, deleteTaskFlow, id)
			return execErr
		})
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}

		if !deleted {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleStats(scope flux.Scope) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := scope.Flush(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats, err := flux.Resolve(scope, taskStatsAtom)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	}
}

func resolveDB(scope flux.Scope) *sql.DB {
	return flux.MustResolve(scope, dbAtom)
}
