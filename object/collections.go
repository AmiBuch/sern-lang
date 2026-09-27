package object

import (
	"fmt"
	"math"
)

// List is a mutable, growable array.
type List struct {
	Items []Value
}

// HashKey is the comparable form of a map key.
type HashKey struct {
	K Kind
	I int64
	S string
}

// KeyOf converts a value to a hash key. Integral floats hash like ints so
// that m[1] and m[1.0] address the same entry.
func KeyOf(v Value) (HashKey, error) {
	switch v.K {
	case KBool:
		return HashKey{K: KBool, I: v.I}, nil
	case KInt:
		return HashKey{K: KInt, I: v.I}, nil
	case KFloat:
		if v.F == math.Trunc(v.F) && math.Abs(v.F) < 1<<62 {
			return HashKey{K: KInt, I: int64(v.F)}, nil
		}
		return HashKey{K: KFloat, I: int64(math.Float64bits(v.F))}, nil
	case KString:
		return HashKey{K: KString, S: v.O.(string)}, nil
	}
	return HashKey{}, fmt.Errorf("%s cannot be used as a map key", v.TypeName())
}

// Map is an insertion-ordered hash map.
type Map struct {
	idx  map[HashKey]int
	keys []Value
	vals []Value
}

// NewMap creates an empty map.
func NewMap() *Map { return &Map{idx: map[HashKey]int{}} }

func (m *Map) Len() int { return len(m.keys) }

// Get looks up k.
func (m *Map) Get(k Value) (Value, bool, error) {
	hk, err := KeyOf(k)
	if err != nil {
		return Nil, false, err
	}
	if i, ok := m.idx[hk]; ok {
		return m.vals[i], true, nil
	}
	return Nil, false, nil
}

// Set inserts or replaces k.
func (m *Map) Set(k, v Value) error {
	hk, err := KeyOf(k)
	if err != nil {
		return err
	}
	if i, ok := m.idx[hk]; ok {
		m.vals[i] = v
		return nil
	}
	m.idx[hk] = len(m.keys)
	m.keys = append(m.keys, k)
	m.vals = append(m.vals, v)
	return nil
}

// Delete removes k, preserving the order of the remaining entries.
func (m *Map) Delete(k Value) (bool, error) {
	hk, err := KeyOf(k)
	if err != nil {
		return false, err
	}
	i, ok := m.idx[hk]
	if !ok {
		return false, nil
	}
	delete(m.idx, hk)
	m.keys = append(m.keys[:i], m.keys[i+1:]...)
	m.vals = append(m.vals[:i], m.vals[i+1:]...)
	for j := i; j < len(m.keys); j++ {
		h, _ := KeyOf(m.keys[j])
		m.idx[h] = j
	}
	return true, nil
}

func (m *Map) GetStr(s string) (Value, bool) {
	i, ok := m.idx[HashKey{K: KString, S: s}]
	if !ok {
		return Nil, false
	}
	return m.vals[i], true
}

func (m *Map) SetStr(s string, v Value) { _ = m.Set(Str(s), v) }

// Keys returns a copy of the keys in insertion order.
func (m *Map) Keys() []Value { return append([]Value(nil), m.keys...) }

// Values returns a copy of the values in insertion order.
func (m *Map) Values() []Value { return append([]Value(nil), m.vals...) }

// Iter is the state of a reading loop.
type Iter struct {
	list  *List
	m     *Map
	keys  []Value
	chars []string
	i     int
}

// NewIter builds an iterator over a list, map or string.
func NewIter(v Value) (*Iter, error) {
	switch v.K {
	case KList:
		return &Iter{list: v.AsList()}, nil
	case KMap:
		m := v.AsMap()
		return &Iter{m: m, keys: m.Keys()}, nil
	case KString:
		var chars []string
		for _, r := range v.AsString() {
			chars = append(chars, string(r))
		}
		return &Iter{chars: chars}, nil
	}
	return nil, fmt.Errorf("cannot read through %s (reading needs a list, map or string)", v.TypeName())
}

// Next advances the iterator. For lists it yields (index, element), for
// maps (key, value), for strings (index, character).
func (it *Iter) Next() (first, second Value, ok bool) {
	switch {
	case it.list != nil:
		if it.i >= len(it.list.Items) {
			return Nil, Nil, false
		}
		i := it.i
		it.i++
		return Int(int64(i)), it.list.Items[i], true
	case it.m != nil:
		for it.i < len(it.keys) {
			k := it.keys[it.i]
			it.i++
			if v, found, _ := it.m.Get(k); found {
				return k, v, true
			}
		}
		return Nil, Nil, false
	default:
		if it.i >= len(it.chars) {
			return Nil, Nil, false
		}
		i := it.i
		it.i++
		return Int(int64(i)), Str(it.chars[i]), true
	}
}

// IsMap reports whether a single-variable loop should bind the key.
func (it *Iter) IsMap() bool { return it.m != nil }
