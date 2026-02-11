package flux

import (
	"context"
	"slices"
	"sync/atomic"
)

type AtomState int

const (
	StateIdle AtomState = iota
	StateResolving
	StateResolved
	StateFailed
)

func (s AtomState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateResolving:
		return "resolving"
	case StateResolved:
		return "resolved"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type ControllerEvent int

const (
	EventResolving ControllerEvent = iota
	EventResolved
	EventAll
)

const eventCount = 3

func (e ControllerEvent) String() string {
	switch e {
	case EventResolving:
		return "resolving"
	case EventResolved:
		return "resolved"
	case EventAll:
		return "*"
	default:
		return "unknown"
	}
}

type UnsubscribeFunc func()

type AnyTagged interface {
	TagKey() string
	TagValue() any
}

// Resolvable is anything that can appear as a named dependency.
// Both *Atom[T] and *Resource[T] implement this interface.
type Resolvable interface {
	Name() string
	ID() uint64
}

type Scope interface {
	Ready() error
	ReadyErr() error
	Tags() []AnyTagged
	CreateContext(opts ...CreateContextOption) *ExecContext
	On(state AtomState, atom AnyAtom, listener func()) UnsubscribeFunc
	Flush() error
	Dispose() error
}

type AnyAtom interface {
	atomID() uint64
	atomName() string
	atomKeepAlive() bool
	atomTags() []AnyTagged
	Name() string
	ID() uint64
	Deps() []Resolvable
}

type AnyFlow interface {
	flowID() uint64
	flowName() string
	flowTags() []AnyTagged
	flowParse() func(any) (any, error)
	callFactory(ec *ExecContext, input any) (any, error)
	Name() string
	ID() uint64
	Deps() []Resolvable
}

type AnyExecTarget interface {
	ExecTargetName() string
}

type ResolveContext struct {
	ctx     context.Context
	scope   *scopeImpl
	stack   *resolveStack
	cleanup func(fn func() error)
	inv     func()
	data    *ContextData
	atom    AnyAtom
}

func (rc *ResolveContext) Context() context.Context { return rc.ctx }
func (rc *ResolveContext) Scope() Scope             { return rc.scope }
func (rc *ResolveContext) Cleanup(fn func() error)  { rc.cleanup(fn) }
func (rc *ResolveContext) Invalidate()              { rc.inv() }
func (rc *ResolveContext) Data() *ContextData       { return rc.data }
func (rc *ResolveContext) Atom() AnyAtom            { return rc.atom }

var globalIDCounter atomic.Uint64

var NameTag = &Tag[string]{label: "name", key: "@flux/name"}

type resolveStack struct {
	ids []uint64
}

func (s *resolveStack) contains(id uint64) bool {
	if s == nil {
		return false
	}
	for _, v := range s.ids {
		if v == id {
			return true
		}
	}
	return false
}

func (s *resolveStack) push(id uint64) *resolveStack {
	if s == nil {
		return &resolveStack{ids: []uint64{id}}
	}
	return &resolveStack{ids: append(slices.Clone(s.ids), id)}
}

func (s *resolveStack) path() []uint64 {
	if s == nil {
		return nil
	}
	return slices.Clone(s.ids)
}

func coalesceString(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func typedValue[T any](val any) T {
	if val == nil {
		var zero T
		return zero
	}
	return val.(T)
}

func mustScopeImpl(s Scope) *scopeImpl {
	si, ok := s.(*scopeImpl)
	if !ok {
		panic("flux: scope was not created by NewScope")
	}
	return si
}
