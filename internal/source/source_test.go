package source

import "testing"

func TestFilePosition(t *testing.T) {
	file := NewFile("a.peep", "ab\n\tç d\r\nlast")
	for _, test := range []struct {
		offset, line, column int
	}{
		{0, 1, 1},  // a
		{2, 1, 3},  // the newline ends line 1
		{3, 2, 1},  // tab
		{4, 2, 2},  // ç, two bytes, one column
		{7, 2, 4},  // d
		{8, 2, 5},  // \r of a CRLF line end
		{10, 3, 1}, // last line, no newline after it
		{14, 3, 5}, // end of text
	} {
		line, column := file.Position(test.offset)
		if line != test.line || column != test.column {
			t.Errorf("Position(%d) = %d:%d, want %d:%d", test.offset, line, column, test.line, test.column)
		}
	}
}

func TestFileWithoutText(t *testing.T) {
	if line, column := NewFile("empty.peep", "").Position(0); line != 1 || column != 1 {
		t.Fatalf("Position(0) of an empty file = %d:%d, want 1:1", line, column)
	}
}
