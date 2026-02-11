package flux

import "fmt"

type flowBase struct {
	id    uint64
	name  string
	tags  []AnyTagged
	deps  []Resolvable
	parse func(any) (any, error)
}

func (f *flowBase) flowID() uint64                    { return f.id }
func (f *flowBase) flowName() string                  { return f.name }
func (f *flowBase) flowTags() []AnyTagged             { return f.tags }
func (f *flowBase) flowParse() func(any) (any, error) { return f.parse }
func (f *flowBase) Name() string                      { return f.name }
func (f *flowBase) ID() uint64                        { return f.id }
func (f *flowBase) Deps() []Resolvable                { return f.deps }

type Flow[In, Out any] struct {
	flowBase
	factory func(*ExecContext, In) (Out, error)
}

func (f *Flow[In, Out]) ExecTargetName() string { return f.name }

func (f *Flow[In, Out]) callFactory(ec *ExecContext, input any) (any, error) {
	return f.factory(ec, input.(In))
}

type FlowOption func(*flowBase)

func WithFlowName(name string) FlowOption {
	return func(b *flowBase) { b.name = name }
}

func WithFlowTags(tags ...AnyTagged) FlowOption {
	return func(b *flowBase) { b.tags = tags }
}

func WithParse[In any](parse func(any) (In, error)) FlowOption {
	return func(b *flowBase) {
		b.parse = func(raw any) (any, error) { return parse(raw) }
	}
}

func resolveFlowDep[T any](ec *ExecContext, dep *Atom[T]) (T, error) {
	if dep.tagSource != nil {
		v, ok := SeekTag(ec.Data(), dep.tagSource)
		if ok {
			return v, nil
		}
		if dep.tagSource.hasDefault {
			return dep.tagSource.defaultVal, nil
		}
		var zero T
		return zero, fmt.Errorf("required tag %q not found in context", dep.tagSource.Label())
	}
	return Resolve(ec.Scope(), dep)
}

func NewFlow[In, Out any](factory func(*ExecContext, In) (Out, error), opts ...FlowOption) *Flow[In, Out] {
	f := &Flow[In, Out]{
		flowBase: flowBase{id: globalIDCounter.Add(1)},
		factory:  factory,
	}
	for _, opt := range opts {
		opt(&f.flowBase)
	}
	return f
}

// NewFlowFrom creates a Flow with one dependency (Atom or Resource).
func NewFlowFrom[D1, In, Out any](dep1 FlowDep[D1], factory func(*ExecContext, In, D1) (Out, error), opts ...FlowOption) *Flow[In, Out] {
	if dep1 == nil {
		panic("flux: NewFlowFrom: dep1 must not be nil")
	}
	f := &Flow[In, Out]{
		flowBase: flowBase{
			id:   globalIDCounter.Add(1),
			deps: []Resolvable{dep1},
		},
		factory: func(ec *ExecContext, input In) (Out, error) {
			v1, err := dep1.resolveForFlow(ec)
			if err != nil {
				var zero Out
				return zero, err
			}
			return factory(ec, input, v1)
		},
	}
	for _, opt := range opts {
		opt(&f.flowBase)
	}
	return f
}

// NewFlowFrom2 creates a Flow with two dependencies (Atom or Resource).
func NewFlowFrom2[D1, D2, In, Out any](dep1 FlowDep[D1], dep2 FlowDep[D2], factory func(*ExecContext, In, D1, D2) (Out, error), opts ...FlowOption) *Flow[In, Out] {
	if dep1 == nil {
		panic("flux: NewFlowFrom2: dep1 must not be nil")
	}
	if dep2 == nil {
		panic("flux: NewFlowFrom2: dep2 must not be nil")
	}
	f := &Flow[In, Out]{
		flowBase: flowBase{
			id:   globalIDCounter.Add(1),
			deps: []Resolvable{dep1, dep2},
		},
		factory: func(ec *ExecContext, input In) (Out, error) {
			v1, err := dep1.resolveForFlow(ec)
			if err != nil {
				var zero Out
				return zero, err
			}
			v2, err := dep2.resolveForFlow(ec)
			if err != nil {
				var zero Out
				return zero, err
			}
			return factory(ec, input, v1, v2)
		},
	}
	for _, opt := range opts {
		opt(&f.flowBase)
	}
	return f
}

// NewFlowFrom3 creates a Flow with three dependencies (Atom or Resource).
func NewFlowFrom3[D1, D2, D3, In, Out any](dep1 FlowDep[D1], dep2 FlowDep[D2], dep3 FlowDep[D3], factory func(*ExecContext, In, D1, D2, D3) (Out, error), opts ...FlowOption) *Flow[In, Out] {
	if dep1 == nil {
		panic("flux: NewFlowFrom3: dep1 must not be nil")
	}
	if dep2 == nil {
		panic("flux: NewFlowFrom3: dep2 must not be nil")
	}
	if dep3 == nil {
		panic("flux: NewFlowFrom3: dep3 must not be nil")
	}
	f := &Flow[In, Out]{
		flowBase: flowBase{
			id:   globalIDCounter.Add(1),
			deps: []Resolvable{dep1, dep2, dep3},
		},
		factory: func(ec *ExecContext, input In) (Out, error) {
			v1, err := dep1.resolveForFlow(ec)
			if err != nil {
				var zero Out
				return zero, err
			}
			v2, err := dep2.resolveForFlow(ec)
			if err != nil {
				var zero Out
				return zero, err
			}
			v3, err := dep3.resolveForFlow(ec)
			if err != nil {
				var zero Out
				return zero, err
			}
			return factory(ec, input, v1, v2, v3)
		},
	}
	for _, opt := range opts {
		opt(&f.flowBase)
	}
	return f
}

type flowExecTarget struct {
	f AnyFlow
}

func (t *flowExecTarget) ExecTargetName() string { return t.f.flowName() }

type fnExecTarget struct {
	name string
}

func (t *fnExecTarget) ExecTargetName() string { return t.name }
