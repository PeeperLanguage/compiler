package diagnostics

import (
	"bytes"
	"strings"
	"testing"

	"compiler/internal/source"
	"compiler/pkg/colors"
	"compiler/pkg/peeper"
)

func testLoc(file string, line, col int) *source.Location {
	start := source.Position{Line: line, Column: col}
	end := source.Position{Line: line, Column: col + 1}
	return source.NewLocation(file, start, end)
}

func TestWithSecondaryLabelRequiresPrimary(t *testing.T) {
	d := NewError("boom")
	loc := testLoc("a"+peeper.SourceExt, 1, 1)

	d.WithSecondaryLabel(loc, "context")
	if d.Severity != Error {
		t.Fatalf("expected severity error, got %v", d.Severity)
	}
	if d.Code != internalCompilerErrorCode {
		t.Fatalf("expected code %q, got %q", internalCompilerErrorCode, d.Code)
	}
	if len(d.Labels) != 2 {
		t.Fatalf("expected fallback primary + secondary labels, got %d", len(d.Labels))
	}
	if d.Labels[0].Style != Primary {
		t.Fatalf("expected first label primary, got %v", d.Labels[0].Style)
	}
	if d.Labels[1].Style != Secondary {
		t.Fatalf("expected second label secondary, got %v", d.Labels[1].Style)
	}
	if len(d.Extras) == 0 || d.Extras[0].Kind != ExtraText || d.Extras[0].Text.Kind != "internal" {
		t.Fatalf("expected internal diagnostic text in Extras, got %#v", d.Extras)
	}
}

func TestWithHelpCarriesItsFixes(t *testing.T) {
	d := NewError("immutable")
	loc := testLoc("main"+peeper.SourceExt, 2, 5)
	d.WithHelp("declare it mutable", Fix.Replace(loc, "mut maybe"))

	if len(d.Extras) != 1 {
		t.Fatalf("expected 1 extra entry, got %d", len(d.Extras))
	}
	if d.Extras[0].Kind != ExtraText || d.Extras[0].Text.Kind != "help" {
		t.Fatalf("expected a help text extra, got %#v", d.Extras[0])
	}
	fixes := d.Extras[0].Text.Fixes
	if len(fixes) != 1 || fixes[0].Location != loc || fixes[0].NewText != "mut maybe" {
		t.Fatalf("unexpected fixes: %#v", fixes)
	}
	if len(d.Labels) != 0 {
		t.Fatalf("a fix must not add or replace labels, got %#v", d.Labels)
	}
}

func TestWithPrimaryLabelSetsFilePath(t *testing.T) {
	d := NewError("x")
	samplePath := "sample" + peeper.SourceExt
	loc := testLoc(samplePath, 3, 2)
	d.WithPrimaryLabel(loc, "here")

	if d.FilePath != samplePath {
		t.Fatalf("expected filepath %s, got %q", samplePath, d.FilePath)
	}
	if len(d.Labels) != 1 || d.Labels[0].Style != Primary {
		t.Fatalf("expected one primary label, got %#v", d.Labels)
	}
}

func TestEmitterAlignsHeaderAndHelpWithGutter(t *testing.T) {
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(colors.LogFormatNormal)
	defer colors.SetLogFormat(prevFormat)

	var out bytes.Buffer
	emitter := NewEmitter(&out)
	samplePath := "sample" + peeper.SourceExt
	emitter.cache.AddSource(samplePath, strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"line 10",
		"line 11",
		"let value = 1;",
	}, "\n"))

	loc := source.NewLocation(samplePath, source.Position{Line: 12, Column: 1}, source.Position{Line: 12, Column: 4})
	diag := NewError("bad").
		WithCode("P0005").
		WithPrimaryLabel(loc, "bad").
		WithHelp("use const instead")

	emitter.Emit(diag)
	text := out.String()

	if !strings.Contains(text, "\n   --> "+samplePath+":12:1\n") {
		t.Fatalf("expected aligned location header, got:\n%s", text)
	}
	if !strings.Contains(text, "\n   | \n11 | line 11\n12 | let value = 1;\n") {
		t.Fatalf("expected aligned blank gutter and context line, got:\n%s", text)
	}
	if !strings.Contains(text, "\n   = help: use const instead\n") {
		t.Fatalf("expected help aligned with gutter, got:\n%s", text)
	}
}

func emitFixForTest(t *testing.T, format colors.LogFormat, sourceText string, fixes ...CodeFix) string {
	t.Helper()
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(format)
	defer colors.SetLogFormat(prevFormat)

	var out bytes.Buffer
	emitter := NewEmitter(&out)
	emitter.cache.AddSource("sample"+peeper.SourceExt, sourceText)
	emitter.Emit(NewError("broken").
		WithCode("P9999").
		WithPrimaryLabel(fixes[0].Location, "").
		WithHelp("apply the fix", fixes...))
	return out.String()
}

func fixLocForTest(line, startColumn, endColumn int) *source.Location {
	return source.NewLocation("sample"+peeper.SourceExt,
		source.Position{Line: line, Column: startColumn}, source.Position{Line: line, Column: endColumn})
}

func TestEmitterShowsFixedLineUnderItsText(t *testing.T) {
	tests := []struct {
		name   string
		source string
		fixes  []CodeFix
		want   string
	}{
		{name: "insertion", source: "let total = 5\n", fixes: []CodeFix{Fix.Insert(fixLocForTest(1, 14, 14), ";")},
			want: "  = help: apply the fix\n  | \n1 | let total = 5;\n"},
		{name: "replacement", source: "return totl;\n", fixes: []CodeFix{Fix.Replace(fixLocForTest(1, 8, 12), "total")},
			want: "  = help: apply the fix\n  | \n1 | return total;\n"},
		{name: "removal keeps one space", source: "let mut total = 5;\n", fixes: []CodeFix{Fix.Remove(fixLocForTest(1, 5, 8))},
			want: "  = help: apply the fix\n  | \n1 | let total = 5;\n"},
		{name: "two fixes on one line", source: "if ready {\n", fixes: []CodeFix{Fix.Insert(fixLocForTest(1, 4, 4), "("), Fix.Insert(fixLocForTest(1, 9, 9), ")")},
			want: "  = help: apply the fix\n  | \n1 | if (ready) {\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := emitFixForTest(t, colors.LogFormatNormal, test.source, test.fixes...)
			if !strings.Contains(got, test.want) {
				t.Fatalf("expected %q in:\n%s", test.want, got)
			}
		})
	}
}

func TestEmitterMarksOnlyChangedTextInColour(t *testing.T) {
	removal := emitFixForTest(t, colors.LogFormatANSI, "let mut total = 5;\n", Fix.Remove(fixLocForTest(1, 5, 8)))
	if !strings.Contains(removal, string(colors.FIX_REMOVED)+"mut"+string(colors.RESET)) {
		t.Fatalf("expected removed text in the removal colour, got:\n%q", removal)
	}

	replacement := emitFixForTest(t, colors.LogFormatANSI, "return totl;\n", Fix.Replace(fixLocForTest(1, 8, 12), "total"))
	if !strings.Contains(replacement, string(colors.FIX_ADDED)+"a"+string(colors.RESET)) {
		t.Fatalf("expected only the inserted letter in the addition colour, got:\n%q", replacement)
	}
	if strings.Contains(replacement, string(colors.FIX_REMOVED)) {
		t.Fatalf("a fix that adds text must not show removed text, got:\n%q", replacement)
	}
}

func TestEmitterKeepsMarginUnbrokenBeforeHelp(t *testing.T) {
	withHelp := emitFixForTest(t, colors.LogFormatNormal, "let total = 5\n", Fix.Insert(fixLocForTest(1, 14, 14), ";"))
	if !strings.Contains(withHelp, "^\n  | \n  = help: apply the fix\n") {
		t.Fatalf("expected a margin line between the snippet and its help, got:\n%s", withHelp)
	}

	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(colors.LogFormatNormal)
	defer colors.SetLogFormat(prevFormat)
	var out bytes.Buffer
	emitter := NewEmitter(&out)
	emitter.cache.AddSource("sample"+peeper.SourceExt, "let total = 5\n")
	emitter.Emit(NewError("broken").WithCode("P9999").WithPrimaryLabel(fixLocForTest(1, 14, 14), ""))
	if !strings.HasSuffix(out.String(), "^\n\n") {
		t.Fatalf("expected an error without help to end with a blank line, got:\n%q", out.String())
	}
}

func TestFixInsertKeepsExistingSource(t *testing.T) {
	got := emitFixForTest(t, colors.LogFormatNormal, "let total = 5\n", Fix.Insert(fixLocForTest(1, 5, 10), "mut "))
	if !strings.Contains(got, "1 | let mut total = 5\n") {
		t.Fatalf("expected insertion before `total` without removing it, got:\n%s", got)
	}
}

func TestEmitterEndsEveryDiagnosticWithOneBlankLine(t *testing.T) {
	withFix := emitFixForTest(t, colors.LogFormatNormal, "let total = 5\n", Fix.Insert(fixLocForTest(1, 14, 14), ";"))
	if !strings.HasSuffix(withFix, "1 | let total = 5;\n\n") || strings.HasSuffix(withFix, "\n\n\n") {
		t.Fatalf("expected exactly one blank line after a fixed line, got:\n%q", withFix)
	}
}

func TestEmitterSkipsFixItCannotPlace(t *testing.T) {
	got := emitFixForTest(t, colors.LogFormatNormal, "let total = 5\n", Fix.Replace(fixLocForTest(1, 40, 41), ";"))
	if !strings.Contains(got, "  = help: apply the fix\n") || strings.Contains(got, "1 | let total = 5;") {
		t.Fatalf("expected the text without a fixed line, got:\n%s", got)
	}
}

func TestEmitterPlacesFixesByCharacterNotByte(t *testing.T) {
	tests := []struct {
		name   string
		source string
		fixes  []CodeFix
		want   string
	}{
		{name: "accented letter before the fix", source: "let s = \"café\"\n", fixes: []CodeFix{Fix.Insert(fixLocForTest(1, 15, 15), ";")},
			want: "1 | let s = \"café\";\n"},
		{name: "wide characters before the fix", source: "let s = \"日本語\"\n", fixes: []CodeFix{Fix.Insert(fixLocForTest(1, 14, 14), ";")},
			want: "1 | let s = \"日本語\";\n"},
		{name: "removal after an accented letter", source: "let s = \"héllo\"; let mut total = 5;\n", fixes: []CodeFix{Fix.Remove(fixLocForTest(1, 22, 25))},
			want: "1 | let s = \"héllo\"; let total = 5;\n"},
		{name: "tab before the fix", source: "\tlet total = 5\n", fixes: []CodeFix{Fix.Insert(fixLocForTest(1, 15, 15), ";")},
			want: "let total = 5;\n"},
		{name: "removal directly followed by an insertion", source: "let mut total = 5;\n", fixes: []CodeFix{Fix.Remove(fixLocForTest(1, 5, 8)), Fix.Insert(fixLocForTest(1, 8, 8), "x")},
			want: "1 | let x total = 5;\n"},
		{name: "empty removal keeps both spaces", source: "a  b\n", fixes: []CodeFix{Fix.Remove(fixLocForTest(1, 3, 3))},
			want: "1 | a  b\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := emitFixForTest(t, colors.LogFormatNormal, test.source, test.fixes...)
			if !strings.Contains(got, test.want) {
				t.Fatalf("expected %q in:\n%s", test.want, got)
			}
		})
	}
}

func TestEmitterDrawsNoFixItCannotShowFaithfully(t *testing.T) {
	spansLines := source.NewLocation("sample"+peeper.SourceExt, source.Position{Line: 1, Column: 5}, source.Position{Line: 2, Column: 2})
	tests := []struct {
		name   string
		source string
		fixes  []CodeFix
	}{
		{name: "column past the end of a line with a tab", source: "\tlet a = 5\n", fixes: []CodeFix{Fix.Replace(fixLocForTest(1, 40, 41), ";")}},
		{name: "a fix that spans two lines hides the others too", source: "let a = (\n5;\n", fixes: []CodeFix{Fix.Remove(spansLines), Fix.Insert(fixLocForTest(2, 2, 2), ")")}},
		{name: "overlapping fixes", source: "let total = 5;\n", fixes: []CodeFix{Fix.Remove(fixLocForTest(1, 5, 10)), Fix.Remove(fixLocForTest(1, 7, 12))}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := emitFixForTest(t, colors.LogFormatNormal, test.source, test.fixes...)
			if !strings.Contains(got, "  = help: apply the fix\n\n") {
				t.Fatalf("expected the help text with no fixed line under it, got:\n%s", got)
			}
		})
	}
}

func TestEmitterMarksOnlyDroppedLettersWhenReplacementShortens(t *testing.T) {
	got := emitFixForTest(t, colors.LogFormatANSI, "return totall;\n", Fix.Replace(fixLocForTest(1, 8, 14), "total"))
	if !strings.Contains(got, string(colors.FIX_REMOVED)+"l"+string(colors.RESET)) || strings.Contains(got, string(colors.FIX_ADDED)) {
		t.Fatalf("expected only the dropped letter marked as removed, got:\n%q", got)
	}
	accent := emitFixForTest(t, colors.LogFormatANSI, "let é = 1\n", Fix.Replace(fixLocForTest(1, 5, 6), "è"))
	if !strings.Contains(accent, string(colors.FIX_ADDED)+"è"+string(colors.RESET)) {
		t.Fatalf("expected the whole replaced character marked, got:\n%q", accent)
	}
}

func TestEmitterSizesMarginForFixLines(t *testing.T) {
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(colors.LogFormatNormal)
	defer colors.SetLogFormat(prevFormat)

	var out bytes.Buffer
	emitter := NewEmitter(&out)
	emitter.cache.AddSource("sample"+peeper.SourceExt, strings.Repeat("skip\n", 8)+"nine\nten\n")
	emitter.Emit(NewError("broken").
		WithCode("P9999").
		WithPrimaryLabel(fixLocForTest(9, 1, 5), "").
		WithNote("continue on the next line", Fix.Insert(fixLocForTest(10, 4, 4), ";")))
	got := out.String()
	if !strings.Contains(got, "\n 9 | nine\n") || !strings.Contains(got, "  = note: continue on the next line\n   | \n10 | ten;\n") {
		t.Fatalf("expected a two-digit margin and the fix under its note, got:\n%s", got)
	}
}

func TestEmitterTidiesSpacesAroundARemovedWord(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{"after an opening bracket", "fn Add(mut a: i32) {\n", "1 | fn Add(a: i32) {\n"},
		{"at the end of the line", "let value mut\n", "1 | let value\n"},
		{"between two words", "let mut total = 5;\n", "1 | let total = 5;\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := strings.Index(test.source, "mut") + 1
			got := emitFixForTest(t, colors.LogFormatNormal, test.source, Fix.Remove(fixLocForTest(1, start, start+3)))
			if !strings.Contains(got, test.want) {
				t.Fatalf("expected %q in:\n%s", test.want, got)
			}
		})
	}
}

func TestEmitterFixResultIgnoresTheOrderFixesWereGiven(t *testing.T) {
	remove, insert := Fix.Remove(fixLocForTest(1, 5, 8)), Fix.Insert(fixLocForTest(1, 5, 5), "x")
	want := "  = help: apply the fix\n  | \n1 | let x total = 5;\n"
	first := emitFixForTest(t, colors.LogFormatNormal, "let mut total = 5;\n", remove, insert)
	second := emitFixForTest(t, colors.LogFormatNormal, "let mut total = 5;\n", insert, remove)
	if !strings.Contains(first, want) || !strings.Contains(second, want) {
		t.Fatalf("expected %q for both orders, got:\n%s\nand:\n%s", want, first, second)
	}
}

func TestEmitterDrawsNoFixWithALineBreakInItsText(t *testing.T) {
	got := emitFixForTest(t, colors.LogFormatNormal, "let total = 5\n", Fix.Insert(fixLocForTest(1, 14, 14), ";\nreturn"))
	if !strings.Contains(got, "  = help: apply the fix\n\n") {
		t.Fatalf("expected the help text with no fixed line under it, got:\n%s", got)
	}
}

func TestEmitterSeparatesAFixedLineFromTheNextText(t *testing.T) {
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(colors.LogFormatNormal)
	defer colors.SetLogFormat(prevFormat)

	var out bytes.Buffer
	emitter := NewEmitter(&out)
	emitter.cache.AddSource("sample"+peeper.SourceExt, "let total = 5\n")
	at := fixLocForTest(1, 14, 14)
	emitter.Emit(NewError("broken").WithCode("P9999").WithPrimaryLabel(at, "").
		WithHelp("first", Fix.Insert(at, ";")).
		WithNote("second").
		WithNote("third"))
	want := "  = help: first\n  | \n1 | let total = 5;\n  | \n  = note: second\n  = note: third\n\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("expected one margin line after the fixed line and none between plain notes, got:\n%s", out.String())
	}
}
