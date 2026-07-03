package cfg

import "testing"

func TestBlockCreation(t *testing.T) {
	b := &Block{ID: 1, Reachable: true}
	if b.ID != 1 {
		t.Fatalf("expected block ID 1, got %d", b.ID)
	}
	if !b.Reachable {
		t.Fatalf("expected block to be reachable")
	}
}

func TestGraphCreation(t *testing.T) {
	g := &Graph{Name: "test"}
	if g.Name != "test" {
		t.Fatalf("expected graph name 'test', got %s", g.Name)
	}
}
