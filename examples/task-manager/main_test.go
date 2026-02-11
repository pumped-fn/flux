package main

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/pumped-fn/flux"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateDB(d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newTestScope(t *testing.T, opts ...flux.ScopeOption) flux.Scope {
	t.Helper()
	s := flux.NewScope(context.Background(), opts...)
	if err := s.Ready(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Dispose() })
	return s
}

// execFlow is a test helper that creates a context, runs a flow, and closes it.
func execFlow[In, Out any](t *testing.T, s flux.Scope, f *flux.Flow[In, Out], input In) (Out, error) {
	t.Helper()
	ec := s.CreateContext()
	out, err := flux.ExecFlow(ec, f, input)
	ec.Close(err)
	return out, err
}

func TestCreateTask(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	task, err := execFlow(t, s, createTask, CreateTaskInput{
		Title:       "Buy groceries",
		Description: "Milk, eggs, bread",
		Priority:    TaskPriorityHigh,
	})
	if err != nil {
		t.Fatal(err)
	}

	if task.Title != "Buy groceries" {
		t.Errorf("title = %q, want %q", task.Title, "Buy groceries")
	}
	if task.Priority != TaskPriorityHigh {
		t.Errorf("priority = %q, want %q", task.Priority, TaskPriorityHigh)
	}
	if task.Status != TaskStatusPending {
		t.Errorf("status = %q, want %q", task.Status, TaskStatusPending)
	}
	if task.ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestCreateTaskDefaultPriority(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	task, err := execFlow(t, s, createTask, CreateTaskInput{
		Title: "No priority specified",
	})
	if err != nil {
		t.Fatal(err)
	}

	if task.Priority != TaskPriorityMedium {
		t.Errorf("priority = %q, want default %q", task.Priority, TaskPriorityMedium)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	_, err := execFlow(t, s, createTask, CreateTaskInput{Title: ""})
	if err == nil {
		t.Fatal("expected validation error for empty title")
	}

	var pe *flux.ParseError
	if !errors.As(err, &pe) {
		t.Errorf("expected ParseError, got %T: %v", err, err)
	}
}

func TestListTasks(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	// Empty list initially.
	tasks, err := execFlow(t, s, listTasks, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}

	// Create two tasks with different priorities.
	for _, p := range []TaskPriority{TaskPriorityHigh, TaskPriorityLow} {
		if _, err := execFlow(t, s, createTask, CreateTaskInput{Title: "task-" + string(p), Priority: p}); err != nil {
			t.Fatal(err)
		}
	}

	// List all.
	tasks, err = execFlow(t, s, listTasks, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}

	// Filter by priority.
	high := TaskPriorityHigh
	tasks, err = execFlow(t, s, listTasks, ListFilter{Priority: &high})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 high-priority task, got %d", len(tasks))
	}
	if tasks[0].Priority != TaskPriorityHigh {
		t.Errorf("priority = %q, want %q", tasks[0].Priority, TaskPriorityHigh)
	}
}

func TestGetTask(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	created, err := execFlow(t, s, createTask, CreateTaskInput{Title: "findable"})
	if err != nil {
		t.Fatal(err)
	}

	found, err := execFlow(t, s, getTask, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != created.ID || found.Title != "findable" {
		t.Errorf("got task %+v, want ID=%d title=%q", found, created.ID, "findable")
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	_, err := execFlow(t, s, getTask, int64(9999))
	if err == nil {
		t.Fatal("expected error for non-existent task")
	}
}

func TestUpdateTask(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	created, err := execFlow(t, s, createTask, CreateTaskInput{Title: "original", Priority: TaskPriorityLow})
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "updated"
	done := TaskStatusDone
	updated, err := execFlow(t, s, updateTask, UpdateTaskRequest{
		ID:    created.ID,
		Input: UpdateTaskInput{Title: &newTitle, Status: &done},
	})
	if err != nil {
		t.Fatal(err)
	}

	if updated.Title != "updated" {
		t.Errorf("title = %q, want %q", updated.Title, "updated")
	}
	if updated.Status != TaskStatusDone {
		t.Errorf("status = %q, want %q", updated.Status, TaskStatusDone)
	}
	// Unchanged fields preserved.
	if updated.Priority != TaskPriorityLow {
		t.Errorf("priority = %q, want %q (unchanged)", updated.Priority, TaskPriorityLow)
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	newTitle := "nope"
	_, err := execFlow(t, s, updateTask, UpdateTaskRequest{
		ID:    9999,
		Input: UpdateTaskInput{Title: &newTitle},
	})
	if err == nil {
		t.Fatal("expected error when updating non-existent task")
	}
}

func TestDeleteTask(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	created, err := execFlow(t, s, createTask, CreateTaskInput{Title: "ephemeral"})
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := execFlow(t, s, deleteTask, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Error("expected deleted = true")
	}

	// Verify it's gone.
	_, err = execFlow(t, s, getTask, created.ID)
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestDeleteTaskNotFound(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	deleted, err := execFlow(t, s, deleteTask, int64(9999))
	if err != nil {
		t.Fatal(err)
	}
	if deleted {
		t.Error("expected deleted = false for non-existent task")
	}
}

func TestTransactionRollback(t *testing.T) {
	testDB := newTestDB(t)
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, testDB)))

	// Create a task so we have data.
	created, err := execFlow(t, s, createTask, CreateTaskInput{Title: "survivor"})
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to update a non-existent task — the flow errors, so the
	// transaction should rollback (no partial writes).
	newTitle := "ghost"
	_, _ = execFlow(t, s, updateTask, UpdateTaskRequest{
		ID:    9999,
		Input: UpdateTaskInput{Title: &newTitle},
	})

	// Original task should be untouched.
	found, err := execFlow(t, s, getTask, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Title != "survivor" {
		t.Errorf("title = %q, want %q (rollback should preserve original)", found.Title, "survivor")
	}
}

func TestTaskStats(t *testing.T) {
	testDB := newTestDB(t)
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, testDB)))

	// Seed some tasks directly.
	for _, st := range []TaskStatus{TaskStatusPending, TaskStatusPending, TaskStatusDone} {
		if _, err := execFlow(t, s, createTask, CreateTaskInput{Title: "t", Priority: TaskPriorityMedium}); err != nil {
			t.Fatal(err)
		}
		if st == TaskStatusDone {
			// Update the last one to done.
			done := TaskStatusDone
			tasks, _ := execFlow(t, s, listTasks, ListFilter{})
			last := tasks[0] // most recent (ORDER BY created_at DESC)
			if _, err := execFlow(t, s, updateTask, UpdateTaskRequest{
				ID: last.ID, Input: UpdateTaskInput{Status: &done},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Invalidate and flush so the atom re-resolves.
	flux.GetController(s, taskStats).Invalidate()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	stats, err := flux.Resolve(s, taskStats)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Total != 3 {
		t.Errorf("total = %d, want 3", stats.Total)
	}
	if stats.ByStatus[TaskStatusPending] != 2 {
		t.Errorf("pending = %d, want 2", stats.ByStatus[TaskStatusPending])
	}
	if stats.ByStatus[TaskStatusDone] != 1 {
		t.Errorf("done = %d, want 1", stats.ByStatus[TaskStatusDone])
	}
}

func TestStatsControllerInvalidation(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	// Initial resolve — 0 tasks.
	stats, err := flux.Resolve(s, taskStats)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 0 {
		t.Fatalf("initial total = %d, want 0", stats.Total)
	}

	// Create a task.
	if _, err := execFlow(t, s, createTask, CreateTaskInput{Title: "new"}); err != nil {
		t.Fatal(err)
	}

	// Without invalidation, cached stats still show 0.
	cached, _ := flux.Resolve(s, taskStats)
	if cached.Total != 0 {
		t.Fatalf("cached total = %d, want 0 (stale)", cached.Total)
	}

	// Invalidate + flush triggers re-resolve.
	flux.GetController(s, taskStats).Invalidate()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	fresh, err := flux.Resolve(s, taskStats)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Total != 1 {
		t.Errorf("fresh total = %d, want 1", fresh.Total)
	}
}

func TestPresetFlowMock(t *testing.T) {
	// Replace the real createTask flow with a mock using PresetFlowFn.
	// No database needed — the mock returns a hardcoded task.
	s := newTestScope(t,
		flux.WithPresets(
			flux.Preset(db, newTestDB(t)),
			flux.PresetFlowFn(createTask, func(ec *flux.ExecContext) (*Task, error) {
				return &Task{
					ID:       42,
					Title:    "mocked",
					Status:   TaskStatusDone,
					Priority: TaskPriorityHigh,
				}, nil
			}),
		),
	)

	task, err := execFlow(t, s, createTask, CreateTaskInput{Title: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != 42 || task.Title != "mocked" {
		t.Errorf("got %+v, want mocked task with ID=42", task)
	}
}

func TestExtensionWrapExec(t *testing.T) {
	var execCount atomic.Int32

	ext := &countingExtension{count: &execCount}
	s := newTestScope(t,
		flux.WithPresets(flux.Preset(db, newTestDB(t))),
		flux.WithExtensions(ext),
	)

	if _, err := execFlow(t, s, createTask, CreateTaskInput{Title: "traced"}); err != nil {
		t.Fatal(err)
	}

	// createTask runs ExecFn("insert-task") inside, so at least 2 execs:
	// the flow itself + the inner ExecFn.
	if c := execCount.Load(); c < 2 {
		t.Errorf("exec count = %d, want >= 2", c)
	}
}

type countingExtension struct {
	count *atomic.Int32
}

func (e *countingExtension) Name() string { return "counting" }

func (e *countingExtension) WrapExec(next func() (any, error), _ flux.AnyExecTarget, _ *flux.ExecContext) (any, error) {
	e.count.Add(1)
	return next()
}

func TestSelectSubscription(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	// Initial resolve.
	if _, err := flux.Resolve(s, taskStats); err != nil {
		t.Fatal(err)
	}

	sel := flux.Select(s, taskStats, func(st *TaskStats) int { return st.Total })

	var notified atomic.Bool
	unsub := sel.Subscribe(func() { notified.Store(true) })
	defer unsub()

	if sel.Get() != 0 {
		t.Fatalf("initial select = %d, want 0", sel.Get())
	}

	// Add a task + invalidate.
	if _, err := execFlow(t, s, createTask, CreateTaskInput{Title: "trigger"}); err != nil {
		t.Fatal(err)
	}
	flux.GetController(s, taskStats).Invalidate()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	if sel.Get() != 1 {
		t.Errorf("select after insert = %d, want 1", sel.Get())
	}
	if !notified.Load() {
		t.Error("expected subscription to fire after invalidation")
	}
}

func TestResourcePerContext(t *testing.T) {
	s := newTestScope(t, flux.WithPresets(flux.Preset(db, newTestDB(t))))

	// Each ExecContext gets its own dbtx resource instance.
	// Create a task in ec1, close it (commits), then verify in ec2.
	ec1 := s.CreateContext()
	_, err := flux.ExecFlow(ec1, createTask, CreateTaskInput{Title: "from-ec1"})
	ec1.Close(err)
	if err != nil {
		t.Fatal(err)
	}

	// ec2 is a fresh context with its own transaction.
	tasks, err := execFlow(t, s, listTasks, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Title != "from-ec1" {
		t.Errorf("expected 1 task titled %q, got %v", "from-ec1", tasks)
	}
}
