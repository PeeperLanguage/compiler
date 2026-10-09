package orderedmap

import (
	"slices"
	"testing"
)

func TestIterationFollowsFirstInsertionOrder(t *testing.T) {
	var m Map[string, int]
	m.Set("c", 1)
	m.Set("a", 2)
	m.Set("b", 3)
	m.Set("a", 4)

	if got := m.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	if got, want := slices.Collect(m.Values()), []int{1, 4, 3}; !slices.Equal(got, want) {
		t.Fatalf("Values() = %v, want %v", got, want)
	}
	var keys []string
	var values []int
	for key, value := range m.All() {
		keys = append(keys, key)
		values = append(values, value)
	}
	if want := []string{"c", "a", "b"}; !slices.Equal(keys, want) {
		t.Fatalf("All() keys = %v, want %v", keys, want)
	}
	if want := []int{1, 4, 3}; !slices.Equal(values, want) {
		t.Fatalf("All() values = %v, want %v", values, want)
	}
}

func TestZeroMapIsEmpty(t *testing.T) {
	var m Map[string, int]
	if got := m.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
	if got := slices.Collect(m.Values()); len(got) != 0 {
		t.Fatalf("Values() = %v, want empty", got)
	}
	for key, value := range m.All() {
		t.Fatalf("All() yielded %q=%d, want nothing", key, value)
	}
}

func TestIterationStopsWhenYieldReturnsFalse(t *testing.T) {
	var m Map[int, int]
	for i := range 5 {
		m.Set(i, i)
	}
	var values []int
	for value := range m.Values() {
		values = append(values, value)
		if value == 1 {
			break
		}
	}
	if want := []int{0, 1}; !slices.Equal(values, want) {
		t.Fatalf("Values() before break = %v, want %v", values, want)
	}
	var keys []int
	for key := range m.All() {
		keys = append(keys, key)
		if key == 1 {
			break
		}
	}
	if want := []int{0, 1}; !slices.Equal(keys, want) {
		t.Fatalf("All() before break = %v, want %v", keys, want)
	}
}
