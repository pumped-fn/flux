package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/pumped-fn/flux"
)

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
