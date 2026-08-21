package lsp

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"compiler/internal/project"
	"compiler/internal/source"
)

func offsetAtPosition(text string, position Position) (int, bool) {
	if position.Line < 0 || position.Character < 0 {
		return 0, false
	}
	lineStart := 0
	for range position.Line {
		newline := strings.IndexByte(text[lineStart:], '\n')
		if newline < 0 {
			return 0, false
		}
		lineStart += newline + 1
	}
	lineEnd := len(text)
	if newline := strings.IndexByte(text[lineStart:], '\n'); newline >= 0 {
		lineEnd = lineStart + newline
	}
	units := 0
	for offset := lineStart; offset < lineEnd; {
		if units == position.Character {
			return offset, true
		}
		r, size := utf8.DecodeRuneInString(text[offset:lineEnd])
		runeUnits := 1
		if r > 0xffff {
			runeUnits = 2
		}
		if units+runeUnits > position.Character {
			return 0, false
		}
		units += runeUnits
		offset += size
	}
	if units == position.Character {
		return lineEnd, true
	}
	return 0, false
}

func positionAtOffset(text string, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	lineStart := strings.LastIndexByte(text[:offset], '\n') + 1
	return Position{
		Line:      strings.Count(text[:lineStart], "\n"),
		Character: len(utf16.Encode([]rune(text[lineStart:offset]))),
	}
}

func sourcePositionAt(text string, position Position) (source.Position, bool) {
	offset, ok := offsetAtPosition(text, position)
	if !ok {
		return source.Position{}, false
	}
	out := source.NewPosition()
	out.Advance(text[:offset])
	return out, true
}

func offsetAtSourcePosition(text string, position *source.Position) (int, bool) {
	if position == nil || position.Line < 1 || position.Column < 1 {
		return 0, false
	}
	lineStart := 0
	for line := 1; line < position.Line; line++ {
		newline := strings.IndexByte(text[lineStart:], '\n')
		if newline < 0 {
			return 0, false
		}
		lineStart += newline + 1
	}
	lineEnd := len(text)
	if newline := strings.IndexByte(text[lineStart:], '\n'); newline >= 0 {
		lineEnd = lineStart + newline
	}
	column := 1
	for offset := range text[lineStart:lineEnd] {
		if column == position.Column {
			return lineStart + offset, true
		}
		column++
	}
	if column == position.Column {
		return lineEnd, true
	}
	return 0, false
}

func rangeAtLocation(text string, location *source.Location) (Range, bool) {
	if location == nil || location.Start == nil || location.End == nil {
		return Range{}, false
	}
	start, startOK := offsetAtSourcePosition(text, location.Start)
	end, endOK := offsetAtSourcePosition(text, location.End)
	if !startOK || !endOK || end < start {
		return Range{}, false
	}
	return Range{Start: positionAtOffset(text, start), End: positionAtOffset(text, end)}, true
}

func sourceTextForFile(ctx *project.CompilerContext, filePath string) (string, bool) {
	if ctx == nil || ctx.Diagnostics == nil || filePath == "" {
		return "", false
	}
	lines, ok := ctx.Diagnostics.GetSourceCache().GetLinesRange(filePath, 1, int(^uint(0)>>1))
	if !ok {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}
