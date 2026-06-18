package diagnostics

import (
	"fmt"
	"io"
	"os"
	pathpkg "path/filepath"
	"sort"
	"strings"
	"sync"

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
	highlighter         *SyntaxHighlighter
}

type labelContext struct {
	filepath     string
	line         int
	startLine    int
	endLine      int
	startCol     int
	endCol       int
	label        Label
	codeHint     *CodeHint
	lineNumWidth int
	severity     Severity
}

func NewEmitter(w io.Writer) *Emitter {
	return &Emitter{
		cache:       NewSourceCache(),
		writer:      w,
		highlighter: NewSyntaxHighlighter(true),
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
	colors.GREY.Fprintf(e.writer, GUTTER_FMT, e.currentLineNumWidth, line)
}

func (e *Emitter) printCurrentGutter(line int) {
	colors.WHITE.Fprintf(e.writer, GUTTER_FMT, e.currentLineNumWidth, line)
}

func (e *Emitter) printBlankGutter() {
	colors.GREY.Fprintf(e.writer, GUTTER_BLANK, e.currentLineNumWidth, "")
}

func (e *Emitter) printAddedGutter(color colors.COLOR) {
	if color == "" {
		color = colors.GREEN
	}
	color.Fprintf(e.writer, GUTTER_BLANK, e.currentLineNumWidth, "+")
}

func (e *Emitter) printRemovedGutter(color colors.COLOR) {
	if color == "" {
		color = colors.RED
	}
	color.Fprintf(e.writer, GUTTER_BLANK, e.currentLineNumWidth, "-")
}

func (e *Emitter) printPipeOnly() {
	e.printBlankGutter()
	fmt.Fprintln(e.writer)
}

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
	colors.BLUE.Fprintf(e.writer, "%*s--> %s:%d:%d\n", indent, "", filepath, line, col)
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
	fallbackHintCtx := e.fallbackCodeHintContext(diag)

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
				colors.GREY.Fprintf(e.writer, "%*s\n", e.currentLineNumWidth, "...")
			}

			// Clean context code block with customizable tilde/caret markings
			e.printPeeperSnippetBlock(filepath, label, diag.Severity)

			// Track the end line of this snippet to know where we left off
			lastLine = line
			if label.Location.End != nil {
				lastLine = label.Location.End.Line
			}
		}
		fmt.Fprintln(e.writer)
	} else if strings.TrimSpace(diag.FilePath) != "" {
		e.printLocationHeader(diag.FilePath, 1, 1)
	}

	// Step 3: Print Extras and Alternative Code Corrections
	suggestionHeaderPrinted := false
	for _, extra := range diag.Extras {
		switch extra.Kind {
		case ExtraText:
			e.printText(extra.Text)
		case ExtraCodeHint:
			hint := extra.CodeHint
			if !e.hasRenderableCodeHint(&hint) {
				continue
			}
			if !suggestionHeaderPrinted {
				e.printSuggestionHeader()
				suggestionHeaderPrinted = true
			}
			ctx := e.codeHintContext(diag, &hint, fallbackHintCtx)
			e.printCodeHint(ctx)
			e.printPipeOnly()
		}
	}
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
		underlineColor.Fprint(e.writer, strings.Repeat(underlineChar, length))

		if l == endLine && label.Message != "" {
			underlineColor.Fprintf(e.writer, " %s", label.Message)
		}
		fmt.Fprintln(e.writer)
	}
}

func (e *Emitter) fallbackCodeHintContext(diag *Diagnostic) labelContext {
	ctx := labelContext{
		filepath: diag.FilePath,
		line:     1,
		startCol: 1,
		endCol:   1,
		severity: diag.Severity,
	}
	for _, label := range diag.Labels {
		if label.Location == nil || label.Location.Start == nil {
			continue
		}
		start := label.Location.Start
		end := label.Location.End
		if end == nil {
			end = start
		}
		if label.Location.Filename != nil && *label.Location.Filename != "" {
			ctx.filepath = *label.Location.Filename
		}
		ctx.line = start.Line
		ctx.startCol = start.Column
		ctx.endCol = end.Column
		if label.Style == Primary {
			return ctx
		}
	}
	return ctx
}

func (e *Emitter) codeHintContext(diag *Diagnostic, hint *CodeHint, fallback labelContext) labelContext {
	ctx := fallback
	ctx.severity = diag.Severity
	ctx.codeHint = hint

	if hint == nil || hint.Location == nil || hint.Location.Start == nil {
		return ctx
	}

	start := hint.Location.Start
	end := hint.Location.End
	if end == nil {
		end = start
	}

	ctx.filepath = diag.FilePath
	if hint.Location.Filename != nil && *hint.Location.Filename != "" {
		ctx.filepath = *hint.Location.Filename
	}
	ctx.line = start.Line
	ctx.startCol = start.Column
	ctx.endCol = end.Column
	return ctx
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

	color.Fprintf(e.writer, "[%s]", diag.Code)
	fmt.Fprint(e.writer, ": ")
	color.Fprintln(e.writer, diag.Message)
}

func (e *Emitter) printCodeHint(ctx labelContext) {
	hint := ctx.codeHint
	if hint == nil {
		return
	}
	if e.printInlineReplacementHint(ctx, hint) {
		return
	}

	lines := e.codeHintRenderableLines(hint)
	if len(lines) == 0 {
		return
	}

	e.printBlankGutter()
	fmt.Fprintln(e.writer)

	labelsByLine := make(map[int][]CodeHintLabel)
	for _, label := range hint.Labels {
		if label.Line <= 0 {
			continue
		}
		labelsByLine[label.Line] = append(labelsByLine[label.Line], label)
	}

	for i, line := range lines {
		prefix := strings.TrimSpace(line.Prefix)
		switch prefix {
		case "+":
			e.printAddedGutter(hint.GutterColor)
		case "-":
			e.printRemovedGutter(colors.RED)
		default:
			e.printBlankGutter()
		}

		if line.BaseColor != "" {
			e.highlighter.HighlightWithBaseColor(line.Code, e.writer, line.BaseColor)
		} else if hint.BaseColor != "" {
			e.highlighter.HighlightWithBaseColor(line.Code, e.writer, hint.BaseColor)
		} else {
			e.highlighter.HighlightWithColor(line.Code, e.writer)
		}
		fmt.Fprintln(e.writer)

		if labels := labelsByLine[i+1]; len(labels) > 0 {
			for _, label := range labels {
				e.printCodeHintLabelLine(label, ctx.severity)
			}
		}
	}
}

func (e *Emitter) printInlineReplacementHint(ctx labelContext, hint *CodeHint) bool {
	if hint == nil {
		return false
	}
	lines := e.codeHintRenderableLines(hint)
	if len(lines) != 2 {
		return false
	}
	if strings.TrimSpace(lines[0].Prefix) != "-" || strings.TrimSpace(lines[1].Prefix) != "+" {
		return false
	}
	if strings.Contains(lines[0].Code, "\n") || strings.Contains(lines[1].Code, "\n") {
		return false
	}
	if ctx.filepath == "" || ctx.line <= 0 || ctx.startCol <= 0 || ctx.endCol < ctx.startCol {
		return false
	}

	sourceLine, err := e.cache.GetLine(ctx.filepath, ctx.line)
	if err != nil {
		return false
	}
	expandedSourceLine := expandTabs(sourceLine)

	oldFrag := lines[0].Code
	start := visualColumnToPosition(sourceLine, ctx.startCol)
	if start < 0 || start > len(expandedSourceLine) {
		return false
	}
	end := visualColumnToPosition(sourceLine, ctx.endCol)

	if oldFrag != "" {
		if start+len(oldFrag) <= len(expandedSourceLine) && expandedSourceLine[start:start+len(oldFrag)] == oldFrag {
			end = start + len(oldFrag)
		} else if idx := strings.Index(expandedSourceLine, oldFrag); idx >= 0 {
			start = idx
			end = idx + len(oldFrag)
		}
	}
	if end < start || end > len(expandedSourceLine) {
		return false
	}

	newFrag := lines[1].Code
	replacementLine := expandedSourceLine[:start] + newFrag + expandedSourceLine[end:]
	oldAbsStart, oldDiffLen, newAbsStart, newDiffLen := diffHighlightSpans(start, oldFrag, newFrag)

	e.printBlankGutter()
	fmt.Fprintln(e.writer)

	if oldAbsStart < 0 {
		oldAbsStart = 0
	}
	if oldAbsStart > len(expandedSourceLine) {
		oldAbsStart = len(expandedSourceLine)
	}

	relPath := e.diffDisplayPath(ctx.filepath)
	e.printBlankGutter()
	colors.RED.Fprintf(e.writer, "  --- a/%s\n", relPath)
	e.printBlankGutter()
	colors.GREEN.Fprintf(e.writer, "  +++ b/%s\n", relPath)
	e.printBlankGutter()
	colors.GREY.Fprintf(e.writer, "  @@ line %d @@\n", ctx.line)

	e.printBlankGutter()
	colors.RED.Fprint(e.writer, "- ")
	e.printLineWithColoredSpan(expandedSourceLine, oldAbsStart, oldDiffLen, colors.RED)
	fmt.Fprintln(e.writer)

	e.printBlankGutter()
	colors.GREEN.Fprint(e.writer, "+ ")
	e.printLineWithColoredSpan(replacementLine, newAbsStart, newDiffLen, colors.GREEN)
	fmt.Fprintln(e.writer)

	return true
}

func diffHighlightSpans(baseStart int, oldFrag, newFrag string) (oldStart, oldLen, newStart, newLen int) {
	oldStart, newStart = baseStart, baseStart
	oldBytes, newBytes := []byte(oldFrag), []byte(newFrag)

	prefix := 0
	for prefix < len(oldBytes) && prefix < len(newBytes) && oldBytes[prefix] == newBytes[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(oldBytes)-prefix && suffix < len(newBytes)-prefix &&
		oldBytes[len(oldBytes)-1-suffix] == newBytes[len(newBytes)-1-suffix] {
		suffix++
	}

	oldLen = len(oldBytes) - prefix - suffix
	newLen = len(newBytes) - prefix - suffix
	oldStart += prefix
	newStart += prefix

	return oldStart, oldLen, newStart, newLen
}

func (e *Emitter) printLineWithColoredSpan(line string, start, length int, spanColor colors.COLOR) {
	if spanColor == "" || length <= 0 || start < 0 || start > len(line) {
		e.highlighter.HighlightWithColor(line, e.writer)
		return
	}
	end := min(start+length, len(line))
	if start >= end {
		e.highlighter.HighlightWithColor(line, e.writer)
		return
	}

	e.highlighter.HighlightWithColor(line[:start], e.writer)
	spanColor.Fprint(e.writer, line[start:end])
	e.highlighter.HighlightWithColor(line[end:], e.writer)
}

func (e *Emitter) diffDisplayPath(filepath string) string {
	if filepath == "" {
		return "unknown"
	}
	rel := filepath
	if wd, err := os.Getwd(); err == nil {
		if r, err := pathpkg.Rel(wd, filepath); err == nil && r != "" && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}
	return pathpkg.ToSlash(rel)
}

func (e *Emitter) hasRenderableCodeHint(hint *CodeHint) bool {
	return len(e.codeHintRenderableLines(hint)) > 0
}

func (e *Emitter) codeHintRenderableLines(hint *CodeHint) []CodeHintLine {
	if hint == nil {
		return nil
	}
	if len(hint.Lines) > 0 {
		return hint.Lines
	}
	if hint.Code == "" {
		return nil
	}
	rawLines := strings.Split(hint.Code, "\n")
	lines := make([]CodeHintLine, 0, len(rawLines))
	for _, line := range rawLines {
		lines = append(lines, CodeHintLine{
			Prefix:    "+",
			Code:      line,
			BaseColor: hint.BaseColor,
		})
	}
	return lines
}

func (e *Emitter) printCodeHintLabelLine(label CodeHintLabel, severity Severity) {
	if label.Column <= 0 {
		return
	}
	length := label.Length
	if length <= 0 {
		length = 1
	}

	padding := label.Column - 1
	e.printBlankGutter()
	fmt.Fprint(e.writer, strings.Repeat(" ", padding))

	var color colors.COLOR
	if label.Style == Primary {
		color = e.getSeverityColor(severity)
	} else {
		color = colors.BLUE
	}

	color.Fprint(e.writer, strings.Repeat("~", length))
	if label.Message != "" {
		color.Fprintf(e.writer, " %s", label.Message)
	}
	fmt.Fprintln(e.writer)
}

func (e *Emitter) printText(text DiagnosticText) {
	if text.Message == "" {
		return
	}
	color := text.Color
	if color == "" {
		color = colors.WHITE
	}

	e.printSideNotePrefix()
	if text.Kind != "" {
		color.Fprintf(e.writer, "= %s: ", text.Kind)
	} else {
		color.Fprintf(e.writer, "= ")
	}
	fmt.Fprintln(e.writer, text.Message)
}

func (e *Emitter) printSuggestionHeader() {
	e.printSideNotePrefix()
	colors.GREEN.Fprint(e.writer, "= suggestion:")
	fmt.Fprintln(e.writer)
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
