package source

import (
	"os"
	"path/filepath"
	"testing"

	"compiler/pkg/peeper"
)

type fakeCache struct {
	lines []string
	ok    bool
}

func (f fakeCache) GetLinesRange(_ string, startLine, endLine int) ([]string, bool) {
	if !f.ok || startLine < 1 || endLine < startLine || startLine > len(f.lines) {
		return nil, false
	}
	if endLine > len(f.lines) {
		endLine = len(f.lines)
	}
	return f.lines[startLine-1 : endLine], true
}

func TestPositionAdvance(t *testing.T) {
	p := NewPosition()
	p.Advance("a\tb\nc")
	if p.Line != 2 || p.Column != 2 {
		t.Fatalf("unexpected position: line=%d col=%d", p.Line, p.Column)
	}
	if p.Index <= 0 {
		t.Fatalf("expected positive index")
	}
}

func TestLocationGetTextAndRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main"+peeper.SourceExt)
	content := "hello world\nsecond line\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	loc := NewLocation(path, Position{Line: 1, Column: 7}, Position{Line: 1, Column: 12})
	if got := loc.GetText(nil); got != "world" {
		t.Fatalf("GetText = %q, want world", got)
	}

	lines, err := GetSourceLinesRange(path, 2, 2, nil)
	if err != nil {
		t.Fatalf("GetSourceLinesRange error: %v", err)
	}
	if len(lines) != 1 || lines[0] != "second line" {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestLocationGetTextUsesRuneColumns(t *testing.T) {
	cache := fakeCache{ok: true, lines: []string{"🙂abc", "𝄞def"}}
	tests := []struct {
		name       string
		start, end Position
		want       string
	}{
		{name: "after emoji", start: Position{Line: 1, Column: 2}, end: Position{Line: 1, Column: 5}, want: "abc"},
		{name: "supplementary rune", start: Position{Line: 2, Column: 1}, end: Position{Line: 2, Column: 2}, want: "𝄞"},
		{name: "multiline", start: Position{Line: 1, Column: 2}, end: Position{Line: 2, Column: 3}, want: "abc\n𝄞d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location := NewLocation("ignored", tt.start, tt.end)
			if got := location.GetText(cache); got != tt.want {
				t.Fatalf("GetText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetSourceLinesRangeUsesCache(t *testing.T) {
	lines, err := GetSourceLinesRange("ignored", 1, 1, fakeCache{ok: true, lines: []string{"cached"}})
	if err != nil {
		t.Fatalf("unexpected cache error: %v", err)
	}
	if len(lines) != 1 || lines[0] != "cached" {
		t.Fatalf("unexpected cached lines: %#v", lines)
	}
}
