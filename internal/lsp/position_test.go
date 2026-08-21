package lsp

import (
	"testing"

	"compiler/internal/source"
)

func TestSourcePositionAndRangeUseUTF16Boundary(t *testing.T) {
	text := "🙂x\n\t𝄞y"
	position, ok := sourcePositionAt(text, Position{Line: 0, Character: 2})
	if !ok || position.Line != 1 || position.Column != 2 || position.Index != len("🙂") {
		t.Fatalf("source position = %#v, %v", position, ok)
	}
	if _, ok := sourcePositionAt(text, Position{Line: 0, Character: 1}); ok {
		t.Fatalf("accepted position inside surrogate pair")
	}

	location := source.NewLocation("test.peep", source.Position{Line: 1, Column: 2}, source.Position{Line: 1, Column: 3})
	rangeValue, ok := rangeAtLocation(text, location)
	if !ok || rangeValue != (Range{Start: Position{Line: 0, Character: 2}, End: Position{Line: 0, Character: 3}}) {
		t.Fatalf("range = %#v, %v", rangeValue, ok)
	}
	start, startOK := offsetAtPosition(text, rangeValue.Start)
	end, endOK := offsetAtPosition(text, rangeValue.End)
	if !startOK || !endOK || text[start:end] != "x" {
		t.Fatalf("range maps to %q, want x", text[start:end])
	}
}

func TestLocationContainmentIsHalfOpen(t *testing.T) {
	location := source.NewLocation("test.peep", source.Position{Line: 1, Column: 3}, source.Position{Line: 1, Column: 5})
	for _, test := range []struct {
		name       string
		line, col  int
		wantInside bool
	}{
		{name: "start", line: 1, col: 3, wantInside: true},
		{name: "interior", line: 1, col: 4, wantInside: true},
		{name: "end", line: 1, col: 5},
		{name: "before", line: 1, col: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := locContains(location, test.line, test.col); got != test.wantInside {
				t.Fatalf("locContains(%d, %d) = %v, want %v", test.line, test.col, got, test.wantInside)
			}
		})
	}

	multiline := source.NewLocation("test.peep", source.Position{Line: 1, Column: 3}, source.Position{Line: 2, Column: 2})
	if !locContains(multiline, 2, 1) || locContains(multiline, 2, 2) {
		t.Fatalf("multiline end is not half-open")
	}
}
