package flux

import "fmt"

type atomBase struct {
	id        uint64
	name      string
	keepAlive bool
	tags      []AnyTagged
	deps      []AnyAtom
}

func (a *atomBase) atomID() uint64        { return a.id }
func (a *atomBase) atomName() string      { return a.name }
func (a *atomBase) atomKeepAlive() bool   { return a.keepAlive }
func (a *atomBase) atomTags() []AnyTagged { return a.tags }
func (a *atomBase) Name() string          { return a.name }
func (a *atomBase) ID() uint64            { return a.id }
func (a *atomBase) Deps() []AnyAtom       { return a.deps }

type Atom[T any] struct {
	atomBase
	factory   func(*ResolveContext) (T, error)
	tagSource *Tag[T]
}

func (a *Atom[T]) runFactory(si *scopeImpl, stack *resolveStack) (any, error) {
	si.mu.Lock()
	entry, exists := si.cache[a.atomID()]
	if !exists {
		entry = newAtomEntry()
		si.cache[a.atomID()] = entry
	}
	si.atomRegistry[a.atomID()] = a
	si.mu.Unlock()

	entry.mu.Lock()
	if entry.state == StateResolved && entry.hasValue {
		v := entry.value
		entry.mu.Unlock()
		return v, nil
	}

	wasResolving := entry.state == StateResolving
	entry.state = StateResolving
	if entry.data == nil {
		entry.data = newContextData(nil)
		if a.atomName() != "" {
			SetTag(entry.data, NameTag, a.atomName())
		}
		applyTags(entry.data, a.atomTags())
		applyTagsIfAbsent(entry.data, si.tags)
	}
	entry.mu.Unlock()

	if !wasResolving {
		si.emitState(StateResolving, a)
		si.notifyListeners(a, EventResolving)
	}

	newStack := stack.push(a.atomID())

	rc := &ResolveContext{
		ctx:   si.ctx,
		scope: si,
		stack: newStack,
		cleanup: func(fn func() error) {
			entry.mu.Lock()
			entry.cleanups = append(entry.cleanups, fn)
			entry.mu.Unlock()
		},
		inv: func() {
			si.invalidateAtom(a)
		},
		data: entry.data,
		atom: a,
	}

	doResolve := func() (any, error) {
		return a.factory(rc)
	}

	val, err := func() (retVal any, retErr error) {
		defer func() {
			if r := recover(); r != nil {
				retErr = asError(r)
			}
		}()
		return si.applyResolveExtensions(a, doResolve)
	}()

	if err != nil {
		entry.mu.Lock()
		entry.state = StateFailed
		entry.err = err
		entry.value = nil
		entry.hasValue = false
		pend := entry.pendingInvalidate
		entry.pendingInvalidate = false
		hasPendingSet := entry.pendingSet != nil
		entry.mu.Unlock()
		si.emitState(StateFailed, a)
		si.notifyAllOnly(a)
		if pend || hasPendingSet {
			si.invMu.Lock()
			if si.invalidationChain != nil {
				delete(si.invalidationChain, a.atomID())
			}
			si.invMu.Unlock()
			si.scheduleInvalidation(a)
		}
		return nil, err
	}

	entry.mu.Lock()
	entry.state = StateResolved
	entry.value = val
	entry.hasValue = true
	entry.err = nil
	pend := entry.pendingInvalidate
	entry.pendingInvalidate = false
	hasPendingSet := entry.pendingSet != nil
	entry.mu.Unlock()

	si.emitState(StateResolved, a)
	si.notifyListeners(a, EventResolved)

	if pend || hasPendingSet {
		si.invMu.Lock()
		if si.invalidationChain != nil {
			delete(si.invalidationChain, a.atomID())
		}
		si.invMu.Unlock()
		si.scheduleInvalidation(a)
	}

	return val, nil
}

type AtomOption func(*atomBase)

func WithKeepAlive() AtomOption {
	return func(b *atomBase) { b.keepAlive = true }
}

func WithAtomName(name string) AtomOption {
	return func(b *atomBase) { b.name = name }
}

func WithAtomTags(tags ...AnyTagged) AtomOption {
	return func(b *atomBase) { b.tags = tags }
}

func (a *Atom[T]) resolveFromData(data *ContextData) (any, bool, error) {
	if a.tagSource == nil {
		return nil, false, nil
	}
	v, ok := SeekTag(data, a.tagSource)
	if ok {
		return v, true, nil
	}
	if a.tagSource.hasDefault {
		return a.tagSource.defaultVal, true, nil
	}
	return nil, false, fmt.Errorf("required tag %q not found in context", a.tagSource.Label())
}

func resolveAtomDep[T any](rc *ResolveContext, dep *Atom[T]) (T, error) {
	if dep.tagSource != nil {
		return dep.tagSource.Get(rc.Scope().Tags())
	}
	return ResolveFrom(rc, dep)
}

func NewAtom[T any](factory func(*ResolveContext) (T, error), opts ...AtomOption) *Atom[T] {
	a := &Atom[T]{
		atomBase: atomBase{id: globalIDCounter.Add(1)},
		factory:  factory,
	}
	for _, opt := range opts {
		opt(&a.atomBase)
	}
	return a
}

func NewAtomFrom[D1, T any](dep1 *Atom[D1], factory func(*ResolveContext, D1) (T, error), opts ...AtomOption) *Atom[T] {
	a := &Atom[T]{
		atomBase: atomBase{
			id:   globalIDCounter.Add(1),
			deps: []AnyAtom{dep1},
		},
		factory: func(rc *ResolveContext) (T, error) {
			v1, err := resolveAtomDep(rc, dep1)
			if err != nil {
				var zero T
				return zero, err
			}
			return factory(rc, v1)
		},
	}
	for _, opt := range opts {
		opt(&a.atomBase)
	}
	return a
}

func NewAtomFrom2[D1, D2, T any](dep1 *Atom[D1], dep2 *Atom[D2], factory func(*ResolveContext, D1, D2) (T, error), opts ...AtomOption) *Atom[T] {
	a := &Atom[T]{
		atomBase: atomBase{
			id:   globalIDCounter.Add(1),
			deps: []AnyAtom{dep1, dep2},
		},
		factory: func(rc *ResolveContext) (T, error) {
			v1, err := resolveAtomDep(rc, dep1)
			if err != nil {
				var zero T
				return zero, err
			}
			v2, err := resolveAtomDep(rc, dep2)
			if err != nil {
				var zero T
				return zero, err
			}
			return factory(rc, v1, v2)
		},
	}
	for _, opt := range opts {
		opt(&a.atomBase)
	}
	return a
}

func NewAtomFrom3[D1, D2, D3, T any](dep1 *Atom[D1], dep2 *Atom[D2], dep3 *Atom[D3], factory func(*ResolveContext, D1, D2, D3) (T, error), opts ...AtomOption) *Atom[T] {
	a := &Atom[T]{
		atomBase: atomBase{
			id:   globalIDCounter.Add(1),
			deps: []AnyAtom{dep1, dep2, dep3},
		},
		factory: func(rc *ResolveContext) (T, error) {
			v1, err := resolveAtomDep(rc, dep1)
			if err != nil {
				var zero T
				return zero, err
			}
			v2, err := resolveAtomDep(rc, dep2)
			if err != nil {
				var zero T
				return zero, err
			}
			v3, err := resolveAtomDep(rc, dep3)
			if err != nil {
				var zero T
				return zero, err
			}
			return factory(rc, v1, v2, v3)
		},
	}
	for _, opt := range opts {
		opt(&a.atomBase)
	}
	return a
}
