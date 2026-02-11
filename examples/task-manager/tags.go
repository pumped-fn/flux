package main

import (
	"log/slog"

	"github.com/pumped-fn/flux"
)

var (
	dbPathTag   = flux.NewTagWithDefault("db-path", "tasks.db")
	logLevelTag = flux.NewTagWithDefault("log-level", "info")
)

var (
	requestIDTag = flux.NewTag[string]("request-id")
	dbTxTag      = flux.NewTag[DBTX]("db-tx")
	reqLoggerTag = flux.NewTag[*slog.Logger]("req-logger")
)

var (
	dbPathDep   = flux.Required(dbPathTag)
	logLevelDep = flux.Required(logLevelTag)
	dbTxDep     = flux.Required(dbTxTag)
	reqLoggerDep = flux.Required(reqLoggerTag)
)
