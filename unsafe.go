package flux

import (
	"fmt"
	"reflect"
	"slices"
)

var (
	rcType   = reflect.TypeFor[*ResolveContext]()
	ecType   = reflect.TypeFor[*ExecContext]()
	errIface = reflect.TypeFor[error]()
)

func NewAtomUnsafe[T any](deps []Resolvable, factory any, opts ...AtomOption) *Atom[T] {
	fVal := reflect.ValueOf(factory)
	fType := fVal.Type()
	if fType.Kind() != reflect.Func {
		panic("flux: NewAtomUnsafe: factory must be a function")
	}
	if fType.NumIn() != 1+len(deps) {
		panic(fmt.Sprintf("flux: NewAtomUnsafe: factory has %d params, expected %d (*ResolveContext + %d deps)", fType.NumIn(), 1+len(deps), len(deps)))
	}
	if fType.In(0) != rcType {
		panic(fmt.Sprintf("flux: NewAtomUnsafe: first param must be *ResolveContext, got %s", fType.In(0)))
	}
	if fType.NumOut() != 2 {
		panic(fmt.Sprintf("flux: NewAtomUnsafe: factory must return 2 values, got %d", fType.NumOut()))
	}
	if !fType.Out(1).Implements(errIface) {
		panic(fmt.Sprintf("flux: NewAtomUnsafe: second return must implement error, got %s", fType.Out(1)))
	}

	depsCopy := slices.Clone(deps)

	// Atom deps must be atoms — validate at construction time.
	atomDeps := make([]AnyAtom, len(depsCopy))
	for i, dep := range depsCopy {
		atom, ok := dep.(AnyAtom)
		if !ok {
			panic(fmt.Sprintf("flux: NewAtomUnsafe: dep %d (%q) is not an Atom", i, dep.Name()))
		}
		atomDeps[i] = atom
	}

	a := &Atom[T]{
		atomBase: atomBase{
			id:   globalIDCounter.Add(1),
			deps: depsCopy,
		},
		factory: func(rc *ResolveContext) (T, error) {
			args := make([]reflect.Value, 1+len(atomDeps))
			args[0] = reflect.ValueOf(rc)
			for i, atom := range atomDeps {
				val, err := ResolveAnyFrom(rc, atom)
				if err != nil {
					var zero T
					return zero, err
				}
				if val == nil {
					args[i+1] = reflect.Zero(fType.In(i + 1))
				} else {
					args[i+1] = reflect.ValueOf(val)
				}
			}
			results := fVal.Call(args)
			var retVal T
			if v := results[0].Interface(); v != nil {
				retVal = v.(T)
			}
			var retErr error
			if v := results[1].Interface(); v != nil {
				retErr = v.(error)
			}
			return retVal, retErr
		},
	}
	for _, opt := range opts {
		opt(&a.atomBase)
	}
	return a
}

func NewFlowUnsafe[In, Out any](deps []Resolvable, factory any, opts ...FlowOption) *Flow[In, Out] {
	fVal := reflect.ValueOf(factory)
	fType := fVal.Type()
	if fType.Kind() != reflect.Func {
		panic("flux: NewFlowUnsafe: factory must be a function")
	}
	if fType.NumIn() != 2+len(deps) {
		panic(fmt.Sprintf("flux: NewFlowUnsafe: factory has %d params, expected %d (*ExecContext + input + %d deps)", fType.NumIn(), 2+len(deps), len(deps)))
	}
	if fType.In(0) != ecType {
		panic(fmt.Sprintf("flux: NewFlowUnsafe: first param must be *ExecContext, got %s", fType.In(0)))
	}
	if fType.NumOut() != 2 {
		panic(fmt.Sprintf("flux: NewFlowUnsafe: factory must return 2 values, got %d", fType.NumOut()))
	}
	if !fType.Out(1).Implements(errIface) {
		panic(fmt.Sprintf("flux: NewFlowUnsafe: second return must implement error, got %s", fType.Out(1)))
	}

	depsCopy := slices.Clone(deps)

	f := &Flow[In, Out]{
		flowBase: flowBase{
			id:   globalIDCounter.Add(1),
			deps: depsCopy,
		},
		factory: func(ec *ExecContext, input In) (Out, error) {
			args := make([]reflect.Value, 2+len(depsCopy))
			args[0] = reflect.ValueOf(ec)
			args[1] = reflect.ValueOf(&input).Elem()
			for i, dep := range depsCopy {
				var val any
				var err error
				if fr, ok := dep.(flowAnyDep); ok {
					val, err = fr.resolveForFlowAny(ec)
				} else if atom, ok := dep.(AnyAtom); ok {
					val, err = ResolveAny(ec.Scope(), atom)
				} else {
					var zero Out
					return zero, fmt.Errorf("flux: NewFlowUnsafe: dep %d (%q) is not a supported dependency type", i, dep.Name())
				}
				if err != nil {
					var zero Out
					return zero, err
				}
				if val == nil {
					args[i+2] = reflect.Zero(fType.In(i + 2))
				} else {
					args[i+2] = reflect.ValueOf(val)
				}
			}
			results := fVal.Call(args)
			var retVal Out
			if v := results[0].Interface(); v != nil {
				retVal = v.(Out)
			}
			var retErr error
			if v := results[1].Interface(); v != nil {
				retErr = v.(error)
			}
			return retVal, retErr
		},
	}
	for _, opt := range opts {
		opt(&f.flowBase)
	}
	return f
}
