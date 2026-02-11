package flux

type presetKind int

const (
	presetValue presetKind = iota
	presetAtomRedirect
	presetFlowRedirect
	presetFlowFn
)

type PresetOption struct {
	targetID uint64
	kind     presetKind
	value    any
}

func Preset[T any](target *Atom[T], value T) PresetOption {
	return PresetOption{
		targetID: target.id,
		kind:     presetValue,
		value:    value,
	}
}

func PresetAtom[T any](target *Atom[T], replacement *Atom[T]) PresetOption {
	if target.id == replacement.id {
		panic("flux: PresetAtom target and replacement must be different atoms")
	}
	return PresetOption{
		targetID: target.id,
		kind:     presetAtomRedirect,
		value:    replacement,
	}
}

func PresetFlow[In, Out any](target *Flow[In, Out], replacement *Flow[In, Out]) PresetOption {
	if target.id == replacement.id {
		panic("flux: PresetFlow target and replacement must be different flows")
	}
	return PresetOption{
		targetID: target.id,
		kind:     presetFlowRedirect,
		value:    replacement,
	}
}

func PresetFlowFn[In, Out any](target *Flow[In, Out], fn func(*ExecContext) (Out, error)) PresetOption {
	return PresetOption{
		targetID: target.id,
		kind:     presetFlowFn,
		value: func(ec *ExecContext) (any, error) {
			return fn(ec)
		},
	}
}
