package diagnostics

import (
	"bytes"
	"strings"
	"testing"

	"compiler/internal/source"
	"compiler/pkg/colors"
	"compiler/pkg/peeper"
)

func TestEmitterHandlesMultiplePrimaryLabelsWithoutPanic(t *testing.T) {
	var out bytes.Buffer
	emitter := NewEmitter(&out)
	mainPath := "main" + peeper.SourceExt
	emitter.cache.AddSource(mainPath, "let a = 1\nlet b = 2\n")

	loc1 := testLoc(mainPath, 1, 5)
	loc2 := testLoc(mainPath, 2, 5)

	diag := NewError("broken diagnostic shape")
	diag.FilePath = mainPath
	diag.Labels = []Label{
		{Location: loc1, Message: "first", Style: Primary},
		{Location: loc2, Message: "second", Style: Primary},
	}

	emitter.Emit(diag)

	text := out.String()
	if !strings.Contains(text, "first") {
		t.Fatalf("expected first label in output, got:\n%s", text)
	}
	if !strings.Contains(text, "second") {
		t.Fatalf("expected second label to remain visible in output, got:\n%s", text)
	}
}

// emitPlain prints diag over the given sources with colours off.
func emitPlain(t *testing.T, sources map[string]string, diag *Diagnostic) string {
	t.Helper()
	prevFormat := colors.CurrentLogFormat()
	colors.SetLogFormat(colors.LogFormatNormal)
	defer colors.SetLogFormat(prevFormat)

	var out bytes.Buffer
	emitter := NewEmitter(&out)
	for path, text := range sources {
		emitter.cache.AddSource(path, text)
	}
	emitter.Emit(diag)
	return out.String()
}

func spanLoc(file string, startLine, startCol, endLine, endCol int) *source.Location {
	return source.NewLocation(file, source.Position{Line: startLine, Column: startCol}, source.Position{Line: endLine, Column: endCol})
}

const snippetSource = "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight"

func TestEmitterPrintsEachSourceLineOnce(t *testing.T) {
	path := "main" + peeper.SourceExt
	for _, test := range []struct {
		name      string
		primary   *source.Location
		secondary *source.Location
		want      string
	}{
		{
			name:      "labels on neighbouring lines",
			primary:   spanLoc(path, 4, 1, 4, 5),
			secondary: spanLoc(path, 3, 1, 3, 6),
			want: "[X]: bad\n  --> " + path + ":4:1\n  | \n" +
				"2 | two\n3 | three\n  | ----- context\n4 | four\n  | ^^^^ here\n\n",
		},
		{
			name:      "two labels on one line in column order",
			primary:   spanLoc(path, 3, 4, 3, 6),
			secondary: spanLoc(path, 3, 1, 3, 3),
			want: "[X]: bad\n  --> " + path + ":3:4\n  | \n" +
				"2 | two\n3 | three\n  | -- context\n  |    ^^ here\n\n",
		},
		{
			name:      "labels over the same lines",
			primary:   spanLoc(path, 3, 1, 4, 5),
			secondary: spanLoc(path, 3, 1, 4, 5),
			want: "[X]: bad\n  --> " + path + ":3:1\n  | \n" +
				"2 | two\n3 | three\n  | ^^^^^\n  | -----\n4 | four\n  | ^^^^ here\n  | ---- context\n\n",
		},
		{
			name:      "secondary label above the primary keeps the primary in the header",
			primary:   spanLoc(path, 7, 1, 7, 6),
			secondary: spanLoc(path, 2, 1, 2, 4),
			want: "[X]: bad\n  --> " + path + ":7:1\n  | \n" +
				"1 | one\n2 | two\n  | --- context\n...\n6 | six\n7 | seven\n  | ^^^^^ here\n\n",
		},
		{
			name:      "one line between two labels is context, not a gap",
			primary:   spanLoc(path, 5, 1, 5, 5),
			secondary: spanLoc(path, 3, 1, 3, 6),
			want: "[X]: bad\n  --> " + path + ":5:1\n  | \n" +
				"2 | two\n3 | three\n  | ----- context\n4 | four\n5 | five\n  | ^^^^ here\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			diag := NewError("bad").WithCode("X").
				WithPrimaryLabel(test.primary, "here").
				WithSecondaryLabel(test.secondary, "context")
			if got := emitPlain(t, map[string]string{path: snippetSource}, diag); got != test.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, test.want)
			}
		})
	}
}

func TestEmitterSingleLabelLayout(t *testing.T) {
	path := "main" + peeper.SourceExt
	diag := NewError("bad").WithCode("X").
		WithPrimaryLabel(spanLoc(path, 4, 1, 4, 5), "here").
		Help("fix it")
	want := "[X]: bad\n  --> " + path + ":4:1\n  | \n3 | three\n4 | four\n  | ^^^^ here\n  | \n  = help: fix it\n\n"
	if got := emitPlain(t, map[string]string{path: snippetSource}, diag); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmitterGivesEachFileItsHeaderWithThePrimaryFileFirst(t *testing.T) {
	first, second := "a"+peeper.SourceExt, "z"+peeper.SourceExt
	diag := NewError("bad").WithCode("X").
		WithPrimaryLabel(spanLoc(second, 2, 1, 2, 4), "here").
		WithSecondaryLabel(spanLoc(first, 3, 1, 3, 6), "context")
	want := "[X]: bad\n  --> " + second + ":2:1\n  | \n1 | one\n2 | two\n  | ^^^ here\n\n" +
		"  --> " + first + ":3:1\n  | \n2 | two\n3 | three\n  | ----- context\n\n"
	if got := emitPlain(t, map[string]string{first: snippetSource, second: snippetSource}, diag); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
