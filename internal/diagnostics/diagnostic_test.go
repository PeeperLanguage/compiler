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

func TestWithCodeReplacementAddsOrderedExtra(t *testing.T) {
	d := NewError("immutable")
	loc := testLoc("main"+peeper.SourceExt, 2, 5)
	d.WithCodeReplacement(loc, "maybe", "mut maybe")

	if len(d.Extras) != 1 {
		t.Fatalf("expected 1 extra entry, got %d", len(d.Extras))
	}
	if d.Extras[0].Kind != ExtraCodeHint {
		t.Fatalf("expected extra kind ExtraCodeHint, got %v", d.Extras[0].Kind)
	}
	hint := d.Extras[0].CodeHint
	if len(hint.Lines) != 2 {
		t.Fatalf("expected 2 hint lines, got %d", len(hint.Lines))
	}
	if hint.Lines[0].Prefix != "-" || hint.Lines[0].Code != "maybe" {
		t.Fatalf("unexpected first replacement line: %#v", hint.Lines[0])
	}
	if hint.Lines[1].Prefix != "+" || hint.Lines[1].Code != "mut maybe" {
		t.Fatalf("unexpected second replacement line: %#v", hint.Lines[1])
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

func TestEmitterSuggestionHeaderUsesBlankGutter(t *testing.T) {
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(colors.LogFormatNormal)
	defer colors.SetLogFormat(prevFormat)

	var out bytes.Buffer
	emitter := NewEmitter(&out)
	samplePath := "sample" + peeper.SourceExt
	emitter.cache.AddSource(samplePath, "value\n")

	loc := source.NewLocation(samplePath, source.Position{Line: 1, Column: 1}, source.Position{Line: 1, Column: 6})
	diag := NewError("replace").
		WithCode("P9999").
		WithPrimaryLabel(loc, "replace").
		WithCodeInsertion(loc, "const value = 1;")

	emitter.Emit(diag)
	text := out.String()

	if !strings.Contains(text, "\n  = suggestion:\n") {
		t.Fatalf("expected suggestion header aligned with gutter, got:\n%s", text)
	}
	if !strings.Contains(text, "\n  | \n") {
		t.Fatalf("expected blank gutter spacer lines, got:\n%s", text)
	}
}
