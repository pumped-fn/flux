package flux

type Controller[T any] struct {
	atom  *Atom[T]
	scope *scopeImpl
}

func (c *Controller[T]) State() AtomState {
	c.scope.mu.RLock()
	entry, ok := c.scope.cache[c.atom.id]
	c.scope.mu.RUnlock()
	if !ok {
		return StateIdle
	}
	entry.mu.RLock()
	s := entry.state
	entry.mu.RUnlock()
	return s
}

func (c *Controller[T]) Get() (T, error) {
	c.scope.mu.RLock()
	entry, ok := c.scope.cache[c.atom.id]
	c.scope.mu.RUnlock()
	if !ok {
		var zero T
		return zero, ErrNotResolved
	}
	entry.mu.RLock()
	state := entry.state
	hasValue := entry.hasValue
	value := entry.value
	err := entry.err
	entry.mu.RUnlock()

	switch state {
	case StateIdle:
		var zero T
		return zero, ErrNotResolved
	case StateFailed:
		var zero T
		if err != nil {
			return zero, err
		}
		return zero, ErrNotResolved
	case StateResolving, StateResolved:
		if hasValue {
			return typedValue[T](value), nil
		}
		var zero T
		return zero, ErrNotResolved
	default:
		var zero T
		return zero, ErrNotResolved
	}
}

func (c *Controller[T]) Resolve() (T, error) {
	return Resolve[T](c.scope, c.atom)
}

func (c *Controller[T]) Release() error {
	return Release[T](c.scope, c.atom)
}

func (c *Controller[T]) Invalidate() {
	c.scope.invalidateAtom(c.atom)
}

func (c *Controller[T]) Set(value T) error {
	return c.scope.scheduleSet(c.atom, value)
}

func (c *Controller[T]) Update(fn func(T) T) error {
	return c.scope.scheduleUpdate(c.atom, &updateFnWrapper[T]{fn: fn})
}

func (c *Controller[T]) On(event ControllerEvent, listener func()) UnsubscribeFunc {
	return c.scope.addListener(c.atom, event, listener)
}

func GetController[T any](s Scope, atom *Atom[T]) *Controller[T] {
	si := mustScopeImpl(s)
	si.mu.Lock()
	if existing, ok := si.controllers[atom.id]; ok {
		si.mu.Unlock()
		return existing.(*Controller[T])
	}
	ctrl := &Controller[T]{atom: atom, scope: si}
	si.controllers[atom.id] = ctrl
	si.mu.Unlock()
	return ctrl
}

func GetControllerResolved[T any](s Scope, atom *Atom[T]) (*Controller[T], error) {
	ctrl := GetController[T](s, atom)
	_, err := ctrl.Resolve()
	if err != nil {
		return nil, err
	}
	return ctrl, nil
}
