package flux

import (
	"fmt"
	"iter"
)

type Tag[T any] struct {
	label      string
	key        string
	hasDefault bool
	defaultVal T
	parse      func(any) (T, error)
}

func NewTag[T any](label string) *Tag[T] {
	return &Tag[T]{
		label: label,
		key:   fmt.Sprintf("@flux/tag/%s/%d", label, globalIDCounter.Add(1)),
	}
}

func NewTagWithDefault[T any](label string, def T) *Tag[T] {
	return &Tag[T]{
		label:      label,
		key:        fmt.Sprintf("@flux/tag/%s/%d", label, globalIDCounter.Add(1)),
		hasDefault: true,
		defaultVal: def,
	}
}

func NewTagWithParse[T any](label string, parse func(any) (T, error)) *Tag[T] {
	return &Tag[T]{
		label: label,
		key:   fmt.Sprintf("@flux/tag/%s/%d", label, globalIDCounter.Add(1)),
		parse: parse,
	}
}

func (t *Tag[T]) Label() string { return t.label }
func (t *Tag[T]) Key() string   { return t.key }

func (t *Tag[T]) Value(v T) *Tagged[T] {
	if t.parse != nil {
		parsed, err := t.parse(v)
		if err != nil {
			panic(&ParseError{Phase: "tag", Label: t.label, Cause: err})
		}
		v = parsed
	}
	return &Tagged[T]{key: t.key, value: v, tag: t}
}

func (t *Tag[T]) Get(source []AnyTagged) (T, error) {
	for _, tagged := range source {
		if tagged.TagKey() == t.key {
			return typedValue[T](tagged.TagValue()), nil
		}
	}
	if t.hasDefault {
		return t.defaultVal, nil
	}
	var zero T
	return zero, fmt.Errorf("tag %q not found and has no default", t.label)
}

func (t *Tag[T]) Find(source []AnyTagged) (T, bool) {
	for _, tagged := range source {
		if tagged.TagKey() == t.key {
			return typedValue[T](tagged.TagValue()), true
		}
	}
	if t.hasDefault {
		return t.defaultVal, true
	}
	var zero T
	return zero, false
}

func (t *Tag[T]) Collect(source []AnyTagged) []T {
	var result []T
	for _, tagged := range source {
		if tagged.TagKey() == t.key {
			result = append(result, typedValue[T](tagged.TagValue()))
		}
	}
	return result
}

func (t *Tag[T]) All(source []AnyTagged) iter.Seq[T] {
	return func(yield func(T) bool) {
		for _, tagged := range source {
			if tagged.TagKey() == t.key {
				if !yield(typedValue[T](tagged.TagValue())) {
					return
				}
			}
		}
	}
}

func Required[T any](tag *Tag[T]) *Atom[T] {
	return &Atom[T]{
		atomBase: atomBase{
			id:   globalIDCounter.Add(1),
			name: "tag:" + tag.label,
		},
		factory: func(rc *ResolveContext) (T, error) {
			return tag.Get(rc.Scope().Tags())
		},
		tagSource: tag,
	}
}

type Tagged[T any] struct {
	key   string
	value T
	tag   *Tag[T]
}

func (t *Tagged[T]) TagKey() string { return t.key }
func (t *Tagged[T]) TagValue() any  { return t.value }
