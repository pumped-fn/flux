package flux

func Resolve[T any](scope Scope, atom *Atom[T]) (T, error) {
	si := mustScopeImpl(scope)
	if err := si.Ready(); err != nil {
		var zero T
		return zero, err
	}
	if si.disposed.Load() {
		var zero T
		return zero, ErrScopeClosed
	}
	return resolveInternal[T](si, atom, nil)
}

func ResolveFrom[T any](rc *ResolveContext, atom *Atom[T]) (T, error) {
	return resolveInternal[T](rc.scope, atom, rc.stack)
}

func MustResolve[T any](scope Scope, atom *Atom[T]) T {
	v, err := Resolve[T](scope, atom)
	if err != nil {
		panic(err)
	}
	return v
}

func Release[T any](scope Scope, atom *Atom[T]) error {
	si := mustScopeImpl(scope)
	return si.releaseByID(atom.id)
}

func ResolveAny(scope Scope, atom AnyAtom) (any, error) {
	si := mustScopeImpl(scope)
	if err := si.Ready(); err != nil {
		return nil, err
	}
	if si.disposed.Load() {
		return nil, ErrScopeClosed
	}
	return resolveInternalAny(si, atom, nil)
}

func ResolveAnyFrom(rc *ResolveContext, atom AnyAtom) (any, error) {
	return resolveInternalAny(rc.scope, atom, rc.stack)
}

func resolveInternal[T any](si *scopeImpl, atom *Atom[T], stack *resolveStack) (T, error) {
	val, err := resolveInternalAny(si, atom, stack)
	if err != nil {
		var zero T
		return zero, err
	}
	return typedValue[T](val), nil
}

func resolveInternalAny(si *scopeImpl, atom AnyAtom, stack *resolveStack) (any, error) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()

	if exists {
		entry.mu.RLock()
		if entry.state == StateResolved && entry.hasValue {
			v := entry.value
			entry.mu.RUnlock()
			trackDependent(si, atom, stack)
			return v, nil
		}
		entry.mu.RUnlock()
	}

	if stack != nil && stack.contains(atom.atomID()) {
		path := append(stack.path(), atom.atomID())
		return nil, &CircularDepError{Path: path}
	}

	p, hasPreset := si.presetMap[atom.atomID()]
	if hasPreset {
		switch p.kind {
		case presetValue:
			si.mu.Lock()
			e, ok := si.cache[atom.atomID()]
			if !ok {
				e = newAtomEntry()
				si.cache[atom.atomID()] = e
			}
			si.atomRegistry[atom.atomID()] = atom
			si.mu.Unlock()
			e.mu.Lock()
			e.state = StateResolved
			e.value = p.value
			e.hasValue = true
			e.err = nil
			e.mu.Unlock()
			si.emitState(StateResolved, atom)
			si.notifyListeners(atom, EventResolved)
			trackDependent(si, atom, stack)
			si.maybeScheduleGC(atom)
			return p.value, nil
		case presetAtomRedirect:
			replacement := p.value.(AnyAtom)
			si.mu.Lock()
			si.atomRegistry[replacement.atomID()] = replacement
			si.mu.Unlock()
			presetStack := stack
			if presetStack == nil {
				presetStack = &resolveStack{}
			}
			presetStack = presetStack.push(atom.atomID())
			val, err := resolveInternalAny(si, replacement, presetStack)
			if err != nil {
				si.mu.Lock()
				e, ok := si.cache[atom.atomID()]
				if !ok {
					e = newAtomEntry()
					si.cache[atom.atomID()] = e
				}
				si.atomRegistry[atom.atomID()] = atom
				si.mu.Unlock()
				e.mu.Lock()
				e.state = StateFailed
				e.err = err
				e.value = nil
				e.hasValue = false
				e.mu.Unlock()
				si.emitState(StateFailed, atom)
				si.notifyAllOnly(atom)
				return nil, err
			}
			si.mu.Lock()
			e, ok := si.cache[atom.atomID()]
			if !ok {
				e = newAtomEntry()
				si.cache[atom.atomID()] = e
			}
			si.atomRegistry[atom.atomID()] = atom
			si.mu.Unlock()
			e.mu.Lock()
			e.state = StateResolved
			e.value = val
			e.hasValue = true
			e.err = nil
			e.mu.Unlock()
			si.emitState(StateResolved, atom)
			si.notifyListeners(atom, EventResolved)
			trackDependent(si, atom, stack)

			si.mu.RLock()
			repEntry, repExists := si.cache[replacement.atomID()]
			si.mu.RUnlock()
			if repExists {
				repEntry.mu.Lock()
				repEntry.dependents[atom.atomID()] = true
				repEntry.mu.Unlock()
			}

			si.maybeScheduleGC(atom)
			return val, nil
		}
	}

	si.mu.Lock()
	if wait, ok := si.singleflight[atom.atomID()]; ok {
		si.mu.Unlock()
		select {
		case <-wait:
		case <-si.ctx.Done():
			return nil, si.ctx.Err()
		}
		si.mu.RLock()
		entry, exists = si.cache[atom.atomID()]
		si.mu.RUnlock()
		if exists {
			entry.mu.RLock()
			if entry.state == StateResolved && entry.hasValue {
				v := entry.value
				entry.mu.RUnlock()
				trackDependent(si, atom, stack)
				return v, nil
			}
			if entry.state == StateFailed && entry.err != nil {
				err := entry.err
				entry.mu.RUnlock()
				return nil, err
			}
			entry.mu.RUnlock()
		}
		val, err := si.resolveUncached(atom, stack)
		if err == nil {
			trackDependent(si, atom, stack)
			si.maybeScheduleGC(atom)
		}
		return val, err
	}

	wait := make(chan struct{})
	si.singleflight[atom.atomID()] = wait
	si.mu.Unlock()

	val, err := si.resolveUncached(atom, stack)

	si.mu.Lock()
	delete(si.singleflight, atom.atomID())
	si.mu.Unlock()
	close(wait)

	if err == nil {
		trackDependent(si, atom, stack)
		si.maybeScheduleGC(atom)
	}
	return val, err
}

func (si *scopeImpl) resolveUncached(atom AnyAtom, stack *resolveStack) (any, error) {
	type atomFactory interface {
		runFactory(si *scopeImpl, stack *resolveStack) (any, error)
	}

	if af, ok := atom.(atomFactory); ok {
		return af.runFactory(si, stack)
	}
	return nil, ErrNotResolved
}

func trackDependent(si *scopeImpl, atom AnyAtom, stack *resolveStack) {
	if stack == nil || len(stack.ids) == 0 {
		return
	}
	parentID := stack.ids[len(stack.ids)-1]
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}
	entry.mu.Lock()
	entry.dependents[parentID] = true
	entry.mu.Unlock()
}
