package diagnostics

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"compiler/internal/source"
	"compiler/pkg/colors"
)

const (
	// Gutter formatting
	GUTTER_FMT   = "%*d | "
	GUTTER_BLANK = "%*s | "

	TAB_WIDTH = 4
)

// expandTabs replaces tab characters with spaces to align with tab stops.
// This ensures consistent visual alignment between source lines and diagnostic markers.
func expandTabs(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var result strings.Builder
	col := 0
	for _, ch := range line {
		if ch == '\t' {
			spaces := TAB_WIDTH - (col % TAB_WIDTH)
			result.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else {
			result.WriteRune(ch)
			col++
		}
	}
	return result.String()
}

// visualColumnToPosition converts a character column (where tabs=1) to a visual position
// in an expanded line (where tabs are expanded to TAB_WIDTH spaces).
// This is used to align carets with source code when tabs are present.
func visualColumnToPosition(line string, column int) int {
	if column <= 0 {
		return 0
	}
	if !strings.ContainsRune(line, '\t') {
		// No tabs, column is already the correct position (1-indexed to 0-indexed)
		return column - 1
	}
	// Iterate through characters, tracking both character column and visual position
	charCol := 1   // 1-indexed character column
	visualPos := 0 // 0-indexed visual position in expanded line

	for _, ch := range line {
		if charCol >= column {
			// Found the character at or past our target column
			// For tabs, we need to check if the column falls within the tab expansion
			if ch == '\t' {
				// This tab starts at charCol and expands to visual positions
				// The tab spans from charCol to charCol (it's one character)
				// but visually it's from visualPos to visualPos+spaces-1
				// Since column == charCol, we return visualPos
				return visualPos
			}
			return visualPos
		}

		if ch == '\t' {
			spaces := TAB_WIDTH - (visualPos % TAB_WIDTH)
			visualPos += spaces
			charCol++
		} else {
			visualPos++
			charCol++
		}
	}

	// If we get here, the column is beyond the end of the line
	return visualPos
}

// SourceCache caches source file contents for error reporting
type SourceCache struct {
	files map[string][]string
	mu    sync.RWMutex
}

func NewSourceCache() *SourceCache {
	return &SourceCache{files: make(map[string][]string)}
}

func (sc *SourceCache) AddSource(filepath, content string) {
	lines := strings.Split(content, "\n")
	sc.mu.Lock()
	sc.files[filepath] = lines
	sc.mu.Unlock()
}

func (sc *SourceCache) GetLinesRange(filepath string, startLine, endLine int) ([]string, bool) {
	sc.mu.RLock()
	lines, ok := sc.files[filepath]
	sc.mu.RUnlock()

	if !ok {
		return nil, false
	}

	// Validate range
	if startLine < 1 || endLine < startLine || startLine > len(lines) {
		return nil, false
	}

	// Adjust endLine if it exceeds file length
	if endLine > len(lines) {
		endLine = len(lines)
	}

	// Return the requested range (convert to 0-indexed)
	return lines[startLine-1 : endLine], true
}

func (sc *SourceCache) GetLine(filepath string, line int) (string, error) {
	sc.mu.RLock()
	lines, ok := sc.files[filepath]
	sc.mu.RUnlock()

	if ok {
		if line > 0 && line <= len(lines) {
			return lines[line-1], nil
		}
		return "", fmt.Errorf("line %d out of range", line)
	}

	// Use the optimized range reading from source package
	// Read entire file and cache it (diagnostics often need multiple lines)
	lines, err := source.GetSourceLines(filepath)
	if err != nil {
		return "", err
	}

	sc.mu.Lock()
	sc.files[filepath] = lines
	sc.mu.Unlock()

	if line > 0 && line <= len(lines) {
		return lines[line-1], nil
	}
	return "", fmt.Errorf("line %d out of range", line)
}

type Emitter struct {
	cache               *SourceCache
	writer              io.Writer
	currentLineNumWidth int
	logger              *colors.Logger
	highlighter         *SyntaxHighlighter
}

func NewEmitter(w io.Writer) *Emitter {
	logger := colors.NewLogger(colors.CurrentLogFormat())
	return &Emitter{
		cache:       NewSourceCache(),
		writer:      w,
		logger:      logger,
		highlighter: NewSyntaxHighlighter(true, logger),
	}
}

// EnableSyntaxHighlighting turns on syntax highlighting for code snippets
func (e *Emitter) EnableSyntaxHighlighting() {
	e.highlighter.Enable()
}

// DisableSyntaxHighlighting turns off syntax highlighting for code snippets
func (e *Emitter) DisableSyntaxHighlighting() {
	e.highlighter.Disable()
}

// SetSyntaxHighlighting sets the syntax highlighting mode
func (e *Emitter) SetSyntaxHighlighting(enabled bool) {
	if enabled {
		e.highlighter.Enable()
	} else {
		e.highlighter.Disable()
	}
}

// ---- Gutter helpers (single source of truth) ----

// printGutter prints " <line> | " with consistent width/color.
func (e *Emitter) printGutter(line int) {
	e.logger.Fprintf(e.writer, colors.GREY, GUTTER_FMT, e.currentLineNumWidth, line)
}

func (e *Emitter) printCurrentGutter(line int) {
	e.logger.Fprintf(e.writer, colors.WHITE, GUTTER_FMT, e.currentLineNumWidth, line)
}

func (e *Emitter) printBlankGutter() {
	e.logger.Fprintf(e.writer, colors.GREY, GUTTER_BLANK, e.currentLineNumWidth, "")
}

func (e *Emitter) printPipeOnly() {
	e.printBlankGutter()
	fmt.Fprintln(e.writer)
}

// printUnderlineIndent pads to the column where source text starts, so an
// underline can be drawn beneath it. Retained at maintainer request for planned
// diagnostics work; do not remove it as dead code without asking.
func (e *Emitter) printUnderlineIndent(extraPadding int) {
	indent := e.currentLineNumWidth + 6 // "   N | " = width + 6 spaces/chars
	fmt.Fprint(e.writer, strings.Repeat(" ", indent+extraPadding))
}

func (e *Emitter) printPrevNonEmptyLine(filepath string, line int) {
	if line <= 1 {
		return
	}
	prevLine, err := e.cache.GetLine(filepath, line-1)
	if err != nil {
		return
	}
	if strings.TrimSpace(prevLine) == "" {
		return
	}
	e.printGutter(line - 1)
	e.highlighter.HighlightWithColor(expandTabs(prevLine), e.writer)
	fmt.Fprintln(e.writer)
}

func (e *Emitter) printLocationHeader(filepath string, line int, col int) {
	indent := e.currentLineNumWidth + 1
	e.logger.Fprintf(e.writer, colors.BLUE, "%*s--> %s:%d:%d\n", indent, "", filepath, line, col)
}

func (e *Emitter) printSideNotePrefix() {
	indent := e.currentLineNumWidth + 1
	fmt.Fprint(e.writer, strings.Repeat(" ", indent))
}

func (e *Emitter) calculateLineNumWidthForDiagnostic(diag *Diagnostic) int {
	lineNumbers := make(map[int]bool)
	for _, label := range diag.Labels {
		if label.Location == nil || label.Location.Start == nil {
			continue
		}
		start := label.Location.Start
		end := label.Location.End
		if end == nil {
			end = start
		}
		for line := start.Line; line <= end.Line; line++ {
			lineNumbers[line] = true
		}
		if start.Line > 1 {
			lineNumbers[start.Line-1] = true
		}
	}

	for _, extra := range diag.Extras {
		for _, fix := range extra.Text.Fixes {
			if fix.Location != nil && fix.Location.Start != nil {
				lineNumbers[fix.Location.Start.Line] = true
			}
		}
	}

	maxLine := 0
	for line := range lineNumbers {
		if line > maxLine {
			maxLine = line
		}
	}
	if maxLine == 0 {
		return 1
	}
	return len(fmt.Sprintf("%d", maxLine))
}

func (e *Emitter) Emit(diag *Diagnostic) {
	e.currentLineNumWidth = e.calculateLineNumWidthForDiagnostic(diag)

	// Step 1: Print Main Diagnostic Header Block
	e.printDiagnosticHeader(diag)

	var lastFile string
	var lastLine int

	// Step 2: Print Sequential Location Cards
	if len(diag.Labels) > 0 {
		// Clone and sort labels by line number so we read the file top-to-bottom
		sortedLabels := make([]Label, len(diag.Labels))
		copy(sortedLabels, diag.Labels)
		sort.SliceStable(sortedLabels, func(i, j int) bool {
			locI, locJ := sortedLabels[i].Location, sortedLabels[j].Location
			if locI == nil || locJ == nil || locI.Start == nil || locJ.Start == nil {
				return false
			}
			fileI, fileJ := "", ""
			if locI.Filename != nil {
				fileI = *locI.Filename
			}
			if locJ.Filename != nil {
				fileJ = *locJ.Filename
			}

			if fileI != fileJ {
				return fileI < fileJ
			}
			return locI.Start.Line < locJ.Start.Line
		})

		for _, label := range sortedLabels {
			if label.Location == nil || label.Location.Filename == nil {
				continue
			}
			filepath := *label.Location.Filename
			if filepath == "" {
				filepath = diag.FilePath
			}

			line := label.Location.Start.Line
			col := label.Location.Start.Column

			if filepath != lastFile {
				// CASE 1: Different file -> Print full file path header
				if lastFile != "" {
					fmt.Fprintln(e.writer)
				}
				e.printLocationHeader(filepath, line, col)
				e.printBlankGutter()
				fmt.Fprintln(e.writer)

				lastFile = filepath
			} else if line > lastLine+1 {
				// CASE 2: Same file, skip in lines -> Print aligned '...' without the pipe
				// This aligns the dots perfectly with where the line numbers sit
				e.logger.Fprintf(e.writer, colors.GREY, "%*s\n", e.currentLineNumWidth, "...")
			}

			// Clean context code block with customizable tilde/caret markings
			e.printPeeperSnippetBlock(filepath, label, diag.Severity)

			// Track the end line of this snippet to know where we left off
			lastLine = line
			if label.Location.End != nil {
				lastLine = label.Location.End.Line
			}
		}
		// Keep the margin unbroken when help or notes follow the snippet.
		if len(diag.Extras) > 0 {
			e.printPipeOnly()
		}
	} else if strings.TrimSpace(diag.FilePath) != "" {
		e.printLocationHeader(diag.FilePath, 1, 1)
	}

	// Step 3: Print extra text lines and the fixes they propose
	isAfterFixedLine := false
	for _, extra := range diag.Extras {
		// A fixed line belongs to the text above it; a margin line keeps the
		// next text from reading as part of it.
		if isAfterFixedLine && extra.Text.Message != "" {
			e.printPipeOnly()
		}
		isAfterFixedLine = e.printText(extra.Text)
	}

	// Every diagnostic ends with one blank line, so the next one, or the
	// summary, never runs into it.
	fmt.Fprintln(e.writer)
}

func (e *Emitter) printPeeperSnippetBlock(filepath string, label Label, severity Severity) {
	startLine := label.Location.Start.Line
	endLine := startLine
	if label.Location.End != nil {
		endLine = label.Location.End.Line
	}

	e.printPrevNonEmptyLine(filepath, startLine)

	for l := startLine; l <= endLine; l++ {
		sourceLine, err := e.cache.GetLine(filepath, l)
		if err != nil {
			continue
		}

		e.printCurrentGutter(l)
		e.highlighter.HighlightWithColor(expandTabs(sourceLine), e.writer)
		fmt.Fprintln(e.writer)

		e.printBlankGutter()

		startCol := 1
		if l == startLine {
			startCol = label.Location.Start.Column
		}

		endCol := len(sourceLine) + 1
		if l == endLine && label.Location.End != nil {
			endCol = label.Location.End.Column
		}

		padding := visualColumnToPosition(sourceLine, startCol)
		length := visualColumnToPosition(sourceLine, endCol) - padding
		if length <= 0 {
			length = 1
		}

		var underlineColor colors.COLOR
		var underlineChar string

		if label.Style == Primary {
			underlineColor = e.getSeverityColor(severity)
			underlineChar = "^" // Sharp pointer for the main error
		} else {
			underlineColor = colors.BLUE
			underlineChar = "-" // Soft line for secondary context
		}

		fmt.Fprint(e.writer, strings.Repeat(" ", padding))
		e.logger.Fprint(e.writer, underlineColor, strings.Repeat(underlineChar, length))

		if l == endLine && label.Message != "" {
			e.logger.Fprintf(e.writer, underlineColor, " %s", label.Message)
		}
		fmt.Fprintln(e.writer)
	}
}

func (e *Emitter) printDiagnosticHeader(diag *Diagnostic) {
	var color colors.COLOR
	switch diag.Severity {
	case Error:
		color = colors.BOLD_RED
	case Warning:
		color = colors.BOLD_YELLOW
	case Info:
		color = colors.BOLD_CYAN
	case Hint:
		color = colors.BOLD_PURPLE
	}

	e.logger.Fprintf(e.writer, color, "[%s]", diag.Code)
	fmt.Fprint(e.writer, ": ")
	e.logger.Fprintln(e.writer, color, diag.Message)
}

// printText prints one extra line and reports whether fixed source lines were
// drawn beneath it.
func (e *Emitter) printText(text DiagnosticText) bool {
	if text.Message == "" {
		return false
	}
	color := text.Color
	if color == "" {
		color = colors.WHITE
	}

	e.printSideNotePrefix()
	if text.Kind != "" {
		e.logger.Fprintf(e.writer, color, "= %s: ", text.Kind)
	} else {
		e.logger.Fprintf(e.writer, color, "= ")
	}
	fmt.Fprintln(e.writer, text.Message)
	return e.printFixes(text.Fixes)
}

// fixSpan is one fix placed on its source line, as byte offsets into the
// tab-expanded line.
type fixSpan struct {
	start, end int
	newText    string
}

// fixedLine is a source line together with every fix that changes it.
type fixedLine struct {
	file   string
	number int
	source string
	spans  []fixSpan
}

// printFixes shows every source line that fixes change, in the order the
// lines are first mentioned. Fixes that cannot be shown faithfully on single
// lines are not drawn at all; the text that proposes them still stands.
func (e *Emitter) printFixes(fixes []CodeFix) bool {
	lines, ok := e.fixedLines(fixes)
	if !ok || len(lines) == 0 {
		return false
	}
	e.printPipeOnly()
	for _, line := range lines {
		e.printFixedLine(line)
	}
	return true
}

// fixedLines groups fixes by the line they change and orders each line's fixes
// by position. It reports false when any fix has no usable location, spans
// lines, points outside its line, or overlaps another: drawing only part of a
// set of edits would show a line the fix does not produce.
func (e *Emitter) fixedLines(fixes []CodeFix) ([]fixedLine, bool) {
	var lines []fixedLine
	for _, fix := range fixes {
		loc := fix.Location
		if loc == nil || loc.Start == nil || loc.Filename == nil {
			return nil, false
		}
		if loc.End != nil && loc.End.Line != loc.Start.Line || strings.ContainsAny(fix.NewText, "\r\n") {
			return nil, false
		}
		index := slices.IndexFunc(lines, func(line fixedLine) bool {
			return line.file == *loc.Filename && line.number == loc.Start.Line
		})
		if index < 0 {
			sourceLine, err := e.cache.GetLine(*loc.Filename, loc.Start.Line)
			if err != nil {
				return nil, false
			}
			index = len(lines)
			lines = append(lines, fixedLine{file: *loc.Filename, number: loc.Start.Line, source: sourceLine})
		}
		start, ok := columnOffset(lines[index].source, loc.Start.Column)
		end := start
		if ok && loc.End != nil {
			end, ok = columnOffset(lines[index].source, loc.End.Column)
		}
		if !ok || end < start {
			return nil, false
		}
		lines[index].spans = append(lines[index].spans, fixSpan{start: start, end: end, newText: fix.NewText})
	}
	for _, line := range lines {
		// An insertion sorts before a wider fix that starts at the same place,
		// so the result does not depend on the order the fixes were given in.
		slices.SortStableFunc(line.spans, func(a, b fixSpan) int { return cmp.Or(a.start-b.start, a.end-b.end) })
		for i := 1; i < len(line.spans); i++ {
			if line.spans[i].start < line.spans[i-1].end {
				return nil, false
			}
		}
	}
	return lines, true
}

// columnOffset maps a 1-based character column of sourceLine to a byte offset
// in its tab-expanded form. Columns count characters, so a character of
// several bytes or a tab of several spaces is one column. The column just past
// the last character is the end of the line; anything further is not on it.
func columnOffset(sourceLine string, column int) (int, bool) {
	offset, visual, current := 0, 0, 1
	for _, ch := range sourceLine {
		if current == column {
			return offset, true
		}
		if ch == '\t' {
			spaces := TAB_WIDTH - (visual % TAB_WIDTH)
			offset += spaces
			visual += spaces
		} else {
			offset += utf8.RuneLen(ch)
			visual++
		}
		current++
	}
	return offset, current == column
}

// printFixedLine draws one fixed line. A line that gains text is shown as it
// will read, with only the new characters marked. A line that only loses text
// is shown as it reads now, with the removed part marked; plain output cannot
// show that marking, so it shows the line as it will read.
func (e *Emitter) printFixedLine(line fixedLine) {
	text := expandTabs(line.source)
	showsResult := e.logger.Format() == colors.LogFormatNormal ||
		slices.ContainsFunc(line.spans, func(span fixSpan) bool {
			_, _, added, _ := diffText(text[span.start:span.end], span.newText)
			return added != ""
		})

	e.printGutter(line.number)
	position := 0
	for i, span := range line.spans {
		kept, removed, added, keptAfter := diffText(text[span.start:span.end], span.newText)
		// Removing a whole word leaves the space on each side of it next to
		// each other, or a stray one beside a bracket or at the line's end;
		// drop one so the line reads as a person would type it.
		isWordRemoved := showsResult && span.newText == "" && removed != ""
		before := text[position:span.start]
		if isWordRemoved && span.end == len(text) {
			before = strings.TrimSuffix(before, " ")
		}
		e.highlighter.HighlightWithColor(before, e.writer)
		position = span.end

		e.highlighter.HighlightWithColor(kept, e.writer)
		if showsResult && added != "" {
			e.logger.Fprint(e.writer, colors.FIX_ADDED, added)
		} else if !showsResult && removed != "" {
			e.logger.Fprint(e.writer, colors.FIX_REMOVED, removed)
		}
		e.highlighter.HighlightWithColor(keptAfter, e.writer)

		// The space after the word goes when the word followed a space, an
		// opening bracket or the line's start, unless the next fix starts on it.
		isSpaceNext := strings.HasPrefix(text[position:], " ")
		isAfterInsertion := i > 0 && line.spans[i-1].end == span.start && line.spans[i-1].newText != ""
		isAfterGap := !isAfterInsertion && (span.start == 0 || strings.ContainsRune(" ([{<", rune(text[span.start-1])))
		isSpaceFree := i+1 == len(line.spans) || line.spans[i+1].start > position
		if isWordRemoved && isSpaceNext && isAfterGap && isSpaceFree {
			position++
		}
	}
	e.highlighter.HighlightWithColor(text[position:], e.writer)
	fmt.Fprintln(e.writer)
}

// diffText splits a replacement into what both texts share at the start and
// the end, and what differs in between: the characters that go and the
// characters that come. Only the differing part is marked.
func diffText(oldText, newText string) (kept, removed, added, keptAfter string) {
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
	return string(before[:prefix]), string(before[prefix : len(before)-suffix]),
		string(after[prefix : len(after)-suffix]), string(before[len(before)-suffix:])
}

func (e *Emitter) getSeverityColor(severity Severity) colors.COLOR {
	switch severity {
	case Error:
		return colors.RED
	case Warning:
		return colors.YELLOW
	case Info:
		return colors.BLUE
	case Hint:
		return colors.PURPLE
	default:
		return colors.RED
	}
}
