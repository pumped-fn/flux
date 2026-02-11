package flux

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAcquire(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		r := NewResource("counter", func(ec *ExecContext) (int, error) {
			return 42, nil
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		v, err := Acquire(ec, r)
		if err != nil {
			t.Fatal(err)
		}
		if v != 42 {
			t.Fatalf("expected 42, got %d", v)
		}
	})

	t.Run("error", func(t *testing.T) {
		r := NewResource("failing", func(ec *ExecContext) (int, error) {
			return 0, errors.New("factory error")
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		_, err := Acquire(ec, r)
		if err == nil || err.Error() != "factory error" {
			t.Fatalf("expected factory error, got %v", err)
		}
	})

	t.Run("caching", func(t *testing.T) {
		var calls atomic.Int32
		r := NewResource("cached", func(ec *ExecContext) (int, error) {
			calls.Add(1)
			return 99, nil
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		v1, _ := Acquire(ec, r)
		v2, _ := Acquire(ec, r)
		if v1 != v2 {
			t.Fatalf("values differ: %d vs %d", v1, v2)
		}
		if calls.Load() != 1 {
			t.Fatalf("factory should be called once, got %d", calls.Load())
		}
	})

	t.Run("panic_containment", func(t *testing.T) {
		r := NewResource("panicky", func(ec *ExecContext) (int, error) {
			panic("kaboom")
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		_, err := Acquire(ec, r)
		if err == nil {
			t.Fatal("expected error from panic")
		}
		if !strings.Contains(err.Error(), "kaboom") {
			t.Fatalf("expected error to contain 'kaboom', got %s", err.Error())
		}
	})

	t.Run("panic_error_type", func(t *testing.T) {
		r := NewResource("panic-err", func(ec *ExecContext) (int, error) {
			panic(errors.New("typed panic"))
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		_, err := Acquire(ec, r)
		if err == nil || err.Error() != "typed panic" {
			t.Fatalf("expected typed panic error, got %v", err)
		}
	})
}

func TestMustAcquire(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		r := NewResource("ok", func(ec *ExecContext) (string, error) {
			return "hello", nil
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		v := MustAcquire(ec, r)
		if v != "hello" {
			t.Fatalf("expected hello, got %s", v)
		}
	})

	t.Run("panics_on_error", func(t *testing.T) {
		r := NewResource("fail", func(ec *ExecContext) (int, error) {
			return 0, errors.New("must fail")
		})
		s := newTestScope(t)
		ec := s.CreateContext()
		defer ec.Close(nil)

		defer func() {
			p := recover()
			if p == nil {
				t.Fatal("expected panic from MustAcquire")
			}
		}()
		MustAcquire(ec, r)
	})
}

func TestResourceInheritance(t *testing.T) {
	t.Run("child_sees_parent", func(t *testing.T) {
		var calls atomic.Int32
		r := NewResource("shared", func(ec *ExecContext) (string, error) {
			calls.Add(1)
			return "from-parent", nil
		})
		s := newTestScope(t)
		parent := s.CreateContext()
		defer parent.Close(nil)

		// Acquire in parent
		v1, err := Acquire(parent, r)
		if err != nil {
			t.Fatal(err)
		}
		if v1 != "from-parent" {
			t.Fatalf("expected from-parent, got %s", v1)
		}

		// Child should inherit via SeekTag parent chain
		var childVal string
		_, err = ExecFn(parent, func(child *ExecContext) (int, error) {
			v, err := Acquire(child, r)
			if err != nil {
				return 0, err
			}
			childVal = v
			return 0, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if childVal != "from-parent" {
			t.Fatalf("child expected from-parent, got %s", childVal)
		}
		if calls.Load() != 1 {
			t.Fatalf("factory should be called once (child inherits), got %d", calls.Load())
		}
	})

	t.Run("grandchild_inherits", func(t *testing.T) {
		var calls atomic.Int32
		r := NewResource("deep", func(ec *ExecContext) (int, error) {
			calls.Add(1)
			return 100, nil
		})
		s := newTestScope(t)
		root := s.CreateContext()
		defer root.Close(nil)

		_, _ = Acquire(root, r)

		// Two levels deep — grandchild should still inherit
		var grandchildVal int
		_, _ = ExecFn(root, func(child *ExecContext) (int, error) {
			v, err := ExecFn(child, func(grandchild *ExecContext) (int, error) {
				return Acquire(grandchild, r)
			})
			grandchildVal = v
			return v, err
		})

		if grandchildVal != 100 {
			t.Fatalf("grandchild expected 100, got %d", grandchildVal)
		}
		if calls.Load() != 1 {
			t.Fatalf("factory should be called once across 3 levels, got %d", calls.Load())
		}
	})

	t.Run("separate_contexts_get_own", func(t *testing.T) {
		var calls atomic.Int32
		r := NewResource("per-ctx", func(ec *ExecContext) (int, error) {
			return int(calls.Add(1)), nil
		})
		s := newTestScope(t)

		ec1 := s.CreateContext()
		ec2 := s.CreateContext()

		v1, _ := Acquire(ec1, r)
		v2, _ := Acquire(ec2, r)

		ec1.Close(nil)
		ec2.Close(nil)

		if v1 == v2 {
			t.Fatal("separate root ExecContexts should get independent resources")
		}
		if calls.Load() != 2 {
			t.Fatalf("expected 2 factory calls for 2 independent contexts, got %d", calls.Load())
		}
	})
}

func TestResourceCleanup(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		var cleaned atomic.Bool
		r := NewResource("cleanable", func(ec *ExecContext) (string, error) {
			ec.OnClose(func(err error) error {
				cleaned.Store(true)
				return nil
			})
			return "val", nil
		})
		s := newTestScope(t)
		ec := s.CreateContext()

		_, err := Acquire(ec, r)
		if err != nil {
			t.Fatal(err)
		}

		if cleaned.Load() {
			t.Fatal("cleanup should not run before Close")
		}

		ec.Close(nil)

		if !cleaned.Load() {
			t.Fatal("cleanup should run on Close")
		}
	})

	t.Run("chained_cleanup_order", func(t *testing.T) {
		// Resources acquired in order A -> B (B depends on A).
		// Cleanups should run in reverse: B's cleanup before A's cleanup,
		// since ExecContext.Close runs cleanups in reverse registration order.
		var order []string
		var mu sync.Mutex
		appendOrder := func(s string) {
			mu.Lock()
			order = append(order, s)
			mu.Unlock()
		}

		rA := NewResource("chain-a", func(ec *ExecContext) (string, error) {
			ec.OnClose(func(err error) error {
				appendOrder("A-cleanup")
				return nil
			})
			return "A", nil
		})
		rB := NewResource("chain-b", func(ec *ExecContext) (string, error) {
			a, err := Acquire(ec, rA)
			if err != nil {
				return "", err
			}
			ec.OnClose(func(err error) error {
				appendOrder("B-cleanup")
				return nil
			})
			return a + "+B", nil
		})

		s := newTestScope(t)
		ec := s.CreateContext()

		_, err := Acquire(ec, rB)
		if err != nil {
			t.Fatal(err)
		}
		ec.Close(nil)

		mu.Lock()
		defer mu.Unlock()
		if len(order) != 2 {
			t.Fatalf("expected 2 cleanups, got %d", len(order))
		}
		// ExecContext.Close reverses cleanups, so B (registered last) runs first
		if order[0] != "B-cleanup" || order[1] != "A-cleanup" {
			t.Fatalf("expected [B-cleanup, A-cleanup], got %v", order)
		}
	})
}

func TestResourceChaining(t *testing.T) {
	rA := NewResource("res-a", func(ec *ExecContext) (string, error) {
		return "A", nil
	})
	rB := NewResource("res-b", func(ec *ExecContext) (string, error) {
		a, err := Acquire(ec, rA)
		if err != nil {
			return "", err
		}
		return a + "+B", nil
	})

	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	v, err := Acquire(ec, rB)
	if err != nil {
		t.Fatal(err)
	}
	if v != "A+B" {
		t.Fatalf("expected A+B, got %s", v)
	}

	// Verify A was also cached
	vA, _ := Acquire(ec, rA)
	if vA != "A" {
		t.Fatalf("expected A, got %s", vA)
	}
}

func TestResourceAccessesScopeAtoms(t *testing.T) {
	atom := NewAtom(func(rc *ResolveContext) (string, error) {
		return "scope-val", nil
	})

	r := NewResource("uses-atom", func(ec *ExecContext) (string, error) {
		v := MustResolve(ec.Scope(), atom)
		return "resource+" + v, nil
	})

	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	v, err := Acquire(ec, r)
	if err != nil {
		t.Fatal(err)
	}
	if v != "resource+scope-val" {
		t.Fatalf("expected resource+scope-val, got %s", v)
	}
}

func TestResourceFactoryNotCalledOnCacheHit(t *testing.T) {
	// Verify that on cache hit, we get back the exact same pointer
	type payload struct{ value int }

	r := NewResource("ptr-check", func(ec *ExecContext) (*payload, error) {
		return &payload{value: 42}, nil
	})
	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	p1, _ := Acquire(ec, r)
	p2, _ := Acquire(ec, r)

	if p1 != p2 {
		t.Fatal("cache hit should return the same pointer")
	}
}

func TestResourceErrorNotCached(t *testing.T) {
	var calls atomic.Int32
	r := NewResource("retry", func(ec *ExecContext) (int, error) {
		n := calls.Add(1)
		if n == 1 {
			return 0, errors.New("transient")
		}
		return 42, nil
	})
	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	// First call fails
	_, err := Acquire(ec, r)
	if err == nil {
		t.Fatal("expected error on first call")
	}

	// Second call should retry (errors are NOT cached)
	v, err := Acquire(ec, r)
	if err != nil {
		t.Fatalf("expected success on retry, got %v", err)
	}
	if v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
}

func TestResourceWithCancelledContext(t *testing.T) {
	r := NewResource("ctx-cancel", func(ec *ExecContext) (int, error) {
		return 1, ec.Context().Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	s := NewScope(ctx)
	_ = s.Ready()
	t.Cleanup(func() { _ = s.Dispose() })

	ec := s.CreateContext()
	defer ec.Close(nil)

	_, err := Acquire(ec, r)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestResourceName(t *testing.T) {
	r := NewResource("my-resource", func(ec *ExecContext) (int, error) {
		return 1, nil
	})
	if r.Name() != "my-resource" {
		t.Fatalf("expected my-resource, got %s", r.Name())
	}
}

func TestResourceID(t *testing.T) {
	r1 := NewResource("res-1", func(ec *ExecContext) (int, error) {
		return 1, nil
	})
	r2 := NewResource("res-2", func(ec *ExecContext) (int, error) {
		return 2, nil
	})
	if r1.ID() == 0 {
		t.Fatal("expected non-zero ID")
	}
	if r1.ID() == r2.ID() {
		t.Fatal("expected unique IDs for different resources")
	}
}

func TestResourceConcurrentAcquire(t *testing.T) {
	var calls atomic.Int32
	r := NewResource("concurrent", func(ec *ExecContext) (int, error) {
		calls.Add(1)
		return 42, nil
	})
	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]int, goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = Acquire(ec, r)
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

func TestResourceConcurrentAcquireMultiple(t *testing.T) {
	// Multiple different resources acquired concurrently on the same ExecContext
	const numResources = 5
	const goroutinesPerResource = 10

	resources := make([]*Resource[int], numResources)
	callCounts := make([]*atomic.Int32, numResources)
	for i := 0; i < numResources; i++ {
		callCounts[i] = &atomic.Int32{}
		idx := i
		counter := callCounts[i]
		resources[i] = NewResource("concurrent-multi", func(ec *ExecContext) (int, error) {
			counter.Add(1)
			return idx * 10, nil
		})
	}

	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	var wg sync.WaitGroup
	for i := 0; i < numResources; i++ {
		for j := 0; j < goroutinesPerResource; j++ {
			wg.Add(1)
			go func(resIdx int) {
				defer wg.Done()
				v, err := Acquire(ec, resources[resIdx])
				if err != nil {
					t.Errorf("resource %d: %v", resIdx, err)
					return
				}
				if v != resIdx*10 {
					t.Errorf("resource %d: expected %d, got %d", resIdx, resIdx*10, v)
				}
			}(i)
		}
	}
	wg.Wait()

	for i, counter := range callCounts {
		c := counter.Load()
		if c < 1 {
			t.Errorf("resource %d: factory never called", i)
		}
	}
}

func TestResourceInsideFlow(t *testing.T) {
	r := NewResource("flow-resource", func(ec *ExecContext) (string, error) {
		return "acquired", nil
	})

	flow := NewFlow(func(ec *ExecContext, input string) (string, error) {
		v, err := Acquire(ec, r)
		if err != nil {
			return "", err
		}
		return input + "+" + v, nil
	})

	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	result, err := ExecFlow(ec, flow, "test")
	if err != nil {
		t.Fatal(err)
	}
	if result != "test+acquired" {
		t.Fatalf("expected test+acquired, got %s", result)
	}
}

func TestResourceSharedAcrossFlows(t *testing.T) {
	// A resource acquired in one flow should be visible to sibling flows
	// executed from the same parent ExecContext, only if acquired at the parent level.
	var calls atomic.Int32
	r := NewResource("shared-flow", func(ec *ExecContext) (string, error) {
		calls.Add(1)
		return "shared-value", nil
	})

	flowA := NewFlow(func(ec *ExecContext, _ struct{}) (string, error) {
		return Acquire(ec, r)
	})
	flowB := NewFlow(func(ec *ExecContext, _ struct{}) (string, error) {
		return Acquire(ec, r)
	})

	s := newTestScope(t)
	ec := s.CreateContext()
	defer ec.Close(nil)

	// Acquire at parent level first
	_, err := Acquire(ec, r)
	if err != nil {
		t.Fatal(err)
	}

	vA, err := ExecFlow(ec, flowA, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	vB, err := ExecFlow(ec, flowB, struct{}{})
	if err != nil {
		t.Fatal(err)
	}

	if vA != "shared-value" || vB != "shared-value" {
		t.Fatalf("expected shared-value from both flows, got %q and %q", vA, vB)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory should be called once (inherited by both flows), got %d", calls.Load())
	}
}

// --- Benchmarks ---

func BenchmarkAcquireCacheHit(b *testing.B) {
	r := NewResource("bench", func(ec *ExecContext) (int, error) {
		return 42, nil
	})
	s := NewScope(context.Background())
	_ = s.Ready()
	defer func() { _ = s.Dispose() }()

	ec := s.CreateContext()
	defer ec.Close(nil)

	// Prime the cache
	_, _ = Acquire(ec, r)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Acquire(ec, r)
	}
}

func BenchmarkAcquireInheritedCacheHit(b *testing.B) {
	r := NewResource("bench-inherited", func(ec *ExecContext) (int, error) {
		return 42, nil
	})
	s := NewScope(context.Background())
	_ = s.Ready()
	defer func() { _ = s.Dispose() }()

	parent := s.CreateContext()
	defer parent.Close(nil)

	// Acquire at parent level
	_, _ = Acquire(parent, r)

	// Benchmark child ExecContext cache hits through parent chain
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate child ContextData looking up through parent
		childData := newContextData(parent.Data())
		if _, ok := SeekTag(childData, r.tag); !ok {
			b.Fatal("expected inherited cache hit")
		}
	}
}

func BenchmarkAcquireFirstCall(b *testing.B) {
	s := NewScope(context.Background())
	_ = s.Ready()
	defer func() { _ = s.Dispose() }()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewResource("bench-first", func(ec *ExecContext) (int, error) {
			return i, nil
		})
		ec := s.CreateContext()
		_, _ = Acquire(ec, r)
		ec.Close(nil)
	}
}

func BenchmarkAcquireConcurrent(b *testing.B) {
	r := NewResource("bench-concurrent", func(ec *ExecContext) (int, error) {
		return 42, nil
	})
	s := NewScope(context.Background())
	_ = s.Ready()
	defer func() { _ = s.Dispose() }()

	ec := s.CreateContext()
	defer ec.Close(nil)

	// Prime the cache
	_, _ = Acquire(ec, r)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = Acquire(ec, r)
		}
	})
}
