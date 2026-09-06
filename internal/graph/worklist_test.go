package graph

import "testing"

func TestWorklistDeduplicatesPendingNodesAndAllowsReschedule(t *testing.T) {
	work := NewWorklist(1, 2, 1)
	if work.Add(2) {
		t.Fatal("pending node was scheduled twice")
	}
	first, ok := work.Next()
	if !ok || first != 1 {
		t.Fatalf("first = %d, %v", first, ok)
	}
	if !work.Add(1) {
		t.Fatal("processed node should be schedulable again")
	}
	second, _ := work.Next()
	third, _ := work.Next()
	if second != 2 || third != 1 {
		t.Fatalf("remaining order = %d, %d", second, third)
	}
	if _, ok := work.Next(); ok {
		t.Fatal("worklist should be empty")
	}
}
