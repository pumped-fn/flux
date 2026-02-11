package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pumped-fn/flux"
)

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "run":
		os.Exit(cmdRun())
	case "serve":
		addr := ":8080"
		if len(os.Args) > 2 {
			addr = os.Args[2]
		}
		os.Exit(cmdServe(addr))
	default:
		fmt.Fprintf(os.Stderr, "usage: %s <run|serve> [addr]\n", os.Args[0])
		os.Exit(1)
	}
}

func dbPath() string {
	if p := os.Getenv("FLUX_DB_PATH"); p != "" {
		return p
	}
	for i, arg := range os.Args {
		if arg == "--db" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return "tasks.db"
}

func logLevel() string {
	if l := os.Getenv("LOG_LEVEL"); l != "" {
		return l
	}
	return "debug"
}

func newScope(ctx context.Context, baseLog *slog.Logger) (flux.Scope, error) {
	scope := flux.NewScope(ctx,
		flux.WithExtensions(&RequestTimingExtension{log: baseLog}),
		flux.WithScopeTags(
			dbPathTag.Value(dbPath()),
			logLevelTag.Value(logLevel()),
		),
	)
	if err := scope.Ready(); err != nil {
		return nil, err
	}
	return scope, nil
}

func cmdRun() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	baseLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	scope, err := newScope(ctx, baseLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scope init failed: %v\n", err)
		return 1
	}
	defer func() { _ = scope.Dispose() }()

	db := flux.MustResolve(scope, db)
	if err := seedDB(db); err != nil {
		fmt.Fprintf(os.Stderr, "seed failed: %v\n", err)
		return 1
	}

	unsubStats, err := setupStats(scope, baseLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats setup failed: %v\n", err)
		return 1
	}
	defer unsubStats()

	printDependencyGraph(os.Stdout)

	fmt.Println("\n=== Create Task ===")
	ec := scope.CreateContext()
	task, err := flux.ExecFlow(ec, createTask, CreateTaskInput{
		Title:       "Demo task from CLI",
		Description: "Created by flux-example run command",
		Priority:    TaskPriorityHigh,
	})
	ec.Close(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create failed: %v\n", err)
		return 1
	}
	flux.GetController(scope, taskStats).Invalidate()
	fmt.Printf("  Created: #%d %q [%s/%s]\n", task.ID, task.Title, task.Status, task.Priority)

	fmt.Println("\n=== List Tasks ===")
	ec = scope.CreateContext()
	tasks, err := flux.ExecFlow(ec, listTasks, ListFilter{})
	ec.Close(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list failed: %v\n", err)
		return 1
	}
	for _, t := range tasks {
		fmt.Printf("  #%d %q [%s/%s]\n", t.ID, t.Title, t.Status, t.Priority)
	}

	fmt.Println("\n=== Update Task ===")
	done := TaskStatusDone
	ec = scope.CreateContext()
	updated, err := flux.ExecFlow(ec, updateTask, UpdateTaskRequest{
		ID:    task.ID,
		Input: UpdateTaskInput{Status: &done},
	})
	ec.Close(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "update failed: %v\n", err)
		return 1
	}
	flux.GetController(scope, taskStats).Invalidate()
	fmt.Printf("  Updated: #%d %q [%s/%s]\n", updated.ID, updated.Title, updated.Status, updated.Priority)

	fmt.Println("\n=== Delete Task ===")
	ec = scope.CreateContext()
	deleted, err := flux.ExecFlow(ec, deleteTask, task.ID)
	ec.Close(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "delete failed: %v\n", err)
		return 1
	}
	flux.GetController(scope, taskStats).Invalidate()
	fmt.Printf("  Deleted: #%d → %v\n", task.ID, deleted)

	fmt.Println("\n=== Stats ===")
	if err := scope.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "flush failed: %v\n", err)
	}
	stats, err := flux.Resolve(scope, taskStats)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats failed: %v\n", err)
		return 1
	}
	fmt.Printf("  Total: %d\n", stats.Total)
	fmt.Printf("  By Status: %v\n", stats.ByStatus)
	fmt.Printf("  By Priority: %v\n", stats.ByPriority)

	return 0
}

func cmdServe(addr string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	baseLog := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	scope, err := newScope(ctx, baseLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scope init failed: %v\n", err)
		return 1
	}

	unsubStats, err := setupStats(scope, baseLog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stats setup failed: %v\n", err)
		_ = scope.Dispose()
		return 1
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: newHandler(scope),
	}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	fmt.Printf("listening on %s\n", addr)
	printDependencyGraph(os.Stdout)

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		unsubStats()
		_ = scope.Dispose()
		return 1
	}

	unsubStats()
	_ = scope.Dispose()
	return 0
}

type graphNode struct {
	kind string
	name string
	deps []string
}

func buildGraph() []graphNode {
	atoms := []flux.AnyAtom{db, logger, taskStats}
	flows := []flux.AnyFlow{
		createTask, listTasks, getTask,
		updateTask, deleteTask,
	}

	var nodes []graphNode
	for _, a := range atoms {
		deps := a.Deps()
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = d.Name()
		}
		nodes = append(nodes, graphNode{kind: "atom", name: a.Name(), deps: names})
	}
	for _, f := range flows {
		deps := f.Deps()
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = d.Name()
		}
		// Resources appear as static deps alongside atoms in the dependency graph
		nodes = append(nodes, graphNode{kind: "flow", name: f.Name(), deps: names})
	}
	return nodes
}

func printDependencyGraph(w io.Writer) {
	fmt.Fprintln(w, "=== Dependency Graph ===")
	for _, n := range buildGraph() {
		if len(n.deps) == 0 {
			fmt.Fprintf(w, "  [%s] %-20s (no deps)\n", n.kind, n.name)
		} else {
			fmt.Fprintf(w, "  [%s] %-20s → [%s]\n", n.kind, n.name, strings.Join(n.deps, ", "))
		}
	}
}
