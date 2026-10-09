// Package orderedmap provides a map that iterates in first-insertion order.
// Compiler output built from a plain Go map follows Go's randomized iteration
// order, so two identical builds can differ; use this map wherever iteration
// order reaches generated output.
package orderedmap

import "iter"

// Map stores keys and values in slices in first-insertion order and keeps each
// key's slice position in a map. The zero value is ready to use.
type Map[K comparable, V any] struct {
	index  map[K]int
	keys   []K
	values []V
}

// Set stores value under key. A new key is appended; an existing key keeps its
// position and has its value replaced.
func (m *Map[K, V]) Set(key K, value V) {
	if position, ok := m.index[key]; ok {
		m.values[position] = value
		return
	}
	if m.index == nil {
		m.index = make(map[K]int)
	}
	m.index[key] = len(m.keys)
	m.keys = append(m.keys, key)
	m.values = append(m.values, value)
}

// Len reports the number of keys.
func (m *Map[K, V]) Len() int {
	return len(m.keys)
}

// All yields key-value pairs in first-insertion order.
func (m *Map[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for position, key := range m.keys {
			if !yield(key, m.values[position]) {
				return
			}
		}
	}
}

// Values yields values in first-insertion order.
func (m *Map[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, value := range m.values {
			if !yield(value) {
				return
			}
		}
	}
}
