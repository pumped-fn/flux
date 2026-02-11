package flux

import "time"

type Extension interface {
	Name() string
}

type Initializer interface {
	Init(s Scope) error
}

type ResolveWrapper interface {
	WrapResolve(next func() (any, error), atom AnyAtom, s Scope) (any, error)
}

type ExecWrapper interface {
	WrapExec(next func() (any, error), target AnyExecTarget, ec *ExecContext) (any, error)
}

type Disposer interface {
	Dispose(s Scope) error
}

func (si *scopeImpl) applyResolveExtensions(atom AnyAtom, doResolve func() (any, error)) (any, error) {
	next := doResolve
	for i := len(si.extensions) - 1; i >= 0; i-- {
		if rw, ok := si.extensions[i].(ResolveWrapper); ok {
			captured := next
			capturedRW := rw
			next = func() (any, error) {
				return capturedRW.WrapResolve(captured, atom, si)
			}
		}
	}
	return next()
}

func (si *scopeImpl) applyExecExtensions(target AnyExecTarget, ec *ExecContext, doExec func() (any, error)) (any, error) {
	next := doExec
	for i := len(si.extensions) - 1; i >= 0; i-- {
		if ew, ok := si.extensions[i].(ExecWrapper); ok {
			captured := next
			capturedEW := ew
			next = func() (any, error) {
				return capturedEW.WrapExec(captured, target, ec)
			}
		}
	}
	return next()
}

func (si *scopeImpl) initExtensions() error {
	for _, ext := range si.extensions {
		if init, ok := ext.(Initializer); ok {
			if err := func() (retErr error) {
				defer func() {
					if r := recover(); r != nil {
						retErr = asError(r)
					}
				}()
				return init.Init(si)
			}(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (si *scopeImpl) disposeExtensions() {
	for _, ext := range si.extensions {
		if d, ok := ext.(Disposer); ok {
			done := make(chan struct{}, 1)
			go func() {
				defer func() {
					_ = recover()
					close(done)
				}()
				_ = d.Dispose(si)
			}()
			select {
			case <-time.After(5 * time.Second):
			case <-done:
			}
		}
	}
}
