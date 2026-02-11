package flux

import (
	"iter"
	"sync"
)

type ContextData struct {
	mu     sync.RWMutex
	data   map[string]any
	parent *ContextData
}

func newContextData(parent *ContextData) *ContextData {
	return &ContextData{
		data:   make(map[string]any),
		parent: parent,
	}
}

func (d *ContextData) Get(key string) (any, bool) {
	d.mu.RLock()
	v, ok := d.data[key]
	d.mu.RUnlock()
	return v, ok
}

func (d *ContextData) Set(key string, value any) {
	d.mu.Lock()
	d.data[key] = value
	d.mu.Unlock()
}

func (d *ContextData) Has(key string) bool {
	d.mu.RLock()
	_, ok := d.data[key]
	d.mu.RUnlock()
	return ok
}

func (d *ContextData) Delete(key string) bool {
	d.mu.Lock()
	_, existed := d.data[key]
	delete(d.data, key)
	d.mu.Unlock()
	return existed
}

func (d *ContextData) Clear() {
	d.mu.Lock()
	clear(d.data)
	d.mu.Unlock()
}

func (d *ContextData) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		d.mu.RLock()
		snapshot := make(map[string]any, len(d.data))
		for k, v := range d.data {
			snapshot[k] = v
		}
		d.mu.RUnlock()
		for k, v := range snapshot {
			if !yield(k, v) {
				return
			}
		}
	}
}

func (d *ContextData) SeekAll(key string) iter.Seq[any] {
	return func(yield func(any) bool) {
		cur := d
		for cur != nil {
			cur.mu.RLock()
			v, ok := cur.data[key]
			cur.mu.RUnlock()
			if ok {
				if !yield(v) {
					return
				}
			}
			cur = cur.parent
		}
	}
}

func (d *ContextData) Seek(key string) (any, bool) {
	d.mu.RLock()
	v, ok := d.data[key]
	d.mu.RUnlock()
	if ok {
		return v, true
	}
	if d.parent != nil {
		return d.parent.Seek(key)
	}
	return nil, false
}

func GetTag[T any](d *ContextData, tag *Tag[T]) (T, bool) {
	d.mu.RLock()
	v, ok := d.data[tag.key]
	d.mu.RUnlock()
	if !ok {
		var zero T
		return zero, false
	}
	return typedValue[T](v), true
}

func SetTag[T any](d *ContextData, tag *Tag[T], value T) {
	d.mu.Lock()
	d.data[tag.key] = value
	d.mu.Unlock()
}

func HasTag[T any](d *ContextData, tag *Tag[T]) bool {
	d.mu.RLock()
	_, ok := d.data[tag.key]
	d.mu.RUnlock()
	return ok
}

func DeleteTag[T any](d *ContextData, tag *Tag[T]) bool {
	d.mu.Lock()
	_, existed := d.data[tag.key]
	delete(d.data, tag.key)
	d.mu.Unlock()
	return existed
}

func SeekTag[T any](d *ContextData, tag *Tag[T]) (T, bool) {
	d.mu.RLock()
	v, ok := d.data[tag.key]
	d.mu.RUnlock()
	if ok {
		return typedValue[T](v), true
	}
	if d.parent != nil {
		return SeekTag[T](d.parent, tag)
	}
	var zero T
	return zero, false
}

func GetOrSetTag[T any](d *ContextData, tag *Tag[T], values ...T) T {
	d.mu.Lock()
	if v, ok := d.data[tag.key]; ok {
		d.mu.Unlock()
		return typedValue[T](v)
	}
	var stored T
	if len(values) > 0 {
		stored = values[0]
	} else if tag.hasDefault {
		stored = tag.defaultVal
	}
	d.data[tag.key] = stored
	d.mu.Unlock()
	return stored
}

func applyTags(data *ContextData, tags []AnyTagged) {
	if len(tags) == 0 {
		return
	}
	data.mu.Lock()
	for _, t := range tags {
		data.data[t.TagKey()] = t.TagValue()
	}
	data.mu.Unlock()
}

func applyTagsIfAbsent(data *ContextData, tags []AnyTagged) {
	if len(tags) == 0 {
		return
	}
	data.mu.Lock()
	for _, t := range tags {
		if _, exists := data.data[t.TagKey()]; !exists {
			data.data[t.TagKey()] = t.TagValue()
		}
	}
	data.mu.Unlock()
}
