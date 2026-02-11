package flux

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

type ExecContext struct {
	ctx       context.Context
	cancel    context.CancelFunc
	scope     *scopeImpl
	parent    *ExecContext
	input     any
	name      string
	data      *ContextData
	mu        sync.Mutex
	cleanups  []func(error) error
	closed    atomic.Bool
	closeErr  error
	closeOnce sync.Once
}

func (ec *ExecContext) Context() context.Context { return ec.ctx }
func (ec *ExecContext) Scope() Scope             { return ec.scope }
func (ec *ExecContext) Parent() *ExecContext     { return ec.parent }
func (ec *ExecContext) Input() any               { return ec.input }
func (ec *ExecContext) Name() string             { return ec.name }
func (ec *ExecContext) Data() *ContextData       { return ec.data }
func (ec *ExecContext) Closed() bool             { return ec.closed.Load() }

func (ec *ExecContext) Depth() int {
	depth := 0
	cur := ec.parent
	for cur != nil {
		depth++
		cur = cur.parent
	}
	return depth
}

func (ec *ExecContext) Root() *ExecContext {
	cur := ec
	for cur.parent != nil {
		cur = cur.parent
	}
	return cur
}

func (ec *ExecContext) OnClose(fn func(error) error) {
	ec.mu.Lock()
	if ec.closed.Load() {
		ec.mu.Unlock()
		return
	}
	ec.cleanups = append(ec.cleanups, fn)
	ec.mu.Unlock()
}

func (ec *ExecContext) Close(cause error) error {
	ec.closeOnce.Do(func() {
		ec.closed.Store(true)
		ec.cancel()
		ec.mu.Lock()
		cleanups := slices.Clone(ec.cleanups)
		ec.mu.Unlock()

		slices.Reverse(cleanups)
		var errs []error
		for _, fn := range cleanups {
			func() {
				defer func() {
					if r := recover(); r != nil {
						errs = append(errs, asError(r))
					}
				}()
				if err := fn(cause); err != nil {
					errs = append(errs, err)
				}
			}()
		}
		ec.closeErr = errors.Join(errs...)
	})
	return ec.closeErr
}

type CreateContextOption func(*ecOptions)

type ecOptions struct {
	tags []AnyTagged
}

func WithContextTags(tags ...AnyTagged) CreateContextOption {
	return func(o *ecOptions) { o.tags = tags }
}

type ExecOption func(*execOpts)

type execOpts struct {
	name string
	tags []AnyTagged
}

func WithExecName(name string) ExecOption {
	return func(o *execOpts) { o.name = name }
}

func WithExecTags(tags ...AnyTagged) ExecOption {
	return func(o *execOpts) { o.tags = tags }
}

func ExecFlow[In, Out any](ec *ExecContext, f *Flow[In, Out], input In, opts ...ExecOption) (Out, error) {
	if ec.closed.Load() {
		var zero Out
		return zero, ErrClosed
	}
	if err := ec.scope.Ready(); err != nil {
		var zero Out
		return zero, err
	}
	if ec.ctx.Err() != nil {
		var zero Out
		return zero, ec.ctx.Err()
	}

	eo := &execOpts{}
	for _, opt := range opts {
		opt(eo)
	}

	si := ec.scope

	target := f
	seen := map[uint64]bool{f.flowID(): true}
	for {
		p, hasPreset := si.presetMap[target.flowID()]
		if !hasPreset {
			return execFlowDirect[In, Out](ec, target, input, eo)
		}
		switch p.kind {
		case presetFlowRedirect:
			replacement := p.value.(*Flow[In, Out])
			if seen[replacement.flowID()] {
				var zero Out
				return zero, &CircularDepError{Path: []uint64{f.flowID(), replacement.flowID()}}
			}
			seen[replacement.flowID()] = true
			target = replacement
			continue
		case presetFlowFn:
			presetFn := p.value.(func(*ExecContext) (any, error))
			return execPresetFnImpl[In, Out](ec, target, presetFn, input, eo)
		default:
			return execFlowDirect[In, Out](ec, target, input, eo)
		}
	}
}

func execFlowDirect[In, Out any](ec *ExecContext, f *Flow[In, Out], input In, eo *execOpts) (Out, error) {
	var parsedInput any = input
	if f.parse != nil {
		var parseErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					parseErr = asError(r)
				}
			}()
			parsed, err := f.parse(input)
			if err != nil {
				label := coalesceString(eo.name, f.name, "anonymous")
				parseErr = &ParseError{Phase: "flow-input", Label: label, Cause: err}
				return
			}
			parsedInput = parsed
		}()
		if parseErr != nil {
			var zero Out
			return zero, parseErr
		}
	}

	childCtx, childCancel := context.WithCancel(ec.ctx)
	child := &ExecContext{
		ctx:    childCtx,
		cancel: childCancel,
		scope:  ec.scope,
		parent: ec,
		input:  parsedInput,
		name:   coalesceString(eo.name, f.name),
		data:   newContextData(ec.data),
	}

	applyTags(child.data, f.flowTags())
	applyTags(child.data, eo.tags)

	target := &flowExecTarget{f: f}

	var result Out
	execErr := func() (retErr error) {
		defer func() {
			if r := recover(); r != nil {
				retErr = asError(r)
			}
		}()
		val, err := ec.scope.applyExecExtensions(target, child, func() (any, error) {
			if child.ctx.Err() != nil {
				return nil, child.ctx.Err()
			}
			return f.factory(child, typedValue[In](parsedInput))
		})
		if err != nil {
			return err
		}
		result = typedValue[Out](val)
		return nil
	}()

	closeErr := child.Close(execErr)
	if execErr != nil {
		var zero Out
		return zero, errors.Join(execErr, closeErr)
	}
	return result, closeErr
}

func execPresetFnImpl[In, Out any](ec *ExecContext, f *Flow[In, Out], presetFn func(*ExecContext) (any, error), input In, eo *execOpts) (Out, error) {
	var parsedInput any = input
	if f.parse != nil {
		var parseErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					parseErr = asError(r)
				}
			}()
			parsed, err := f.parse(input)
			if err != nil {
				label := coalesceString(eo.name, f.name, "anonymous")
				parseErr = &ParseError{Phase: "flow-input", Label: label, Cause: err}
				return
			}
			parsedInput = parsed
		}()
		if parseErr != nil {
			var zero Out
			return zero, parseErr
		}
	}

	childCtx, childCancel := context.WithCancel(ec.ctx)
	child := &ExecContext{
		ctx:    childCtx,
		cancel: childCancel,
		scope:  ec.scope,
		parent: ec,
		input:  parsedInput,
		name:   coalesceString(eo.name, f.name),
		data:   newContextData(ec.data),
	}

	applyTags(child.data, f.flowTags())
	applyTags(child.data, eo.tags)

	target := &flowExecTarget{f: f}

	var result Out
	execErr := func() (retErr error) {
		defer func() {
			if r := recover(); r != nil {
				retErr = asError(r)
			}
		}()
		val, err := ec.scope.applyExecExtensions(target, child, func() (any, error) {
			if child.ctx.Err() != nil {
				return nil, child.ctx.Err()
			}
			return presetFn(child)
		})
		if err != nil {
			return err
		}
		result = typedValue[Out](val)
		return nil
	}()

	closeErr := child.Close(execErr)
	if execErr != nil {
		var zero Out
		return zero, errors.Join(execErr, closeErr)
	}
	return result, closeErr
}

func ExecFn[Out any](ec *ExecContext, fn func(*ExecContext) (Out, error), opts ...ExecOption) (Out, error) {
	if ec.closed.Load() {
		var zero Out
		return zero, ErrClosed
	}
	if err := ec.scope.Ready(); err != nil {
		var zero Out
		return zero, err
	}
	if ec.ctx.Err() != nil {
		var zero Out
		return zero, ec.ctx.Err()
	}

	eo := &execOpts{}
	for _, opt := range opts {
		opt(eo)
	}
	fnName := coalesceString(eo.name, runtimeFuncName(fn), "fn")

	childCtx, childCancel := context.WithCancel(ec.ctx)
	child := &ExecContext{
		ctx:    childCtx,
		cancel: childCancel,
		scope:  ec.scope,
		parent: ec,
		name:   fnName,
		data:   newContextData(ec.data),
	}

	applyTags(child.data, eo.tags)

	target := &fnExecTarget{name: fnName}

	var result Out
	execErr := func() (retErr error) {
		defer func() {
			if r := recover(); r != nil {
				retErr = asError(r)
			}
		}()
		val, err := ec.scope.applyExecExtensions(target, child, func() (any, error) {
			if child.ctx.Err() != nil {
				return nil, child.ctx.Err()
			}
			return fn(child)
		})
		if err != nil {
			return err
		}
		result = typedValue[Out](val)
		return nil
	}()

	closeErr := child.Close(execErr)
	if execErr != nil {
		var zero Out
		return zero, errors.Join(execErr, closeErr)
	}
	return result, closeErr
}

func runtimeFuncName(fn any) string {
	if fn == nil {
		return ""
	}
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return ""
	}
	rf := runtime.FuncForPC(rv.Pointer())
	if rf == nil {
		return ""
	}
	name := rf.Name()
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, "-fm")
	if strings.HasPrefix(name, "func") {
		for _, ch := range name[len("func"):] {
			if ch < '0' || ch > '9' {
				return name
			}
		}
		return ""
	}
	return name
}
