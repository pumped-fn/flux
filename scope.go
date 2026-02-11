package flux

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type atomEntry struct {
	mu                sync.RWMutex
	state             AtomState
	value             any
	hasValue          bool
	err               error
	cleanups          []func() error
	listeners         [eventCount][]func()
	notifying         int
	pendingInvalidate bool
	pendingSet        *pendingSetOp
	data              *ContextData
	dependents        map[uint64]bool
	gcTimer           *time.Timer
}

type pendingSetKind int

const (
	pendingSetValue pendingSetKind = iota
	pendingSetFn
)

type pendingSetOp struct {
	kind  pendingSetKind
	value any
	fn    any
}

func newAtomEntry() *atomEntry {
	return &atomEntry{
		dependents: make(map[uint64]bool),
	}
}

type ScopeOption func(*scopeOpts)

type scopeOpts struct {
	extensions []Extension
	tags       []AnyTagged
	presets    []PresetOption
	gc         *GCOptions
}

func WithExtensions(exts ...Extension) ScopeOption {
	return func(o *scopeOpts) { o.extensions = exts }
}

func WithScopeTags(tags ...AnyTagged) ScopeOption {
	return func(o *scopeOpts) { o.tags = tags }
}

func WithPresets(presets ...PresetOption) ScopeOption {
	return func(o *scopeOpts) { o.presets = presets }
}

func WithGC(opts GCOptions) ScopeOption {
	return func(o *scopeOpts) { o.gc = &opts }
}

type scopeImpl struct {
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.RWMutex
	cache          map[uint64]*atomEntry
	presetMap      map[uint64]PresetOption
	controllers    map[uint64]any
	stateListeners map[AtomState]map[uint64][]func()
	atomRegistry   map[uint64]AnyAtom
	singleflight   map[uint64]chan struct{}

	extensions []Extension
	tags       []AnyTagged
	gcOpts     GCOptions
	disposed   atomic.Bool

	readyOnce sync.Once
	readyErr  error
	readyCh   chan struct{}

	invMu             sync.Mutex
	invalidationQueue []AnyAtom
	invalidationChain map[uint64]bool
	chainRunning      bool
	chainDone         chan struct{}
}

func NewScope(parentCtx context.Context, opts ...ScopeOption) Scope {
	so := &scopeOpts{}
	for _, opt := range opts {
		opt(so)
	}

	gc := defaultGCOptions()
	if so.gc != nil {
		gc = *so.gc
	}

	ctx, cancel := context.WithCancel(parentCtx)

	si := &scopeImpl{
		ctx:            ctx,
		cancel:         cancel,
		cache:          make(map[uint64]*atomEntry),
		presetMap:      make(map[uint64]PresetOption),
		controllers:    make(map[uint64]any),
		stateListeners: make(map[AtomState]map[uint64][]func()),
		atomRegistry:   make(map[uint64]AnyAtom),
		singleflight:   make(map[uint64]chan struct{}),
		extensions:     so.extensions,
		tags:           so.tags,
		gcOpts:         gc,
		readyCh:        make(chan struct{}),
	}

	for _, p := range so.presets {
		si.presetMap[p.targetID] = p
	}

	go func() {
		si.readyOnce.Do(func() {
			defer close(si.readyCh)
			si.readyErr = si.initExtensions()
		})
	}()

	return si
}

func (si *scopeImpl) Ready() error {
	<-si.readyCh
	return si.readyErr
}

func (si *scopeImpl) ReadyErr() error {
	<-si.readyCh
	return si.readyErr
}

func (si *scopeImpl) Tags() []AnyTagged { return si.tags }

func (si *scopeImpl) CreateContext(opts ...CreateContextOption) *ExecContext {
	cfg := &ecOptions{}
	for _, opt := range opts {
		opt(cfg)
	}

	data := newContextData(nil)
	applyTags(data, cfg.tags)
	applyTagsIfAbsent(data, si.tags)

	childCtx, childCancel := context.WithCancel(si.ctx)

	return &ExecContext{
		ctx:    childCtx,
		cancel: childCancel,
		scope:  si,
		data:   data,
	}
}

func (si *scopeImpl) On(state AtomState, atom AnyAtom, listener func()) UnsubscribeFunc {
	si.mu.Lock()
	stateMap, ok := si.stateListeners[state]
	if !ok {
		stateMap = make(map[uint64][]func())
		si.stateListeners[state] = stateMap
	}
	stateMap[atom.atomID()] = append(stateMap[atom.atomID()], listener)
	idx := len(stateMap[atom.atomID()]) - 1
	si.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			si.mu.Lock()
			if sm, ok := si.stateListeners[state]; ok {
				ls := sm[atom.atomID()]
				if idx < len(ls) {
					ls[idx] = nil
				}
				allNil := true
				for _, l := range ls {
					if l != nil {
						allNil = false
						break
					}
				}
				if allNil {
					delete(sm, atom.atomID())
				}
			}
			si.mu.Unlock()
		})
	}
}

func (si *scopeImpl) emitState(state AtomState, atom AnyAtom) {
	si.mu.RLock()
	sm, ok := si.stateListeners[state]
	if !ok {
		si.mu.RUnlock()
		return
	}
	listeners := slices.Clone(sm[atom.atomID()])
	si.mu.RUnlock()

	si.withAtomNotification(atom, func() {
		for _, fn := range listeners {
			if fn == nil {
				continue
			}
			func() {
				defer func() { recover() }()
				fn()
			}()
		}
	})
}

func (si *scopeImpl) addListener(atom AnyAtom, event ControllerEvent, listener func()) UnsubscribeFunc {
	si.cancelScheduledGC(atom)

	si.mu.Lock()
	entry, exists := si.cache[atom.atomID()]
	if !exists {
		entry = newAtomEntry()
		si.cache[atom.atomID()] = entry
	}
	si.mu.Unlock()

	entry.mu.Lock()
	entry.listeners[event] = append(entry.listeners[event], listener)
	idx := len(entry.listeners[event]) - 1
	entry.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Lock()
			if idx < len(entry.listeners[event]) {
				entry.listeners[event][idx] = nil
			}
			entry.mu.Unlock()
			si.maybeScheduleGC(atom)
		})
	}
}

func (si *scopeImpl) notifyListeners(atom AnyAtom, event ControllerEvent) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.RLock()
	eventListeners := slices.Clone(entry.listeners[event])
	allListeners := slices.Clone(entry.listeners[EventAll])
	entry.mu.RUnlock()

	si.withAtomNotification(atom, func() {
		for _, fn := range eventListeners {
			if fn == nil {
				continue
			}
			func() {
				defer func() { recover() }()
				fn()
			}()
		}
		for _, fn := range allListeners {
			if fn == nil {
				continue
			}
			func() {
				defer func() { recover() }()
				fn()
			}()
		}
	})
}

func (si *scopeImpl) notifyAllOnly(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.RLock()
	listeners := slices.Clone(entry.listeners[EventAll])
	entry.mu.RUnlock()

	si.withAtomNotification(atom, func() {
		for _, fn := range listeners {
			if fn == nil {
				continue
			}
			func() {
				defer func() { recover() }()
				fn()
			}()
		}
	})
}

func (si *scopeImpl) withAtomNotification(atom AnyAtom, fn func()) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		fn()
		return
	}

	entry.mu.Lock()
	entry.notifying++
	entry.mu.Unlock()
	defer func() {
		entry.mu.Lock()
		entry.notifying--
		entry.mu.Unlock()
	}()

	fn()
}

func (si *scopeImpl) invalidateAtom(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.Lock()
	if entry.state == StateIdle {
		entry.mu.Unlock()
		return
	}
	if entry.state == StateResolving {
		entry.pendingInvalidate = true
		entry.mu.Unlock()
		return
	}
	entry.mu.Unlock()

	si.scheduleInvalidation(atom)
}

func (si *scopeImpl) scheduleInvalidation(atom AnyAtom) {
	si.cancelScheduledGC(atom)

	si.invMu.Lock()
	si.invalidationQueue = append(si.invalidationQueue, atom)

	if !si.chainRunning {
		si.chainRunning = true
		si.invalidationChain = make(map[uint64]bool)
		si.chainDone = make(chan struct{})
		done := si.chainDone
		si.invMu.Unlock()
		go func() {
			defer close(done)
			si.processInvalidationChain()
		}()
	} else {
		si.invMu.Unlock()
	}
}

func (si *scopeImpl) processInvalidationChain() {
	for {
		si.invMu.Lock()
		if len(si.invalidationQueue) == 0 {
			si.chainRunning = false
			si.invalidationChain = nil
			si.invMu.Unlock()
			return
		}
		atom := si.invalidationQueue[0]
		si.invalidationQueue = si.invalidationQueue[1:]

		if si.invalidationChain[atom.atomID()] {
			si.invMu.Unlock()
			si.mu.RLock()
			entry, exists := si.cache[atom.atomID()]
			si.mu.RUnlock()
			if exists {
				entry.mu.RLock()
				hasPending := entry.pendingInvalidate || entry.pendingSet != nil
				entry.mu.RUnlock()
				if hasPending {
					si.doInvalidateSequential(atom)
				}
			}
			continue
		}

		si.invalidationChain[atom.atomID()] = true
		si.invMu.Unlock()

		si.doInvalidateSequential(atom)
	}
}

func (si *scopeImpl) doInvalidateSequential(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.Lock()
	if entry.state == StateIdle {
		entry.mu.Unlock()
		return
	}

	prevValue := entry.value
	pendingSet := entry.pendingSet
	entry.pendingSet = nil

	cleanups := slices.Clone(entry.cleanups)
	entry.cleanups = nil
	entry.mu.Unlock()

	slices.Reverse(cleanups)
	for _, fn := range cleanups {
		func() {
			defer func() { recover() }()
			fn()
		}()
	}

	entry.mu.Lock()
	entry.state = StateResolving
	entry.value = prevValue
	entry.err = nil
	entry.pendingInvalidate = false
	entry.mu.Unlock()

	if pendingSet != nil {
		switch pendingSet.kind {
		case pendingSetValue:
			si.emitState(StateResolving, atom)
			si.notifyListeners(atom, EventResolving)
			entry.mu.Lock()
			entry.value = pendingSet.value
			entry.state = StateResolved
			entry.hasValue = true
			entry.mu.Unlock()
			si.emitState(StateResolved, atom)
			si.notifyListeners(atom, EventResolved)
			si.cascadeDependents(atom)
			si.maybeScheduleGC(atom)
			si.recheckPending(atom)
			return
		case pendingSetFn:
			si.emitState(StateResolving, atom)
			si.notifyListeners(atom, EventResolving)

			entry.mu.RLock()
			prev := entry.value
			entry.mu.RUnlock()

			var newVal any
			func() {
				defer func() {
					if r := recover(); r != nil {
						newVal = prev
					}
				}()
				newVal = callPendingFn(pendingSet.fn, prev)
			}()

			entry.mu.Lock()
			entry.value = newVal
			entry.state = StateResolved
			entry.hasValue = true
			entry.mu.Unlock()
			si.emitState(StateResolved, atom)
			si.notifyListeners(atom, EventResolved)
			si.cascadeDependents(atom)
			si.maybeScheduleGC(atom)
			si.recheckPending(atom)
			return
		}
	}

	p, hasPreset := si.presetMap[atom.atomID()]
	if hasPreset {
		switch p.kind {
		case presetValue:
			si.emitState(StateResolving, atom)
			si.notifyListeners(atom, EventResolving)
			entry.mu.Lock()
			entry.value = p.value
			entry.state = StateResolved
			entry.hasValue = true
			entry.err = nil
			entry.mu.Unlock()
			si.emitState(StateResolved, atom)
			si.notifyListeners(atom, EventResolved)
			si.cascadeDependents(atom)
			si.maybeScheduleGC(atom)
			si.recheckPending(atom)
			return
		case presetAtomRedirect:
			si.emitState(StateResolving, atom)
			si.notifyListeners(atom, EventResolving)
			replacement := p.value.(AnyAtom)

			si.invMu.Lock()
			alreadyDone := si.invalidationChain[replacement.atomID()]
			if !alreadyDone {
				si.invalidationChain[replacement.atomID()] = true
			}
			si.invMu.Unlock()

			if !alreadyDone {
				si.mu.RLock()
				_, repCached := si.cache[replacement.atomID()]
				si.mu.RUnlock()
				if repCached {
					si.doInvalidateSequential(replacement)
				}
			}

			val, err := resolveInternalAny(si, replacement, nil)
			if err == nil {
				entry.mu.Lock()
				entry.value = val
				entry.state = StateResolved
				entry.hasValue = true
				entry.err = nil
				entry.mu.Unlock()
				si.emitState(StateResolved, atom)
				si.notifyListeners(atom, EventResolved)
			} else {
				entry.mu.Lock()
				entry.state = StateFailed
				entry.err = err
				entry.value = nil
				entry.hasValue = false
				entry.mu.Unlock()
				si.emitState(StateFailed, atom)
				si.notifyAllOnly(atom)
			}

			si.mu.RLock()
			repEntry, repExists := si.cache[replacement.atomID()]
			si.mu.RUnlock()
			if repExists {
				repEntry.mu.Lock()
				repEntry.dependents[atom.atomID()] = true
				repEntry.mu.Unlock()
			}

			si.cascadeDependents(atom)
			si.maybeScheduleGC(atom)
			si.recheckPending(atom)
			return
		}
	}

	si.mu.RLock()
	_, stillCached := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !stillCached {
		return
	}

	si.emitState(StateResolving, atom)
	si.notifyListeners(atom, EventResolving)
	si.resolveUncached(atom, nil)
	si.cascadeDependents(atom)
	si.maybeScheduleGC(atom)
	si.recheckPending(atom)
}

func (si *scopeImpl) recheckPending(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}
	entry.mu.Lock()
	needs := entry.pendingInvalidate || entry.pendingSet != nil
	entry.mu.Unlock()
	if needs {
		si.invMu.Lock()
		delete(si.invalidationChain, atom.atomID())
		si.invMu.Unlock()
		si.scheduleInvalidation(atom)
	}
}

func callPendingFn(fn any, prev any) any {
	type caller interface {
		call(any) any
	}
	if c, ok := fn.(caller); ok {
		return c.call(prev)
	}
	return prev
}

func (si *scopeImpl) cascadeDependents(atom AnyAtom) {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return
	}

	entry.mu.RLock()
	depIDs := slices.Collect(maps.Keys(entry.dependents))
	entry.mu.RUnlock()

	for _, depID := range depIDs {
		si.mu.RLock()
		depAtom, ok := si.atomRegistry[depID]
		si.mu.RUnlock()
		if ok {
			si.scheduleInvalidation(depAtom)
		}
	}
}

func (si *scopeImpl) scheduleSet(atom AnyAtom, value any) error {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return ErrNotResolved
	}

	entry.mu.Lock()
	if entry.state == StateIdle {
		entry.mu.Unlock()
		return ErrNotResolved
	}
	if entry.state == StateFailed && entry.err != nil {
		err := entry.err
		entry.mu.Unlock()
		return err
	}

	if entry.state == StateResolving {
		entry.pendingSet = &pendingSetOp{kind: pendingSetValue, value: value}
		entry.mu.Unlock()
		return nil
	}

	entry.pendingSet = &pendingSetOp{kind: pendingSetValue, value: value}
	entry.mu.Unlock()
	si.scheduleInvalidation(atom)
	return nil
}

type updateFnWrapper[T any] struct {
	fn func(T) T
}

func (u *updateFnWrapper[T]) call(prev any) any {
	return u.fn(typedValue[T](prev))
}

func (si *scopeImpl) scheduleUpdate(atom AnyAtom, fn any) error {
	si.mu.RLock()
	entry, exists := si.cache[atom.atomID()]
	si.mu.RUnlock()
	if !exists {
		return ErrNotResolved
	}

	entry.mu.Lock()
	if entry.state == StateIdle {
		entry.mu.Unlock()
		return ErrNotResolved
	}
	if entry.state == StateFailed && entry.err != nil {
		err := entry.err
		entry.mu.Unlock()
		return err
	}

	if entry.state == StateResolving {
		entry.pendingSet = &pendingSetOp{kind: pendingSetFn, fn: fn}
		entry.mu.Unlock()
		return nil
	}

	entry.pendingSet = &pendingSetOp{kind: pendingSetFn, fn: fn}
	entry.mu.Unlock()
	si.scheduleInvalidation(atom)
	return nil
}

func (si *scopeImpl) releaseByID(id uint64) error {
	si.mu.RLock()
	wait := si.singleflight[id]
	waitEntry := si.cache[id]
	si.mu.RUnlock()
	if wait != nil {
		shouldWait := true
		if waitEntry != nil {
			waitEntry.mu.RLock()
			shouldWait = waitEntry.notifying == 0
			waitEntry.mu.RUnlock()
		}
		if shouldWait {
			select {
			case <-wait:
			case <-si.ctx.Done():
			}
		}
	}

	si.mu.Lock()
	entry, exists := si.cache[id]
	if !exists {
		delete(si.controllers, id)
		delete(si.atomRegistry, id)
		si.mu.Unlock()
		return nil
	}
	delete(si.cache, id)
	delete(si.controllers, id)
	delete(si.singleflight, id)
	delete(si.atomRegistry, id)

	remaining := make([]*atomEntry, 0, len(si.cache))
	for _, e := range si.cache {
		remaining = append(remaining, e)
	}
	si.mu.Unlock()

	for _, other := range remaining {
		other.mu.Lock()
		delete(other.dependents, id)
		other.mu.Unlock()
	}

	entry.mu.Lock()
	if entry.gcTimer != nil {
		entry.gcTimer.Stop()
		entry.gcTimer = nil
	}
	cleanups := slices.Clone(entry.cleanups)
	entry.cleanups = nil
	entry.mu.Unlock()

	slices.Reverse(cleanups)
	var errs []error
	for _, fn := range cleanups {
		func() {
			defer func() {
				if r := recover(); r != nil {
					errs = append(errs, asError(r))
				}
			}()
			if err := fn(); err != nil {
				errs = append(errs, err)
			}
		}()
	}
	return errors.Join(errs...)
}

func (si *scopeImpl) Flush() error {
	for {
		si.invMu.Lock()
		if !si.chainRunning {
			si.invMu.Unlock()
			return nil
		}
		done := si.chainDone
		si.invMu.Unlock()

		select {
		case <-done:
			si.invMu.Lock()
			if !si.chainRunning {
				si.invMu.Unlock()
				return nil
			}
			si.invMu.Unlock()
		case <-si.ctx.Done():
			return si.ctx.Err()
		}
	}
}

func (si *scopeImpl) Dispose() error {
	if si.disposed.Swap(true) {
		return nil
	}

	si.cancel()

	timeout := time.After(5 * time.Second)
	si.invMu.Lock()
	if si.chainRunning {
		done := si.chainDone
		si.invMu.Unlock()
		select {
		case <-done:
		case <-timeout:
		}
	} else {
		si.invMu.Unlock()
	}

	<-si.readyCh
	si.disposeExtensions()

	si.mu.Lock()
	ids := slices.Collect(maps.Keys(si.cache))
	si.mu.Unlock()

	var errs []error
	for _, id := range ids {
		if err := si.releaseByID(id); err != nil {
			errs = append(errs, err)
		}
	}

	si.mu.Lock()
	clear(si.cache)
	clear(si.controllers)
	clear(si.stateListeners)
	clear(si.singleflight)
	clear(si.atomRegistry)
	si.mu.Unlock()

	return errors.Join(errs...)
}
