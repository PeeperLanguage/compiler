// Package source holds source files and byte spans of their text.
package source

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Span is the byte range [Start, End) of one file's text.
type Span struct{ Start, End int }

// File is one source file. It records where each line starts, so a byte offset
// becomes a line and column only when a message needs them.
type File struct {
	Name  string
	Text  string
	lines []int // byte offset where each line starts
}

func NewFile(name, text string) *File {
	lines := make([]int, 1, strings.Count(text, "\n")+1)
	for offset := 0; ; {
		next := strings.IndexByte(text[offset:], '\n')
		if next < 0 {
			break
		}
		offset += next + 1
		lines = append(lines, offset)
	}
	return &File{Name: name, Text: text, lines: lines}
}

// Line returns the 1-based line that holds offset.
func (f *File) Line(offset int) int {
	return sort.Search(len(f.lines), func(i int) bool { return f.lines[i] > offset })
}

// Position returns the 1-based line and column of offset; columns count runes.
func (f *File) Position(offset int) (line, column int) {
	line = f.Line(offset)
	return line, utf8.RuneCountInString(f.Text[f.lines[line-1]:offset]) + 1
}
