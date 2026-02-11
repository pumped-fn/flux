package flux

import "sync"

// FlowDep[T] is a typed dependency that can be resolved from an ExecContext.
// Both *Atom[T] and *Resource[T] implement this interface, so NewFlowFrom
// and NewFlowFrom2 accept either as a dependency.
type FlowDep[T any] interface {
	Resolvable
	resolveForFlow(ec *ExecContext) (T, error)
}

// flowAnyDep is the untyped counterpart of FlowDep, used by NewFlowUnsafe
// for reflection-based dependency resolution.
type flowAnyDep interface {
	resolveForFlowAny(ec *ExecContext) (any, error)
}

// Resource represents a lazily-resolved, ExecContext-scoped value.
// Unlike Atom (which is cached per Scope), a Resource is cached per ExecContext
// and inherited by child contexts via the ContextData parent chain.
type Resource[T any] struct {
	id       uint64
	tag      *Tag[T]
	guardTag *Tag[*resourceGuard]
	name     string
	factory  func(*ExecContext) (T, error)
}

// resourceGuard ensures at most one concurrent factory execution per
// ExecContext. Successful results are cached via the value tag;
// errors are NOT cached, allowing retries.
type resourceGuard struct {
	mu sync.Mutex
}

// NewResource creates a new Resource with the given name and factory function.
// The factory receives the ExecContext and can access scope-level atoms,
// register cleanup via OnClose, and chain to other resources via Acquire.
func NewResource[T any](name string, factory func(*ExecContext) (T, error)) *Resource[T] {
	return &Resource[T]{
		id:       globalIDCounter.Add(1),
		tag:      NewTag[T]("resource:" + name),
		guardTag: NewTag[*resourceGuard]("resource-guard:" + name),
		name:     name,
		factory:  factory,
	}
}

// Acquire lazily resolves a Resource within an ExecContext.
// On first call, runs the factory and caches the result in ec.Data().
// Subsequent calls (or calls from child ExecContexts) return the cached value.
// Safe for concurrent use from multiple goroutines on the same ExecContext.
// Errors are NOT cached — a failed Acquire can be retried.
func Acquire[T any](ec *ExecContext, r *Resource[T]) (T, error) {
	// Fast path: already resolved (checks parent chain).
	if v, ok := SeekTag(ec.Data(), r.tag); ok {
		return v, nil
	}

	// Slow path: serialize concurrent callers via a per-resource guard.
	guard := GetOrSetTag(ec.Data(), r.guardTag, &resourceGuard{})
	guard.mu.Lock()
	defer guard.mu.Unlock()

	// Double-check after acquiring the lock.
	if v, ok := SeekTag(ec.Data(), r.tag); ok {
		return v, nil
	}

	val, err := func() (ret T, retErr error) {
		defer func() {
			if p := recover(); p != nil {
				retErr = asError(p)
			}
		}()
		return r.factory(ec)
	}()
	if err != nil {
		var zero T
		return zero, err
	}

	SetTag(ec.Data(), r.tag, val)
	return val, nil
}

// Name returns the resource's name.
func (r *Resource[T]) Name() string { return r.name }

// ID returns the resource's unique identifier.
func (r *Resource[T]) ID() uint64 { return r.id }

// resolveForFlow implements FlowDep[T] for Resource.
func (r *Resource[T]) resolveForFlow(ec *ExecContext) (T, error) {
	return Acquire(ec, r)
}

// resolveForFlowAny is the untyped version used by NewFlowUnsafe.
func (r *Resource[T]) resolveForFlowAny(ec *ExecContext) (any, error) {
	return Acquire(ec, r)
}

// NewResourceFrom creates a Resource with one dependency (Resource or Atom).
func NewResourceFrom[D1, T any](dep1 FlowDep[D1], name string, factory func(*ExecContext, D1) (T, error)) *Resource[T] {
	if dep1 == nil {
		panic("flux: NewResourceFrom: dep1 must not be nil")
	}
	return NewResource(name, func(ec *ExecContext) (T, error) {
		v1, err := dep1.resolveForFlow(ec)
		if err != nil {
			var zero T
			return zero, err
		}
		return factory(ec, v1)
	})
}

// NewResourceFrom2 creates a Resource with two dependencies (Resource or Atom).
func NewResourceFrom2[D1, D2, T any](dep1 FlowDep[D1], dep2 FlowDep[D2], name string, factory func(*ExecContext, D1, D2) (T, error)) *Resource[T] {
	if dep1 == nil {
		panic("flux: NewResourceFrom2: dep1 must not be nil")
	}
	if dep2 == nil {
		panic("flux: NewResourceFrom2: dep2 must not be nil")
	}
	return NewResource(name, func(ec *ExecContext) (T, error) {
		v1, err := dep1.resolveForFlow(ec)
		if err != nil {
			var zero T
			return zero, err
		}
		v2, err := dep2.resolveForFlow(ec)
		if err != nil {
			var zero T
			return zero, err
		}
		return factory(ec, v1, v2)
	})
}

// MustAcquire is like Acquire but panics on error.
func MustAcquire[T any](ec *ExecContext, r *Resource[T]) T {
	v, err := Acquire(ec, r)
	if err != nil {
		panic(err)
	}
	return v
}
