package diag

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"compiler/internal/source"
	"compiler/pkg/colors"
)

const tabWidth = 4

// Print writes the diagnostics, ordered by the file and position of their
// primary place (those without a place first), then a summary line. Color
// adds terminal colors. A span outside its file's text is clamped, so printing
// never fails.
func Print(w io.Writer, diagnostics []Diagnostic, color bool) {
	ordered := slices.Clone(diagnostics)
	slices.SortStableFunc(ordered, comparePlaces)
	p := printer{w: w, color: color}
	errors, warnings := 0, 0
	for _, d := range ordered {
		p.diagnostic(d)
		switch d.Code.Severity {
		case Error:
			errors++
		case Warning:
			warnings++
		}
	}
	switch {
	case errors > 0 && warnings > 0:
		p.paint(colors.RED, fmt.Sprintf("Compilation failed with %d error(s) and %d warning(s)", errors, warnings))
		fmt.Fprintln(w)
	case errors > 0:
		p.paint(colors.RED, fmt.Sprintf("Compilation failed with %d error(s)", errors))
		fmt.Fprintln(w)
	case warnings > 0:
		p.paint(colors.ORANGE, fmt.Sprintf("Compilation succeeded with %d warning(s)", warnings))
		fmt.Fprintln(w)
	}
}

func comparePlaces(a, b Diagnostic) int {
	switch {
	case len(a.Labels) == 0 || len(b.Labels) == 0:
		return cmp.Compare(len(a.Labels), len(b.Labels))
	case a.Labels[0].File != b.Labels[0].File:
		return strings.Compare(fileName(a.Labels[0].File), fileName(b.Labels[0].File))
	}
	return cmp.Compare(a.Labels[0].Span.Start, b.Labels[0].Span.Start)
}

func fileName(f *source.File) string {
	if f == nil {
		return ""
	}
	return f.Name
}

type printer struct {
	w     io.Writer
	color bool
	width int // digits of the largest line number shown
}

func (p *printer) paint(color colors.COLOR, text string) {
	if p.color {
		fmt.Fprint(p.w, color, text, colors.RESET)
	} else {
		fmt.Fprint(p.w, text)
	}
}

func (p *printer) gutter(color colors.COLOR, number int) {
	p.paint(color, fmt.Sprintf("%*d | ", p.width, number))
}

func (p *printer) blankGutter() {
	p.paint(colors.GREY, fmt.Sprintf("%*s | ", p.width, ""))
}

func (p *printer) margin() {
	p.blankGutter()
	fmt.Fprintln(p.w)
}

func (p *printer) diagnostic(d Diagnostic) {
	files := snippets(d)
	p.width = lineNumberWidth(d, files)

	headerColor, markColor := colors.BOLD_RED, colors.RED
	switch d.Code.Severity {
	case Warning:
		headerColor, markColor = colors.BOLD_YELLOW, colors.YELLOW
	case Info:
		headerColor, markColor = colors.BOLD_CYAN, colors.BLUE
	}
	p.paint(headerColor, "["+d.Code.ID+"]")
	fmt.Fprint(p.w, ": ")
	p.paint(headerColor, d.Message)
	fmt.Fprintln(p.w)

	for index, file := range files {
		if index > 0 {
			fmt.Fprintln(p.w)
		}
		p.paint(colors.BLUE, fmt.Sprintf("%*s--> %s:%d:%d", p.width+1, "", file.file.Name, file.line, file.column))
		fmt.Fprintln(p.w)
		p.margin()
		lastShown := 0
		for _, line := range file.lines {
			context := contextLine(file.file, line.number, lastShown)
			firstShown := line.number
			if context != "" {
				firstShown--
			}
			if lastShown > 0 && firstShown > lastShown+1 {
				p.paint(colors.GREY, fmt.Sprintf("%*s", p.width, "..."))
				fmt.Fprintln(p.w)
			}
			if context != "" {
				p.gutter(colors.GREY, line.number-1)
				p.cells(context, 0, "")
				fmt.Fprintln(p.w)
			}
			p.markedLine(file.file, line, markColor)
			lastShown = line.number
		}
	}
	if len(files) > 0 && len(d.Notes) > 0 {
		p.margin()
	}

	afterFixedLines := false
	for _, note := range d.Notes {
		if afterFixedLines {
			p.margin()
		}
		kind, color := "note", colors.CYAN
		if note.Help {
			kind, color = "help", colors.GREEN
		}
		fmt.Fprint(p.w, strings.Repeat(" ", p.width+1))
		p.paint(color, "= "+kind+": ")
		fmt.Fprintln(p.w, note.Text)
		afterFixedLines = p.fixedLines(note.Fixes)
	}
	fmt.Fprintln(p.w)
}

// mark is the part of one source line a label covers, in 1-based character
// columns; end is 0 when the label runs on past the end of the line.
type mark struct {
	start, end int
	primary    bool
	messages   []string
}

type markedLine struct {
	number int
	marks  []mark
}

// fileSnippet holds the marked lines of one file and the place its header names.
type fileSnippet struct {
	file         *source.File
	line, column int
	lines        []markedLine
}

// snippets arranges the labels of d by file: the primary label's file first,
// then the others by name; lines in order; on a line, marks from left to right
// with the primary one first. Labels on exactly the same span are drawn once.
func snippets(d Diagnostic) []fileSnippet {
	var files []fileSnippet
	for index, label := range d.Labels {
		if label.File == nil {
			continue
		}
		at := slices.IndexFunc(files, func(f fileSnippet) bool { return f.file == label.File })
		if at < 0 {
			at = len(files)
			files = append(files, fileSnippet{file: label.File})
		}
		file := &files[at]
		span := clamp(label.File, label.Span)
		startLine, startColumn := label.File.Position(span.Start)
		endLine, endColumn := label.File.Position(span.End)
		if file.line == 0 || index == 0 {
			file.line, file.column = startLine, startColumn
		}
		for number := startLine; number <= endLine; number++ {
			m := mark{start: 1, primary: index == 0}
			if number == startLine {
				m.start = startColumn
			}
			if number == endLine {
				m.end = endColumn
				if label.Message != "" {
					m.messages = []string{label.Message}
				}
			}
			lineAt := slices.IndexFunc(file.lines, func(l markedLine) bool { return l.number == number })
			if lineAt < 0 {
				lineAt = len(file.lines)
				file.lines = append(file.lines, markedLine{number: number})
			}
			file.lines[lineAt].marks = append(file.lines[lineAt].marks, m)
		}
	}
	for index := range files {
		file := &files[index]
		slices.SortFunc(file.lines, func(a, b markedLine) int { return a.number - b.number })
		for lineAt := range file.lines {
			marks := file.lines[lineAt].marks
			slices.SortStableFunc(marks, func(a, b mark) int {
				if a.start != b.start {
					return a.start - b.start
				}
				return boolOrder(b.primary) - boolOrder(a.primary)
			})
			file.lines[lineAt].marks = mergeSameSpan(marks)
		}
	}
	if len(files) > 1 {
		slices.SortStableFunc(files[1:], func(a, b fileSnippet) int { return strings.Compare(a.file.Name, b.file.Name) })
	}
	return files
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mergeSameSpan(marks []mark) []mark {
	merged := marks[:0]
	for _, m := range marks {
		at := slices.IndexFunc(merged, func(kept mark) bool { return kept.start == m.start && kept.end == m.end })
		if at < 0 {
			merged = append(merged, m)
			continue
		}
		merged[at].primary = merged[at].primary || m.primary
		merged[at].messages = append(merged[at].messages, m.messages...)
	}
	return merged
}

func clamp(file *source.File, span source.Span) source.Span {
	span.Start = min(max(span.Start, 0), len(file.Text))
	span.End = min(max(span.End, span.Start), len(file.Text))
	return span
}

func lineText(file *source.File, number int) string {
	span := file.LineSpan(number)
	return file.Text[span.Start:span.End]
}

// contextLine returns the line above number when it is not blank and not
// already shown, so the marked line reads in its setting.
func contextLine(file *source.File, number, lastShown int) string {
	if number <= 1 || number-1 <= lastShown {
		return ""
	}
	if text := lineText(file, number-1); strings.TrimSpace(text) != "" {
		return text
	}
	return ""
}

func lineNumberWidth(d Diagnostic, files []fileSnippet) int {
	largest := 1
	for _, file := range files {
		for _, line := range file.lines {
			largest = max(largest, line.number)
		}
	}
	for _, note := range d.Notes {
		for _, fix := range note.Fixes {
			if fix.File != nil {
				line, _ := fix.File.Position(clamp(fix.File, fix.Span).Start)
				largest = max(largest, line)
			}
		}
	}
	return len(fmt.Sprint(largest))
}

func (p *printer) markedLine(file *source.File, line markedLine, markColor colors.COLOR) {
	text := lineText(file, line.number)
	p.gutter(colors.WHITE, line.number)
	p.cells(text, 0, "")
	fmt.Fprintln(p.w)
	for _, m := range line.marks {
		end := m.end
		if end == 0 {
			end = utf8.RuneCountInString(text) + 1
		}
		padding := cellOf(text, m.start)
		length := max(cellOf(text, end)-padding, 1)
		color, underline := colors.BLUE, "-"
		if m.primary {
			color, underline = markColor, "^"
		}
		p.blankGutter()
		fmt.Fprint(p.w, strings.Repeat(" ", padding))
		p.paint(color, strings.Repeat(underline, length))
		for index, message := range m.messages {
			if index > 0 {
				fmt.Fprintln(p.w)
				p.blankGutter()
				fmt.Fprint(p.w, strings.Repeat(" ", padding+length))
			}
			p.paint(color, " "+message)
		}
		fmt.Fprintln(p.w)
	}
}

// fixedLine is a source line with the fixes that change it, as spans relative
// to the start of the line.
type fixedLine struct {
	file   *source.File
	number int
	fixes  []Fix
}

// fixedLines shows every line the fixes change, as it reads once they are
// applied, with the added text marked. It shows nothing unless every fix is
// drawable: inside its file, on one line, without a line break in its text and
// not overlapping another. It reports whether it drew anything.
func (p *printer) fixedLines(fixes []Fix) bool {
	var lines []fixedLine
	for _, fix := range fixes {
		if fix.File == nil || fix.Span.Start < 0 || fix.Span.End < fix.Span.Start || fix.Span.End > len(fix.File.Text) ||
			strings.ContainsAny(fix.NewText, "\r\n") || fix.File.Line(fix.Span.Start) != fix.File.Line(fix.Span.End) {
			return false
		}
		number := fix.File.Line(fix.Span.Start)
		at := slices.IndexFunc(lines, func(l fixedLine) bool { return l.file == fix.File && l.number == number })
		if at < 0 {
			at = len(lines)
			lines = append(lines, fixedLine{file: fix.File, number: number})
		}
		lines[at].fixes = append(lines[at].fixes, fix)
	}
	for _, line := range lines {
		slices.SortStableFunc(line.fixes, func(a, b Fix) int {
			return cmp.Or(a.Span.Start-b.Span.Start, a.Span.End-b.Span.End)
		})
		for i := 1; i < len(line.fixes); i++ {
			if line.fixes[i].Span.Start < line.fixes[i-1].Span.End {
				return false
			}
		}
	}
	if len(lines) == 0 {
		return false
	}
	p.margin()
	for _, line := range lines {
		p.gutter(colors.GREY, line.number)
		span := line.file.LineSpan(line.number)
		cells, position := 0, span.Start
		for _, fix := range line.fixes {
			cells = p.cells(line.file.Text[position:fix.Span.Start], cells, "")
			kept, added, keptAfter := splitChange(line.file.Text[fix.Span.Start:fix.Span.End], fix.NewText)
			cells = p.cells(kept, cells, "")
			cells = p.cells(added, cells, colors.FIX_ADDED)
			cells = p.cells(keptAfter, cells, "")
			position = fix.Span.End
		}
		p.cells(line.file.Text[position:span.End], cells, "")
		fmt.Fprintln(p.w)
	}
	return true
}

// cells prints text starting at terminal cell column, expanding tabs, in color
// when one is given, and returns the column after it.
func (p *printer) cells(text string, column int, color colors.COLOR) int {
	var out strings.Builder
	for _, r := range text {
		width := cellWidth(r, column)
		if r == '\t' {
			out.WriteString(strings.Repeat(" ", width))
		} else {
			out.WriteRune(r)
		}
		column += width
	}
	if color != "" && out.Len() > 0 {
		p.paint(color, out.String())
	} else {
		fmt.Fprint(p.w, out.String())
	}
	return column
}

// splitChange splits a replacement into the part both texts start with, the
// new text in between, and the part both end with; only the middle is marked.
func splitChange(oldText, newText string) (kept, added, keptAfter string) {
	before, after := []rune(oldText), []rune(newText)
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	return string(after[:prefix]), string(after[prefix : len(after)-suffix]), string(after[len(after)-suffix:])
}

// cellOf returns the terminal cell where the 1-based character column of text
// is drawn; a column past the end counts one cell per column.
func cellOf(text string, column int) int {
	cell, current := 0, 1
	for _, r := range text {
		if current >= column {
			return cell
		}
		cell += cellWidth(r, cell)
		current++
	}
	return cell + max(column-current, 0)
}

// cellWidth is how many terminal cells r takes at cell column: a tab runs to
// the next tab stop, a combining mark or format character takes none, and wide
// East Asian characters and emoji take two.
func cellWidth(r rune, column int) int {
	switch {
	case r == '\t':
		return tabWidth - column%tabWidth
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul),
		r >= 0x3000 && r <= 0x303F,
		r >= 0xFF01 && r <= 0xFF60, r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF:
		return 2
	}
	return 1
}
