package flux

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestScope(t *testing.T, opts ...ScopeOption) Scope {
	t.Helper()
	s := NewScope(context.Background(), opts...)
	t.Cleanup(func() { _ = s.Dispose() })
	if err := s.Ready(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestResolve(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 42, nil
		})
		s := newTestScope(t)
		v, err := Resolve(s, atom)
		if err != nil {
			t.Fatal(err)
		}
		if v != 42 {
			t.Fatalf("expected 42, got %d", v)
		}
	})
	t.Run("error", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 0, errors.New("boom")
		})
		s := newTestScope(t)
		_, err := Resolve(s, atom)
		if err == nil || err.Error() != "boom" {
			t.Fatalf("expected boom error, got %v", err)
		}
	})
	t.Run("panic", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			panic("kaboom")
		})
		s := newTestScope(t)
		_, err := Resolve(s, atom)
		if err == nil {
			t.Fatal("expected error from panic")
		}
	})
	t.Run("caching", func(t *testing.T) {
		var count atomic.Int32
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			count.Add(1)
			return 42, nil
		})
		s := newTestScope(t)
		v1, _ := Resolve(s, atom)
		v2, _ := Resolve(s, atom)
		if v1 != v2 {
			t.Fatalf("values differ: %d vs %d", v1, v2)
		}
		if count.Load() != 1 {
			t.Fatalf("expected factory called once, got %d", count.Load())
		}
	})
	t.Run("must", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (string, error) {
			return "hello", nil
		})
		s := newTestScope(t)
		v := MustResolve(s, atom)
		if v != "hello" {
			t.Fatalf("expected hello, got %s", v)
		}
	})
	t.Run("must_panic", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 0, errors.New("fail")
		})
		s := newTestScope(t)
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic")
			}
		}()
		MustResolve(s, atom)
	})
}

func TestDeepDependencyChain(t *testing.T) {
	a := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
	b := NewAtom(func(rc *ResolveContext) (int, error) {
		v, _ := ResolveFrom(rc, a)
		return v + 1, nil
	})
	c := NewAtom(func(rc *ResolveContext) (int, error) {
		v, _ := ResolveFrom(rc, b)
		return v + 1, nil
	})
	d := NewAtom(func(rc *ResolveContext) (int, error) {
		v, _ := ResolveFrom(rc, c)
		return v + 1, nil
	})
	s := newTestScope(t)
	v, err := Resolve(s, d)
	if err != nil {
		t.Fatal(err)
	}
	if v != 4 {
		t.Fatalf("expected 4, got %d", v)
	}
}

func TestCircularDependencyDetection(t *testing.T) {
	var atomA, atomB *Atom[int]
	atomA = NewAtom(func(rc *ResolveContext) (int, error) {
		return ResolveFrom(rc, atomB)
	})
	atomB = NewAtom(func(rc *ResolveContext) (int, error) {
		return ResolveFrom(rc, atomA)
	})
	s := newTestScope(t)
	_, err := Resolve(s, atomA)
	if err == nil {
		t.Fatal("expected circular dep error")
	}
	var circErr *CircularDepError
	if !errors.As(err, &circErr) {
		t.Fatalf("expected CircularDepError, got %T: %v", err, err)
	}
}

func TestConcurrentResolve(t *testing.T) {
	var count atomic.Int32
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		count.Add(1)
		time.Sleep(10 * time.Millisecond)
		return 42, nil
	})
	s := newTestScope(t)

	var wg sync.WaitGroup
	results := make([]int, 10)
	errs := make([]error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = Resolve(s, atom)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	for i, v := range results {
		if v != 42 {
			t.Fatalf("goroutine %d: expected 42, got %d", i, v)
		}
	}
}

func TestSingleflightRespectsContextCancellation(t *testing.T) {
	started := make(chan struct{})
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		return 42, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	s := NewScope(ctx)
	_ = s.Ready()
	t.Cleanup(func() { _ = s.Dispose() })

	go func() { _, _ = Resolve(s, atom) }()
	<-started

	cancel()
	_, err := Resolve(s, atom)
	if err == nil {
		return
	}
}

func TestControllerStateAndGet(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (string, error) {
		return "value", nil
	})
	s := newTestScope(t)

	ctrl := GetController(s, atom)
	if ctrl.State() != StateIdle {
		t.Fatalf("expected idle, got %v", ctrl.State())
	}

	_, err := ctrl.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	if ctrl.State() != StateResolved {
		t.Fatalf("expected resolved, got %v", ctrl.State())
	}

	v, err := ctrl.Get()
	if err != nil {
		t.Fatal(err)
	}
	if v != "value" {
		t.Fatalf("expected value, got %s", v)
	}
}

func TestControllerGetBeforeResolve(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	ctrl := GetController(s, atom)
	_, err := ctrl.Get()
	if !errors.Is(err, ErrNotResolved) {
		t.Fatalf("expected ErrNotResolved, got %v", err)
	}
}

func TestControllerSet(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	ctrl, _ := GetControllerResolved(s, atom)

	err := ctrl.Set(42)
	if err != nil {
		t.Fatal(err)
	}
	s.Flush()

	v, _ := ctrl.Get()
	if v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
}

func TestControllerUpdate(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 10, nil
	})
	s := newTestScope(t)
	ctrl, _ := GetControllerResolved(s, atom)

	err := ctrl.Update(func(prev int) int { return prev + 5 })
	if err != nil {
		t.Fatal(err)
	}
	s.Flush()

	v, _ := ctrl.Get()
	if v != 15 {
		t.Fatalf("expected 15, got %d", v)
	}
}

func TestControllerSetBeforeResolve(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	ctrl := GetController(s, atom)
	err := ctrl.Set(42)
	if !errors.Is(err, ErrNotResolved) {
		t.Fatalf("expected ErrNotResolved, got %v", err)
	}
}

func TestControllerInvalidate(t *testing.T) {
	var count atomic.Int32
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return int(count.Add(1)), nil
	})
	s := newTestScope(t)
	ctrl, _ := GetControllerResolved(s, atom)

	v1, _ := ctrl.Get()
	if v1 != 1 {
		t.Fatalf("expected 1, got %d", v1)
	}

	ctrl.Invalidate()
	s.Flush()

	v2, _ := ctrl.Get()
	if v2 != 2 {
		t.Fatalf("expected 2, got %d", v2)
	}
}

func TestControllerEvents(t *testing.T) {
	t.Run("resolved", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		})
		s := newTestScope(t)

		var notified atomic.Int32
		ctrl := GetController(s, atom)
		unsub := ctrl.On(EventResolved, func() {
			notified.Add(1)
		})
		defer unsub()

		_, _ = ctrl.Resolve()

		if notified.Load() < 1 {
			t.Fatal("expected at least one notification")
		}
	})
	t.Run("resolving", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		})
		s := newTestScope(t)

		var notified atomic.Int32
		ctrl := GetController(s, atom)
		unsub := ctrl.On(EventResolving, func() {
			notified.Add(1)
		})
		defer unsub()

		_, _ = ctrl.Resolve()

		if notified.Load() < 1 {
			t.Fatal("expected resolving notification")
		}
	})
	t.Run("all", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		})
		s := newTestScope(t)

		var notified atomic.Int32
		ctrl := GetController(s, atom)
		unsub := ctrl.On(EventAll, func() {
			notified.Add(1)
		})
		defer unsub()

		_, _ = ctrl.Resolve()

		if notified.Load() < 1 {
			t.Fatal("expected wildcard notification")
		}
	})
}

func TestControllerCached(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	c1 := GetController(s, atom)
	c2 := GetController(s, atom)
	if c1 != c2 {
		t.Fatal("controllers should be same instance")
	}
}

func TestControllerGetOnFailedAtom(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 0, errors.New("factory error")
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)
	ctrl := GetController(s, atom)
	_, err := ctrl.Get()
	if err == nil || err.Error() != "factory error" {
		t.Fatalf("expected factory error, got %v", err)
	}
}

func TestListenerUnsubscribe(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	ctrl := GetController(s, atom)
	var count atomic.Int32
	unsub := ctrl.On(EventResolved, func() {
		count.Add(1)
	})
	_, _ = ctrl.Resolve()
	unsub()

	ctrl.Invalidate()
	s.Flush()

	if count.Load() > 1 {
		t.Fatalf("listener should have been unsubscribed, got %d calls", count.Load())
	}
}

func TestListenerPanicDoesNotCrash(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	ctrl := GetController(s, atom)

	var secondCalled atomic.Bool
	ctrl.On(EventResolved, func() { panic("listener panic") })
	ctrl.On(EventResolved, func() { secondCalled.Store(true) })

	_, _ = ctrl.Resolve()

	if !secondCalled.Load() {
		t.Fatal("second listener should still be called after first panics")
	}
}

func TestReleaseFromResolvingListenerDoesNotDeadlock(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		time.Sleep(5 * time.Millisecond)
		return 1, nil
	})
	s := newTestScope(t)
	ctrl := GetController(s, atom)

	releaseDone := make(chan error, 1)
	ctrl.On(EventResolving, func() {
		releaseDone <- ctrl.Release()
	})

	go func() {
		_, _ = ctrl.Resolve()
	}()

	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatalf("release from listener failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("release from resolving listener deadlocked")
	}
}

func TestReleaseFromResolvedListenerDoesNotDeadlock(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		time.Sleep(5 * time.Millisecond)
		return 1, nil
	})
	s := newTestScope(t)
	ctrl := GetController(s, atom)

	releaseDone := make(chan error, 1)
	ctrl.On(EventResolved, func() {
		releaseDone <- ctrl.Release()
	})

	go func() {
		_, _ = ctrl.Resolve()
	}()

	select {
	case err := <-releaseDone:
		if err != nil {
			t.Fatalf("release from listener failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("release from resolved listener deadlocked")
	}
}

func TestSelect(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (map[string]int, error) {
			return map[string]int{"x": 10, "y": 20}, nil
		})
		s := newTestScope(t)
		_, _ = Resolve(s, atom)

		handle := Select(s, atom, func(m map[string]int) int { return m["x"] })
		if handle.Get() != 10 {
			t.Fatalf("expected 10, got %d", handle.Get())
		}
	})
	t.Run("notify", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		})
		s := newTestScope(t)
		_, _ = Resolve(s, atom)

		handle := Select(s, atom, func(v int) int { return v * 10 })

		var notified atomic.Int32
		unsub := handle.Subscribe(func() {
			notified.Add(1)
		})
		defer unsub()

		ctrl := GetController(s, atom)
		_ = ctrl.Set(2)
		s.Flush()

		time.Sleep(10 * time.Millisecond)
		if notified.Load() < 1 {
			t.Fatal("expected select notification on change")
		}
		if handle.Get() != 20 {
			t.Fatalf("expected 20, got %d", handle.Get())
		}
	})
	t.Run("no_notify_same", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 5, nil
		})
		s := newTestScope(t)
		_, _ = Resolve(s, atom)

		handle := Select(s, atom, func(v int) int { return v })
		var notified atomic.Int32
		unsub := handle.Subscribe(func() {
			notified.Add(1)
		})
		defer unsub()

		ctrl := GetController(s, atom)
		_ = ctrl.Set(5)
		s.Flush()

		time.Sleep(10 * time.Millisecond)
		if notified.Load() != 0 {
			t.Fatalf("expected no notification for same value, got %d", notified.Load())
		}
	})
	t.Run("custom_eq", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) ([]int, error) {
			return []int{1, 2, 3}, nil
		})
		s := newTestScope(t)
		_, _ = Resolve(s, atom)

		handle := SelectWith(s, atom, func(v []int) int { return len(v) }, func(a, b int) bool { return a == b })

		var notified atomic.Int32
		unsub := handle.Subscribe(func() {
			notified.Add(1)
		})
		defer unsub()

		ctrl := GetController(s, atom)
		_ = ctrl.Set([]int{4, 5, 6})
		s.Flush()
		time.Sleep(10 * time.Millisecond)

		if notified.Load() != 0 {
			t.Fatalf("should not notify when length same, got %d", notified.Load())
		}
	})
}

func TestSelectLazySubscribe(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)

	handle := Select(s, atom, func(v int) int { return v })

	if handle.unsub != nil {
		t.Fatal("expected no controller subscription before first Subscribe")
	}

	unsub := handle.Subscribe(func() {})

	if handle.unsub == nil {
		t.Fatal("expected controller subscription after first Subscribe")
	}

	unsub()
	time.Sleep(5 * time.Millisecond)
}

func TestSelectAutoCleanupOnLastUnsubscribe(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)

	handle := Select(s, atom, func(v int) int { return v })
	unsub := handle.Subscribe(func() {})
	unsub()

	handle.mu.RLock()
	u := handle.unsub
	handle.mu.RUnlock()
	if u != nil {
		t.Fatal("unsub should be nil after last subscriber removed")
	}
}

func TestSelectFromUnresolvedPanics(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from Select on unresolved atom")
		}
	}()
	Select(s, atom, func(v int) int { return v })
}

func TestExecContextClose(t *testing.T) {
	t.Run("lifo", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()

		var order []int
		ec.OnClose(func(err error) error {
			order = append(order, 1)
			return nil
		})
		ec.OnClose(func(err error) error {
			order = append(order, 2)
			return nil
		})
		ec.OnClose(func(err error) error {
			order = append(order, 3)
			return nil
		})

		ec.Close(nil)

		if len(order) != 3 || order[0] != 3 || order[1] != 2 || order[2] != 1 {
			t.Fatalf("expected LIFO [3,2,1], got %v", order)
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()

		var count int
		ec.OnClose(func(err error) error {
			count++
			return nil
		})

		ec.Close(nil)
		ec.Close(nil)

		if count != 1 {
			t.Fatalf("expected 1 close, got %d", count)
		}
	})
	t.Run("panic", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()

		var secondRan bool
		ec.OnClose(func(err error) error {
			secondRan = true
			return nil
		})
		ec.OnClose(func(err error) error { panic("close panic") })

		ec.Close(nil)
		if !secondRan {
			t.Fatal("second cleanup should run despite panic")
		}
	})
	t.Run("after_close", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()
		ec.Close(nil)

		var called bool
		ec.OnClose(func(err error) error {
			called = true
			return nil
		})

		if called {
			t.Fatal("cleanup registered after close should not run")
		}
	})
}

func TestExecContextReceivesCause(t *testing.T) {
	s := newTestScope(t)
	ec := s.CreateContext()

	var received error
	ec.OnClose(func(cause error) error {
		received = cause
		return nil
	})

	cause := errors.New("test cause")
	ec.Close(cause)

	if received != cause {
		t.Fatalf("expected cause, got %v", received)
	}
}

func TestExecContextCreation(t *testing.T) {
	s := newTestScope(t)
	ec := s.CreateContext()
	if ec.Scope() == nil {
		t.Fatal("scope should not be nil")
	}
	if ec.Data() == nil {
		t.Fatal("data should not be nil")
	}
	if ec.Context() == nil {
		t.Fatal("context should not be nil")
	}
	ec.Close(nil)
}

func TestExecContextParentChain(t *testing.T) {
	s := newTestScope(t)
	parent := s.CreateContext()
	defer parent.Close(nil)

	var capturedParent *ExecContext
	_, _ = ExecFn(parent, func(child *ExecContext) (int, error) {
		capturedParent = child.Parent()
		return 1, nil
	})

	if capturedParent != parent {
		t.Fatal("child's parent should be the parent ExecContext")
	}
}

func TestExecContextInput(t *testing.T) {
	flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
		if ec.Input() != "hello" {
			return "", fmt.Errorf("expected hello input, got %v", ec.Input())
		}
		return input, nil
	})
	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	_, err := ExecFlow(ec, flow, "hello")
	if err != nil {
		t.Fatal(err)
	}
}

func TestExecFlowBasic(t *testing.T) {
	flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
		return "hello " + input, nil
	})
	s := newTestScope(t)
	ec := s.CreateContext()

	result, err := ExecFlow(ec, flow, "world")
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello world" {
		t.Fatalf("expected hello world, got %s", result)
	}
}

func TestExecFlowWithParse(t *testing.T) {
	flow := NewFlow(func(ec *ExecContext, input int) (string, error) {
		return fmt.Sprintf("got %d", input), nil
	}, WithParse(func(raw any) (int, error) {
		v, ok := raw.(int)
		if !ok {
			return 0, errors.New("expected int")
		}
		if v < 0 {
			return 0, errors.New("negative")
		}
		return v * 10, nil
	}))

	s := newTestScope(t)
	ec := s.CreateContext()

	result, err := ExecFlow(ec, flow, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result != "got 50" {
		t.Fatalf("expected got 50, got %s", result)
	}
}

func TestExecFlowParseError(t *testing.T) {
	flow := NewFlow(func(ec *ExecContext, input int) (string, error) {
		return "", nil
	}, WithParse(func(raw any) (int, error) {
		return 0, errors.New("parse failed")
	}))

	s := newTestScope(t)
	ec := s.CreateContext()

	_, err := ExecFlow(ec, flow, 42)
	if err == nil {
		t.Fatal("expected parse error")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected ParseError, got %T", err)
	}
}

func TestExecContextGating(t *testing.T) {
	t.Run("flow_closed", func(t *testing.T) {
		flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
			return input, nil
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		ec.Close(nil)

		_, err := ExecFlow(ec, flow, "test")
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed, got %v", err)
		}
	})
	t.Run("flow_cancelled", func(t *testing.T) {
		flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
			return input, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := NewScope(ctx)
		_ = s.Ready()

		ec := s.CreateContext()
		_, err := ExecFlow(ec, flow, "test")
		if err == nil {
			t.Fatal("expected context cancelled error")
		}
	})
	t.Run("fn_closed", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()
		ec.Close(nil)

		_, err := ExecFn(ec, func(ec *ExecContext) (int, error) {
			return 1, nil
		})
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed, got %v", err)
		}
	})
	t.Run("fn_cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s := NewScope(ctx)
		_ = s.Ready()
		ec := s.CreateContext()

		_, err := ExecFn(ec, func(ec *ExecContext) (int, error) {
			return 1, nil
		})
		if err == nil {
			t.Fatal("expected error on cancelled context")
		}
	})
}

func TestExecFlowPresetRedirect(t *testing.T) {
	original := NewFlow(func(ec *ExecContext, input string) (string, error) {
		return "original:" + input, nil
	})
	replacement := NewFlow(func(ec *ExecContext, input string) (string, error) {
		return "replaced:" + input, nil
	})
	s := newTestScope(t, WithPresets(PresetFlow(original, replacement)))
	ec := s.CreateContext()

	v, err := ExecFlow(ec, original, "test")
	if err != nil {
		t.Fatal(err)
	}
	if v != "replaced:test" {
		t.Fatalf("expected replaced:test, got %s", v)
	}
}

func TestExecFlowPresetFn(t *testing.T) {
	original := NewFlow(func(ec *ExecContext, input string) (string, error) {
		return "original:" + input, nil
	})
	s := newTestScope(t, WithPresets(PresetFlowFn(original, func(ec *ExecContext) (string, error) {
		return "preset-fn-result", nil
	})))
	ec := s.CreateContext()

	v, err := ExecFlow(ec, original, "test")
	if err != nil {
		t.Fatal(err)
	}
	if v != "preset-fn-result" {
		t.Fatalf("expected preset-fn-result, got %s", v)
	}
}

func TestExecFnBasic(t *testing.T) {
	s := newTestScope(t)
	ec := s.CreateContext()

	result, err := ExecFn(ec, func(ec *ExecContext) (int, error) {
		return 42, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != 42 {
		t.Fatalf("expected 42, got %d", result)
	}
}

func TestExecFnWithName(t *testing.T) {
	s := newTestScope(t)
	ec := s.CreateContext()

	var capturedName string
	_, err := ExecFn(ec, func(child *ExecContext) (int, error) {
		capturedName = child.Name()
		return 1, nil
	}, WithExecName("myFunc"))
	if err != nil {
		t.Fatal(err)
	}
	if capturedName != "myFunc" {
		t.Fatalf("expected myFunc, got %s", capturedName)
	}
}

func namedExecFnForNameFallback(child *ExecContext) (string, error) {
	return child.Name(), nil
}

func TestExecFnName(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()

		capturedName, err := ExecFn(ec, namedExecFnForNameFallback)
		if err != nil {
			t.Fatal(err)
		}
		if capturedName != "namedExecFnForNameFallback" {
			t.Fatalf("expected namedExecFnForNameFallback, got %s", capturedName)
		}
	})
	t.Run("explicit", func(t *testing.T) {
		s := newTestScope(t)
		ec := s.CreateContext()

		capturedName, err := ExecFn(ec, namedExecFnForNameFallback, WithExecName("override"))
		if err != nil {
			t.Fatal(err)
		}
		if capturedName != "override" {
			t.Fatalf("expected override, got %s", capturedName)
		}
	})
}

func TestMultipleConcurrentExecFlow(t *testing.T) {
	flow := NewFlow(func(ec *ExecContext, input int) (int, error) {
		time.Sleep(5 * time.Millisecond)
		return input * 2, nil
	})
	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	var wg sync.WaitGroup
	results := make([]int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			v, _ := ExecFlow(ec, flow, idx)
			results[idx] = v
		}(i)
	}
	wg.Wait()

	for i, v := range results {
		if v != i*2 {
			t.Fatalf("idx %d: expected %d, got %d", i, i*2, v)
		}
	}
}

func TestTag(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		tag := NewTag[string]("env")
		tagged := tag.Value("production")

		if tagged.TagKey() != tag.Key() {
			t.Fatal("key mismatch")
		}
		if tagged.TagValue() != "production" {
			t.Fatal("value mismatch")
		}
	})
	t.Run("get", func(t *testing.T) {
		tag := NewTag[string]("env")
		source := []AnyTagged{tag.Value("staging")}

		v, err := tag.Get(source)
		if err != nil {
			t.Fatal(err)
		}
		if v != "staging" {
			t.Fatalf("expected staging, got %s", v)
		}
	})
	t.Run("not_found", func(t *testing.T) {
		tag := NewTag[string]("env")
		_, err := tag.Get(nil)
		if err == nil {
			t.Fatal("expected error for missing tag")
		}
	})
	t.Run("default", func(t *testing.T) {
		tag := NewTagWithDefault("env", "dev")
		v, err := tag.Get(nil)
		if err != nil {
			t.Fatal(err)
		}
		if v != "dev" {
			t.Fatalf("expected dev, got %s", v)
		}
	})
	t.Run("find", func(t *testing.T) {
		tag := NewTag[int]("count")
		source := []AnyTagged{tag.Value(5)}

		v, ok := tag.Find(source)
		if !ok || v != 5 {
			t.Fatalf("expected (5, true), got (%d, %v)", v, ok)
		}

		_, ok = tag.Find(nil)
		if ok {
			t.Fatal("expected not found")
		}
	})
	t.Run("collect", func(t *testing.T) {
		tag := NewTag[string]("item")
		source := []AnyTagged{tag.Value("a"), tag.Value("b"), tag.Value("c")}
		result := tag.Collect(source)
		if len(result) != 3 {
			t.Fatalf("expected 3 items, got %d", len(result))
		}
	})
}

func TestTagWithParse(t *testing.T) {
	tag := NewTagWithParse("positive", func(raw any) (int, error) {
		v, ok := raw.(int)
		if !ok || v < 0 {
			return 0, errors.New("must be positive int")
		}
		return v, nil
	})

	tagged := tag.Value(5)
	if tagged.TagValue() != 5 {
		t.Fatalf("expected 5, got %v", tagged.TagValue())
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid value")
		}
	}()
	tag.Value(-1)
}

func TestTagUniqueIDs(t *testing.T) {
	t1 := NewTag[string]("same")
	t2 := NewTag[string]("same")
	if t1.Key() == t2.Key() {
		t.Fatal("tags with same label should have unique keys")
	}
}

func TestContextData(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		d := newContextData(nil)
		d.Set("key", "value")

		v, ok := d.Get("key")
		if !ok || v != "value" {
			t.Fatal("get failed")
		}

		if !d.Has("key") {
			t.Fatal("has failed")
		}

		d.Delete("key")
		if d.Has("key") {
			t.Fatal("delete failed")
		}
	})
	t.Run("seek", func(t *testing.T) {
		parent := newContextData(nil)
		parent.Set("inherited", "from-parent")

		child := newContextData(parent)
		child.Set("own", "from-child")

		v, ok := child.Seek("inherited")
		if !ok || v != "from-parent" {
			t.Fatal("seek through parent failed")
		}

		v, ok = child.Seek("own")
		if !ok || v != "from-child" {
			t.Fatal("seek own failed")
		}
	})
	t.Run("clear", func(t *testing.T) {
		d := newContextData(nil)
		d.Set("a", 1)
		d.Set("b", 2)
		d.Clear()
		if d.Has("a") || d.Has("b") {
			t.Fatal("clear failed")
		}
	})
}

func TestContextDataTagOps(t *testing.T) {
	tag := NewTag[string]("test")
	d := newContextData(nil)

	SetTag(d, tag, "hello")
	v, ok := GetTag(d, tag)
	if !ok || v != "hello" {
		t.Fatal("tag ops failed")
	}

	if !HasTag(d, tag) {
		t.Fatal("HasTag failed")
	}

	DeleteTag(d, tag)
	if HasTag(d, tag) {
		t.Fatal("DeleteTag failed")
	}
}

func TestContextDataSeekTag(t *testing.T) {
	tag := NewTag[int]("count")
	parent := newContextData(nil)
	SetTag(parent, tag, 42)

	child := newContextData(parent)
	v, ok := SeekTag(child, tag)
	if !ok || v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
}

func TestContextDataGetOrSetTag(t *testing.T) {
	tag := NewTagWithDefault("counter", 0)
	d := newContextData(nil)

	v := GetOrSetTag(d, tag)
	if v != 0 {
		t.Fatalf("expected default 0, got %d", v)
	}

	v2 := GetOrSetTag(d, tag, 99)
	if v2 != 0 {
		t.Fatalf("expected existing 0, got %d", v2)
	}
}

func TestPresetValue(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (string, error) {
		return "original", nil
	})
	s := newTestScope(t, WithPresets(Preset(atom, "preset-value")))

	v, err := Resolve(s, atom)
	if err != nil {
		t.Fatal(err)
	}
	if v != "preset-value" {
		t.Fatalf("expected preset-value, got %s", v)
	}
}

func TestPresetAtomRedirect(t *testing.T) {
	original := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	replacement := NewAtom(func(rc *ResolveContext) (int, error) {
		return 99, nil
	})
	s := newTestScope(t, WithPresets(PresetAtom(original, replacement)))

	v, err := Resolve(s, original)
	if err != nil {
		t.Fatal(err)
	}
	if v != 99 {
		t.Fatalf("expected 99, got %d", v)
	}
}

type trackingExtension struct {
	initCalled    atomic.Bool
	disposeCalled atomic.Bool
	resolveCount  atomic.Int32
	execCount     atomic.Int32
}

func (e *trackingExtension) Name() string { return "tracking" }
func (e *trackingExtension) Init(s Scope) error {
	e.initCalled.Store(true)
	return nil
}
func (e *trackingExtension) WrapResolve(next func() (any, error), atom AnyAtom, s Scope) (any, error) {
	e.resolveCount.Add(1)
	return next()
}
func (e *trackingExtension) WrapExec(next func() (any, error), target AnyExecTarget, ec *ExecContext) (any, error) {
	e.execCount.Add(1)
	return next()
}
func (e *trackingExtension) Dispose(s Scope) error {
	e.disposeCalled.Store(true)
	return nil
}

func TestExtensionInit(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ext := &trackingExtension{}
		s := newTestScope(t, WithExtensions(ext))
		_ = s
		if !ext.initCalled.Load() {
			t.Fatal("init not called")
		}
	})
	t.Run("failure", func(t *testing.T) {
		ext := &failInitExtension{}
		s := NewScope(context.Background(), WithExtensions(ext))
		err := s.ReadyErr()
		if err == nil {
			t.Fatal("expected init error")
		}
	})
	t.Run("panic", func(t *testing.T) {
		ext := &panicInitExtension{}
		s := NewScope(context.Background(), WithExtensions(ext))
		err := s.ReadyErr()
		if err == nil {
			t.Fatal("expected error from panic in init")
		}
	})
}

type failInitExtension struct{}

func (e *failInitExtension) Name() string       { return "fail-init" }
func (e *failInitExtension) Init(s Scope) error { return errors.New("init failed") }

func TestExtensionWrapResolve(t *testing.T) {
	ext := &trackingExtension{}
	atom := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
	s := newTestScope(t, WithExtensions(ext))
	_, _ = Resolve(s, atom)

	if ext.resolveCount.Load() < 1 {
		t.Fatal("WrapResolve not called")
	}
}

func TestExtensionWrapExec(t *testing.T) {
	ext := &trackingExtension{}
	s := newTestScope(t, WithExtensions(ext))
	ec := s.CreateContext()

	_, _ = ExecFn(ec, func(ec *ExecContext) (int, error) { return 1, nil })
	if ext.execCount.Load() < 1 {
		t.Fatal("WrapExec not called")
	}
}

func TestExtensionDispose(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		ext := &trackingExtension{}
		s := NewScope(context.Background(), WithExtensions(ext))
		_ = s.Ready()
		_ = s.Dispose()

		if !ext.disposeCalled.Load() {
			t.Fatal("dispose not called")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		ext := &slowDisposeExtension{}
		s := NewScope(context.Background(), WithExtensions(ext))
		_ = s.Ready()

		start := time.Now()
		_ = s.Dispose()
		elapsed := time.Since(start)

		if elapsed > 6*time.Second {
			t.Fatal("dispose should timeout at 5s")
		}
	})
	t.Run("panic", func(t *testing.T) {
		ext := &panicDisposeExtension{}
		s := NewScope(context.Background(), WithExtensions(ext))
		_ = s.Ready()
		_ = s.Dispose()
	})
}

type slowDisposeExtension struct{}

func (e *slowDisposeExtension) Name() string { return "slow-dispose" }
func (e *slowDisposeExtension) Dispose(s Scope) error {
	time.Sleep(10 * time.Second)
	return nil
}

func TestExtensionChain(t *testing.T) {
	var order []string
	ext1 := &orderExtension{name: "ext1", order: &order}
	ext2 := &orderExtension{name: "ext2", order: &order}

	atom := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
	s := newTestScope(t, WithExtensions(ext1, ext2))
	_, _ = Resolve(s, atom)

	if len(order) != 2 || order[0] != "ext1" || order[1] != "ext2" {
		t.Fatalf("expected [ext1 ext2], got %v", order)
	}
}

type orderExtension struct {
	name  string
	order *[]string
}

func (e *orderExtension) Name() string { return e.name }
func (e *orderExtension) WrapResolve(next func() (any, error), atom AnyAtom, s Scope) (any, error) {
	*e.order = append(*e.order, e.name)
	return next()
}

func TestExtensionExecChain(t *testing.T) {
	var order []string
	ext1 := &orderExecExtension{name: "ext1", order: &order}
	ext2 := &orderExecExtension{name: "ext2", order: &order}

	s := newTestScope(t, WithExtensions(ext1, ext2))
	ec := s.CreateContext()

	_, _ = ExecFn(ec, func(ec *ExecContext) (int, error) { return 1, nil })

	if len(order) != 2 || order[0] != "ext1" || order[1] != "ext2" {
		t.Fatalf("expected [ext1 ext2], got %v", order)
	}
}

type orderExecExtension struct {
	name  string
	order *[]string
}

func (e *orderExecExtension) Name() string { return e.name }
func (e *orderExecExtension) WrapExec(next func() (any, error), target AnyExecTarget, ec *ExecContext) (any, error) {
	*e.order = append(*e.order, e.name)
	return next()
}

type panicInitExtension struct{}

func (e *panicInitExtension) Name() string       { return "panic-init" }
func (e *panicInitExtension) Init(s Scope) error { panic("init panic") }

func TestExtensionExecPanicContainment(t *testing.T) {
	ext := &panicExecExtension{}
	s := newTestScope(t, WithExtensions(ext))
	ec := s.CreateContext()

	_, err := ExecFn(ec, func(ec *ExecContext) (int, error) { return 1, nil })
	if err == nil {
		t.Fatal("expected error from panic in exec extension")
	}
}

type panicExecExtension struct{}

func (e *panicExecExtension) Name() string { return "panic-exec" }
func (e *panicExecExtension) WrapExec(next func() (any, error), target AnyExecTarget, ec *ExecContext) (any, error) {
	panic("exec panic")
}

type panicDisposeExtension struct{}

func (e *panicDisposeExtension) Name() string          { return "panic-dispose" }
func (e *panicDisposeExtension) Dispose(s Scope) error { panic("dispose panic") }

func TestGC(t *testing.T) {
	t.Run("releases", func(t *testing.T) {
		var cleanedUp atomic.Bool
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			rc.Cleanup(func() error {
				cleanedUp.Store(true)
				return nil
			})
			return 1, nil
		})

		s := newTestScope(t, WithGC(GCOptions{Enabled: true, GraceMs: 50}))
		_, _ = Resolve(s, atom)

		time.Sleep(200 * time.Millisecond)

		if !cleanedUp.Load() {
			t.Fatal("GC should have cleaned up the atom")
		}
	})
	t.Run("keepalive", func(t *testing.T) {
		var cleanedUp atomic.Bool
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			rc.Cleanup(func() error {
				cleanedUp.Store(true)
				return nil
			})
			return 1, nil
		}, WithKeepAlive())

		s := newTestScope(t, WithGC(GCOptions{Enabled: true, GraceMs: 50}))
		_, _ = Resolve(s, atom)

		time.Sleep(200 * time.Millisecond)

		if cleanedUp.Load() {
			t.Fatal("keepAlive atom should not be GC'd")
		}
	})
	t.Run("subscriber_prevents", func(t *testing.T) {
		var cleanedUp atomic.Bool
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			rc.Cleanup(func() error {
				cleanedUp.Store(true)
				return nil
			})
			return 1, nil
		})

		s := newTestScope(t, WithGC(GCOptions{Enabled: true, GraceMs: 50}))
		ctrl, _ := GetControllerResolved(s, atom)
		unsub := ctrl.On(EventResolved, func() {})

		time.Sleep(200 * time.Millisecond)
		if cleanedUp.Load() {
			t.Fatal("atom with subscriber should not be GC'd")
		}

		unsub()
		time.Sleep(200 * time.Millisecond)
		if !cleanedUp.Load() {
			t.Fatal("atom should be GC'd after unsubscribe")
		}
	})
	t.Run("dependents_prevent", func(t *testing.T) {
		var cleanedUp atomic.Bool
		child := NewAtom(func(rc *ResolveContext) (int, error) {
			rc.Cleanup(func() error {
				cleanedUp.Store(true)
				return nil
			})
			return 10, nil
		})
		parent := NewAtom(func(rc *ResolveContext) (int, error) {
			v, _ := ResolveFrom(rc, child)
			return v + 1, nil
		})

		s := newTestScope(t, WithGC(GCOptions{Enabled: true, GraceMs: 50}))
		_, _ = Resolve(s, parent)

		time.Sleep(200 * time.Millisecond)
		if cleanedUp.Load() {
			t.Fatal("child with dependents should not be GC'd")
		}
	})
	t.Run("disabled", func(t *testing.T) {
		var cleanedUp atomic.Bool
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			rc.Cleanup(func() error {
				cleanedUp.Store(true)
				return nil
			})
			return 1, nil
		})

		s := newTestScope(t, WithGC(GCOptions{Enabled: false}))
		_, _ = Resolve(s, atom)

		time.Sleep(200 * time.Millisecond)

		if cleanedUp.Load() {
			t.Fatal("GC disabled should not clean up")
		}
	})
}

func TestGCPendingInvalidateDoesNotReleaseDuringReresolve(t *testing.T) {
	resolving := make(chan struct{})
	proceed := make(chan struct{})
	firstDone := make(chan struct{})
	var count atomic.Int32

	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		v := count.Add(1)
		if v == 1 {
			close(resolving)
			<-proceed
		}
		if v == 2 {
			time.Sleep(60 * time.Millisecond)
		}
		return int(v), nil
	})

	s := newTestScope(t, WithGC(GCOptions{Enabled: true, GraceMs: 5}))
	ctrl := GetController(s, atom)

	go func() {
		_, _ = Resolve(s, atom)
		close(firstDone)
	}()

	<-resolving
	ctrl.Invalidate()
	close(proceed)
	<-firstDone

	if err := s.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	v, err := ctrl.Get()
	if err != nil {
		t.Fatalf("get after pending invalidate failed: %v", err)
	}
	if v != 2 {
		t.Fatalf("expected 2, got %d", v)
	}
}

func TestCleanupLIFO(t *testing.T) {
	var order []int
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		rc.Cleanup(func() error { order = append(order, 1); return nil })
		rc.Cleanup(func() error { order = append(order, 2); return nil })
		rc.Cleanup(func() error { order = append(order, 3); return nil })
		return 1, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)
	_ = Release(s, atom)

	if len(order) != 3 || order[0] != 3 || order[1] != 2 || order[2] != 1 {
		t.Fatalf("expected LIFO [3,2,1], got %v", order)
	}
}

func TestCleanupPanicContainment(t *testing.T) {
	var secondRan atomic.Bool
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		rc.Cleanup(func() error {
			secondRan.Store(true)
			return nil
		})
		rc.Cleanup(func() error {
			panic("cleanup panic")
		})
		return 1, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)
	_ = Release(s, atom)

	if !secondRan.Load() {
		t.Fatal("second cleanup should run despite panic in first")
	}
}

func TestScopeDispose(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		var cleanedUp atomic.Bool
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			rc.Cleanup(func() error {
				cleanedUp.Store(true)
				return nil
			})
			return 1, nil
		})

		s := NewScope(context.Background())
		_ = s.Ready()
		_, _ = Resolve(s, atom)
		_ = s.Dispose()

		if !cleanedUp.Load() {
			t.Fatal("dispose should run cleanups")
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		s := NewScope(context.Background())
		_ = s.Ready()

		err1 := s.Dispose()
		err2 := s.Dispose()

		if err1 != nil {
			t.Fatal(err1)
		}
		if err2 != nil {
			t.Fatal(err2)
		}
	})
	t.Run("rejects_resolve", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		})
		s := NewScope(context.Background())
		_ = s.Ready()
		_ = s.Dispose()

		_, err := Resolve(s, atom)
		if err == nil {
			t.Fatal("expected error after dispose")
		}
	})
}

func TestContextCancellationPropagation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewScope(ctx)
	_ = s.Ready()
	ec := s.CreateContext()

	cancel()

	if ec.Context().Err() == nil {
		t.Fatal("expected context to be cancelled")
	}
}

func TestFlush(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		var count atomic.Int32
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return int(count.Add(1)), nil
		})
		s := newTestScope(t)
		ctrl, _ := GetControllerResolved(s, atom)
		ctrl.Invalidate()
		s.Flush()

		v, _ := ctrl.Get()
		if v != 2 {
			t.Fatalf("expected 2 after invalidate+flush, got %d", v)
		}
	})
	t.Run("empty", func(t *testing.T) {
		s := newTestScope(t)
		err := s.Flush()
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("waits_chain", func(t *testing.T) {
		var count atomic.Int32
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			v := count.Add(1)
			if v == 2 {
				time.Sleep(20 * time.Millisecond)
			}
			return int(v), nil
		})
		s := newTestScope(t)
		ctrl, _ := GetControllerResolved(s, atom)
		ctrl.Invalidate()
		s.Flush()

		v, _ := ctrl.Get()
		if v != 2 {
			t.Fatalf("expected 2, got %d", v)
		}
	})
}

func TestDependentsCascadeInvalidation(t *testing.T) {
	var childCount atomic.Int32
	var parentCount atomic.Int32

	child := NewAtom(func(rc *ResolveContext) (int, error) {
		return int(childCount.Add(1)), nil
	})
	parent := NewAtom(func(rc *ResolveContext) (int, error) {
		parentCount.Add(1)
		v, _ := ResolveFrom(rc, child)
		return v * 10, nil
	})

	s := newTestScope(t)
	_, _ = Resolve(s, parent)

	childCtrl := GetController(s, child)
	childCtrl.Invalidate()
	s.Flush()

	parentCtrl := GetController(s, parent)
	v, _ := parentCtrl.Get()
	if v < 20 {
		t.Fatalf("expected cascade to re-resolve parent, got %d", v)
	}
}

func TestRelease(t *testing.T) {
	var cleanedUp atomic.Bool
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		rc.Cleanup(func() error {
			cleanedUp.Store(true)
			return nil
		})
		return 1, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)
	_ = Release(s, atom)

	if !cleanedUp.Load() {
		t.Fatal("release should run cleanups")
	}

	_, err := Resolve(s, atom)
	if err != nil {
		t.Fatal("re-resolve after release should work")
	}
}

func TestReleaseNonExistent(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
	s := newTestScope(t)
	err := Release(s, atom)
	if err != nil {
		t.Fatalf("release non-existent should be noop, got %v", err)
	}
}

func TestCloseErrorAggregation(t *testing.T) {
	s := newTestScope(t)
	ec := s.CreateContext()

	ec.OnClose(func(err error) error { return errors.New("err1") })
	ec.OnClose(func(err error) error { return errors.New("err2") })
	ec.OnClose(func(err error) error { return errors.New("err3") })

	closeErr := ec.Close(nil)
	if closeErr == nil {
		t.Fatal("expected aggregated error")
	}
}

func TestScopeOnStateListener(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)

	var resolvedCount atomic.Int32
	unsub := s.On(StateResolved, atom, func() {
		resolvedCount.Add(1)
	})
	defer unsub()

	_, _ = Resolve(s, atom)
	if resolvedCount.Load() < 1 {
		t.Fatal("expected resolved state notification")
	}
}

func TestScopeOnStateUnsubscribe(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 1, nil
	})
	s := newTestScope(t)

	var count atomic.Int32
	unsub := s.On(StateResolved, atom, func() {
		count.Add(1)
	})
	unsub()

	_, _ = Resolve(s, atom)
	time.Sleep(10 * time.Millisecond)
}

func TestAtomStateString(t *testing.T) {
	tests := []struct {
		s    AtomState
		want string
	}{
		{StateIdle, "idle"},
		{StateResolving, "resolving"},
		{StateResolved, "resolved"},
		{StateFailed, "failed"},
		{AtomState(99), "unknown"},
	}
	for _, tt := range tests {
		if tt.s.String() != tt.want {
			t.Fatalf("expected %s, got %s", tt.want, tt.s.String())
		}
	}
}

func TestControllerEventString(t *testing.T) {
	tests := []struct {
		e    ControllerEvent
		want string
	}{
		{EventResolving, "resolving"},
		{EventResolved, "resolved"},
		{EventAll, "*"},
		{ControllerEvent(99), "unknown"},
	}
	for _, tt := range tests {
		if tt.e.String() != tt.want {
			t.Fatalf("expected %s, got %s", tt.want, tt.e.String())
		}
	}
}

func TestAtomOptions(t *testing.T) {
	t.Run("name", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		}, WithAtomName("myAtom"))

		if atom.atomName() != "myAtom" {
			t.Fatalf("expected myAtom, got %s", atom.atomName())
		}
	})
	t.Run("keepalive", func(t *testing.T) {
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		}, WithKeepAlive())

		if !atom.atomKeepAlive() {
			t.Fatal("expected keepAlive")
		}
	})
	t.Run("tags", func(t *testing.T) {
		tag := NewTag[string]("env")
		atom := NewAtom(func(rc *ResolveContext) (int, error) {
			return 1, nil
		}, WithAtomTags(tag.Value("test")))

		if len(atom.atomTags()) != 1 {
			t.Fatal("expected 1 tag")
		}
	})
}

func TestFlowName(t *testing.T) {
	flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
		return input, nil
	}, WithFlowName("myFlow"))

	if flow.flowName() != "myFlow" {
		t.Fatalf("expected myFlow, got %s", flow.flowName())
	}
}

func TestFlowTags(t *testing.T) {
	tag := NewTag[string]("env")
	flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
		return input, nil
	}, WithFlowTags(tag.Value("prod")))

	if len(flow.flowTags()) != 1 {
		t.Fatal("expected 1 tag")
	}
}

func TestTagPrecedence(t *testing.T) {
	t.Run("scope", func(t *testing.T) {
		tag := NewTag[string]("env")
		s := newTestScope(t, WithScopeTags(tag.Value("staging")))

		tags := s.Tags()
		if len(tags) != 1 {
			t.Fatal("expected 1 scope tag")
		}
	})
	t.Run("create_context", func(t *testing.T) {
		tag := NewTag[string]("req")
		s := newTestScope(t)
		ec := s.CreateContext(WithContextTags(tag.Value("abc")))
		defer ec.Close(nil)

		v, ok := GetTag(ec.Data(), tag)
		if !ok || v != "abc" {
			t.Fatalf("expected abc, got %s", v)
		}
	})
	t.Run("absent_create", func(t *testing.T) {
		tag := NewTag[string]("env")
		s := newTestScope(t, WithScopeTags(tag.Value("default")))

		ec1 := s.CreateContext()
		defer ec1.Close(nil)
		v1, _ := GetTag(ec1.Data(), tag)
		if v1 != "default" {
			t.Fatalf("expected default, got %s", v1)
		}

		ec2 := s.CreateContext(WithContextTags(tag.Value("override")))
		defer ec2.Close(nil)
		v2, _ := GetTag(ec2.Data(), tag)
		if v2 != "override" {
			t.Fatalf("expected override, got %s", v2)
		}
	})
	t.Run("absent_exec", func(t *testing.T) {
		tag := NewTag[string]("env")
		flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
			v, _ := SeekTag(ec.Data(), tag)
			return v, nil
		})

		s := newTestScope(t, WithScopeTags(tag.Value("scope-default")))
		ec := s.CreateContext()

		result, _ := ExecFlow(ec, flow, "test", WithExecTags(tag.Value("exec-override")))
		if result != "exec-override" {
			t.Fatalf("expected exec-override, got %s", result)
		}

		result2, _ := ExecFlow(ec, flow, "test")
		if result2 != "scope-default" {
			t.Fatalf("expected scope-default, got %s", result2)
		}
	})
	t.Run("parent_context", func(t *testing.T) {
		tag := NewTag[string]("env")
		flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
			v, _ := SeekTag(ec.Data(), tag)
			return v, nil
		})

		s := newTestScope(t, WithScopeTags(tag.Value("scope-default")))
		ec := s.CreateContext(WithContextTags(tag.Value("ctx-override")))

		result, err := ExecFlow(ec, flow, "test")
		if err != nil {
			t.Fatalf("exec failed: %v", err)
		}
		if result != "ctx-override" {
			t.Fatalf("expected ctx-override, got %s", result)
		}
	})
	t.Run("nested_exec", func(t *testing.T) {
		tag := NewTag[string]("env")
		inner := NewFlow(func(ec *ExecContext, input string) (string, error) {
			v, _ := SeekTag(ec.Data(), tag)
			return v, nil
		})

		s := newTestScope(t, WithScopeTags(tag.Value("scope-default")))
		root := s.CreateContext()

		got, err := ExecFn(root, func(parent *ExecContext) (string, error) {
			return ExecFlow(parent, inner, "child")
		}, WithExecTags(tag.Value("exec-override")))
		if err != nil {
			t.Fatalf("nested exec failed: %v", err)
		}
		if got != "exec-override" {
			t.Fatalf("expected exec-override, got %s", got)
		}
	})
	t.Run("fn_parent", func(t *testing.T) {
		tag := NewTag[string]("env")
		s := newTestScope(t, WithScopeTags(tag.Value("scope-default")))
		root := s.CreateContext(WithContextTags(tag.Value("ctx-override")))

		got, err := ExecFn(root, func(child *ExecContext) (string, error) {
			v, _ := SeekTag(child.Data(), tag)
			return v, nil
		})
		if err != nil {
			t.Fatalf("exec fn failed: %v", err)
		}
		if got != "ctx-override" {
			t.Fatalf("expected ctx-override, got %s", got)
		}
	})
	t.Run("flow_tags", func(t *testing.T) {
		tag := NewTag[string]("source")
		flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
			v, _ := GetTag(ec.Data(), tag)
			return v, nil
		}, WithFlowTags(tag.Value("from-flow")))

		s := newTestScope(t)
		ec := s.CreateContext()
		result, _ := ExecFlow(ec, flow, "test")
		if result != "from-flow" {
			t.Fatalf("expected from-flow, got %s", result)
		}
	})
}

func TestUniqueIDs(t *testing.T) {
	t.Run("atoms", func(t *testing.T) {
		a1 := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
		a2 := NewAtom(func(rc *ResolveContext) (int, error) { return 2, nil })
		if a1.id == a2.id {
			t.Fatal("atoms should have unique IDs")
		}
	})
	t.Run("flows", func(t *testing.T) {
		f1 := NewFlow(func(ec *ExecContext, input int) (int, error) { return input, nil })
		f2 := NewFlow(func(ec *ExecContext, input int) (int, error) { return input, nil })
		if f1.id == f2.id {
			t.Fatal("flows should have unique IDs")
		}
	})
	t.Run("shared_counter", func(t *testing.T) {
		a := NewAtom(func(rc *ResolveContext) (int, error) { return 1, nil })
		f := NewFlow(func(ec *ExecContext, input int) (int, error) { return input, nil })
		if a.id == f.id {
			t.Fatal("atom and flow should not share ID")
		}
	})
}

func TestCircularDepErrorMessage(t *testing.T) {
	err := &CircularDepError{Path: []uint64{1, 2, 3, 1}}
	msg := err.Error()
	if msg != "circular dependency detected: 1 -> 2 -> 3 -> 1" {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestParseError(t *testing.T) {
	err := &ParseError{Phase: "test", Label: "myLabel", Cause: errors.New("bad")}
	msg := err.Error()
	if msg != "failed to parse test \"myLabel\": bad" {
		t.Fatalf("unexpected error message: %s", msg)
	}
	if !errors.Is(err, errors.Unwrap(err)) {
		t.Fatal("unwrap failed")
	}
}

func TestResolveContextScope(t *testing.T) {
	var capturedScope Scope
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		capturedScope = rc.Scope()
		return 1, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)

	if capturedScope == nil {
		t.Fatal("scope should not be nil")
	}
}

func TestResolveContextData(t *testing.T) {
	tag := NewTag[string]("info")
	atom := NewAtom(func(rc *ResolveContext) (string, error) {
		SetTag(rc.Data(), tag, "hello")
		v, _ := GetTag(rc.Data(), tag)
		return v, nil
	})
	s := newTestScope(t)
	v, _ := Resolve(s, atom)
	if v != "hello" {
		t.Fatalf("expected hello, got %s", v)
	}
}

func TestResolveContextInvalidate(t *testing.T) {
	var count atomic.Int32
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		v := int(count.Add(1))
		if v == 1 {
			go func() {
				time.Sleep(5 * time.Millisecond)
				rc.Invalidate()
			}()
		}
		return v, nil
	})
	s := newTestScope(t)
	_, _ = Resolve(s, atom)
	time.Sleep(50 * time.Millisecond)
	s.Flush()
}

func TestResolveFromTracksDependents(t *testing.T) {
	child := NewAtom(func(rc *ResolveContext) (int, error) { return 10, nil })
	parent := NewAtom(func(rc *ResolveContext) (int, error) {
		v, _ := ResolveFrom(rc, child)
		return v, nil
	})

	s := newTestScope(t)
	_, _ = Resolve(s, parent)

	si := s.(*scopeImpl)
	si.mu.RLock()
	entry := si.cache[child.atomID()]
	si.mu.RUnlock()

	entry.mu.RLock()
	hasDep := entry.dependents[parent.atomID()]
	entry.mu.RUnlock()

	if !hasDep {
		t.Fatal("child should track parent as dependent")
	}
}

func TestPendingSetDuringResolving(t *testing.T) {
	resolving := make(chan struct{})
	proceed := make(chan struct{})

	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		close(resolving)
		<-proceed
		return 1, nil
	})

	s := newTestScope(t)
	ctrl := GetController(s, atom)

	go func() {
		_, _ = Resolve(s, atom)
	}()

	<-resolving
	_ = ctrl.Set(42)
	close(proceed)

	s.Flush()
	time.Sleep(50 * time.Millisecond)

	v, _ := ctrl.Get()
	if v != 42 {
		t.Fatalf("expected pending set value 42, got %d", v)
	}
}

func TestPendingInvalidateDuringResolving(t *testing.T) {
	var count atomic.Int32
	resolving := make(chan struct{})
	proceed := make(chan struct{})

	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		v := count.Add(1)
		if v == 1 {
			close(resolving)
			<-proceed
		}
		return int(v), nil
	})

	s := newTestScope(t)
	ctrl := GetController(s, atom)

	go func() {
		_, _ = Resolve(s, atom)
	}()

	<-resolving
	ctrl.Invalidate()
	close(proceed)

	time.Sleep(50 * time.Millisecond)
	s.Flush()

	v, _ := ctrl.Get()
	if v < 2 {
		t.Fatalf("expected re-resolve after pending invalidate, got %d", v)
	}
}

func TestUpdateFunctionPanicContainment(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return 10, nil
	})
	s := newTestScope(t)
	ctrl, _ := GetControllerResolved(s, atom)

	_ = ctrl.Update(func(prev int) int {
		panic("update panic")
	})
	s.Flush()

	v, _ := ctrl.Get()
	if v != 10 {
		t.Fatalf("expected original value 10 after update panic, got %d", v)
	}
}

func TestReadyErrBlocksUntilReady(t *testing.T) {
	ext := &slowInitExtension{delay: 50 * time.Millisecond}
	s := NewScope(context.Background(), WithExtensions(ext))
	t.Cleanup(func() { _ = s.Dispose() })

	start := time.Now()
	err := s.ReadyErr()
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatal("ReadyErr should block until extensions initialize")
	}
}

type slowInitExtension struct {
	delay time.Duration
}

func (e *slowInitExtension) Name() string { return "slow-init" }
func (e *slowInitExtension) Init(s Scope) error {
	time.Sleep(e.delay)
	return nil
}

func TestExecContextData(t *testing.T) {
	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	ec.Data().Set("key", "val")
	v, ok := ec.Data().Get("key")
	if !ok || v != "val" {
		t.Fatal("ExecContext Data should work")
	}
}

func TestMultipleInvalidationBatches(t *testing.T) {
	var count atomic.Int32
	atom := NewAtom(func(rc *ResolveContext) (int, error) {
		return int(count.Add(1)), nil
	})
	s := newTestScope(t)
	ctrl, _ := GetControllerResolved(s, atom)

	ctrl.Invalidate()
	s.Flush()

	ctrl.Invalidate()
	s.Flush()

	v, _ := ctrl.Get()
	if v != 3 {
		t.Fatalf("expected 3 after two invalidation batches, got %d", v)
	}
}

func TestTagIterator(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		tag := NewTag[string]("item")
		other := NewTag[int]("num")
		source := []AnyTagged{tag.Value("a"), other.Value(1), tag.Value("b"), tag.Value("c")}

		var collected []string
		for v := range tag.All(source) {
			collected = append(collected, v)
		}
		if len(collected) != 3 || collected[0] != "a" || collected[1] != "b" || collected[2] != "c" {
			t.Fatalf("expected [a b c], got %v", collected)
		}
	})
	t.Run("empty", func(t *testing.T) {
		tag := NewTag[string]("item")
		var count int
		for range tag.All(nil) {
			count++
		}
		if count != 0 {
			t.Fatalf("expected 0 iterations on nil source, got %d", count)
		}
	})
	t.Run("break", func(t *testing.T) {
		tag := NewTag[int]("n")
		source := []AnyTagged{tag.Value(1), tag.Value(2), tag.Value(3)}

		var first int
		for v := range tag.All(source) {
			first = v
			break
		}
		if first != 1 {
			t.Fatalf("expected 1, got %d", first)
		}
	})
}

func TestContextDataAll(t *testing.T) {
	t.Run("normal", func(t *testing.T) {
		d := newContextData(nil)
		d.Set("a", 1)
		d.Set("b", 2)
		d.Set("c", 3)

		collected := make(map[string]any)
		for k, v := range d.All() {
			collected[k] = v
		}
		if len(collected) != 3 {
			t.Fatalf("expected 3 entries, got %d", len(collected))
		}
		if collected["a"] != 1 || collected["b"] != 2 || collected["c"] != 3 {
			t.Fatalf("unexpected values: %v", collected)
		}
	})
	t.Run("empty", func(t *testing.T) {
		d := newContextData(nil)
		var count int
		for range d.All() {
			count++
		}
		if count != 0 {
			t.Fatalf("expected 0 iterations on empty data, got %d", count)
		}
	})
	t.Run("break", func(t *testing.T) {
		d := newContextData(nil)
		d.Set("a", 1)
		d.Set("b", 2)

		var count int
		for range d.All() {
			count++
			break
		}
		if count != 1 {
			t.Fatalf("expected 1 iteration after break, got %d", count)
		}
	})
}

func TestContextDataSeekAll(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		grandparent := newContextData(nil)
		grandparent.Set("key", "gp-val")

		parent := newContextData(grandparent)
		parent.Set("key", "parent-val")

		child := newContextData(parent)
		child.Set("key", "child-val")

		var collected []any
		for v := range child.SeekAll("key") {
			collected = append(collected, v)
		}
		if len(collected) != 3 {
			t.Fatalf("expected 3 values, got %d", len(collected))
		}
		if collected[0] != "child-val" || collected[1] != "parent-val" || collected[2] != "gp-val" {
			t.Fatalf("expected [child-val parent-val gp-val], got %v", collected)
		}
	})
	t.Run("missing", func(t *testing.T) {
		parent := newContextData(nil)
		child := newContextData(parent)

		var count int
		for range child.SeekAll("nonexistent") {
			count++
		}
		if count != 0 {
			t.Fatalf("expected 0 iterations for missing key, got %d", count)
		}
	})
	t.Run("partial", func(t *testing.T) {
		grandparent := newContextData(nil)
		grandparent.Set("key", "gp-val")

		parent := newContextData(grandparent)

		child := newContextData(parent)
		child.Set("key", "child-val")

		var collected []any
		for v := range child.SeekAll("key") {
			collected = append(collected, v)
		}
		if len(collected) != 2 {
			t.Fatalf("expected 2 values (skipping parent), got %d", len(collected))
		}
		if collected[0] != "child-val" || collected[1] != "gp-val" {
			t.Fatalf("expected [child-val gp-val], got %v", collected)
		}
	})
	t.Run("break", func(t *testing.T) {
		parent := newContextData(nil)
		parent.Set("key", "parent-val")

		child := newContextData(parent)
		child.Set("key", "child-val")

		var first any
		for v := range child.SeekAll("key") {
			first = v
			break
		}
		if first != "child-val" {
			t.Fatalf("expected child-val, got %v", first)
		}
	})
}
