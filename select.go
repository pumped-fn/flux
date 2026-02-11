package flux

import (
	"fmt"
	"slices"
	"sync"
)

type SelectHandle[S any] struct {
	mu        sync.RWMutex
	current   S
	ctrl      controllerSubscriber
	eq        func(S, S) bool
	selector  func() S
	listeners []func()
	unsub     UnsubscribeFunc
}

type controllerSubscriber interface {
	On(event ControllerEvent, listener func()) UnsubscribeFunc
}

func Select[T any, S comparable](s Scope, atom *Atom[T], selector func(T) S) *SelectHandle[S] {
	return SelectWith[T, S](s, atom, selector, func(a, b S) bool { return a == b })
}

func SelectWith[T any, S any](s Scope, atom *Atom[T], selector func(T) S, eq func(S, S) bool) *SelectHandle[S] {
	ctrl := GetController[T](s, atom)
	v, err := ctrl.Get()
	if err != nil {
		panic(fmt.Sprintf("flux: cannot select from unresolved atom %q (call Resolve first)", atom.Name()))
	}

	h := &SelectHandle[S]{
		current: selector(v),
		ctrl:    ctrl,
		eq:      eq,
		selector: func() S {
			val, err := ctrl.Get()
			if err != nil {
				var zero S
				return zero
			}
			return selector(val)
		},
	}

	return h
}

func (h *SelectHandle[S]) Get() S {
	h.mu.RLock()
	v := h.current
	h.mu.RUnlock()
	return v
}

func (h *SelectHandle[S]) Subscribe(listener func()) UnsubscribeFunc {
	h.mu.Lock()
	if h.unsub == nil {
		h.unsub = h.ctrl.On(EventResolved, func() {
			next := h.selector()

			var changed bool
			var listeners []func()
			func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				if h.eq(h.current, next) {
					return
				}
				h.current = next
				changed = true
				listeners = slices.Clone(h.listeners)
			}()
			if !changed {
				return
			}

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
	h.listeners = append(h.listeners, listener)
	idx := len(h.listeners) - 1
	h.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			if idx < len(h.listeners) {
				h.listeners[idx] = nil
			}
			size := 0
			for _, l := range h.listeners {
				if l != nil {
					size++
				}
			}
			unsub := h.unsub
			if size == 0 {
				h.unsub = nil
			}
			h.mu.Unlock()

			if size == 0 && unsub != nil {
				unsub()
			}
		})
	}
}
