package diag

import (
	"strings"
	"testing"

	"compiler/internal/source"
	"compiler/pkg/colors"
)

func span(file *source.File, text string) source.Span {
	start := strings.Index(file.Text, text)
	return source.Span{Start: start, End: start + len(text)}
}

func render(diagnostics ...Diagnostic) string {
	var out strings.Builder
	Print(&out, diagnostics, false)
	return out.String()
}

func TestPrintTwoFiles(t *testing.T) {
	main := source.NewFile("src/main.peep", "fn main() -> i32 {\n    global::exit(0);\n}\n")
	global := source.NewFile("lib/global.peep", "#[extern(\"exit\")]\nfn exit(code: i32);\n")
	d := New(testError("M0004"), "`exit` is not exported from `global`").
		Label(main, span(main, "global::exit"), "use of unexported symbol").
		Label(global, span(global, "exit("), "defined here").
		Note("symbols with uppercase are exported otherwise private")
	d.Labels[1].Span.End-- // "exit" without "("
	want := `[M0004]: ` + "`exit` is not exported from `global`" + `
  --> src/main.peep:2:5
  | 
1 | fn main() -> i32 {
2 |     global::exit(0);
  |     ^^^^^^^^^^^^ use of unexported symbol

  --> lib/global.peep:2:4
  | 
1 | #[extern("exit")]
2 | fn exit(code: i32);
  |    ---- defined here
  | 
  = note: symbols with uppercase are exported otherwise private

Compilation failed with 1 error(s)
`
	if got := render(*d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintHelpWithFix(t *testing.T) {
	file := source.NewFile("a.peep", "struct S {\n    value: i32,\n}\n")
	comma := span(file, ",")
	d := New(testStyle("S0001"), "trailing comma is unnecessary").
		Label(file, comma, "").
		Help("remove this comma", Fix{File: file, Span: comma})
	want := `[S0001]: trailing comma is unnecessary
  --> a.peep:2:15
  | 
1 | struct S {
2 |     value: i32,
  |               ^
  | 
  = help: remove this comma
  | 
2 |     value: i32

`
	if got := render(*d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintOrderAndSummary(t *testing.T) {
	file := source.NewFile("a.peep", "a\nb\n")
	late := New(testWarning("W0001"), "late").Label(file, span(file, "b"), "")
	early := New(testError("L0001"), "early").Label(file, span(file, "a"), "")
	nowhere := New(testError("M0001"), "no place")
	got := render(*late, *early, *nowhere)
	order := []string{"[M0001]", "[L0001]", "[W0001]", "Compilation failed with 2 error(s) and 1 warning(s)"}
	last := -1
	for _, part := range order {
		at := strings.Index(got, part)
		if at <= last {
			t.Fatalf("%q out of order in:\n%s", part, got)
		}
		last = at
	}
}

func TestPrintSpanOverLinesTabsAndWideCharacters(t *testing.T) {
	file := source.NewFile("a.peep", "\tlet 名 = (\r\n  1)\r\n")
	d := New(testError("P0003"), "bad").Label(file, source.Span{Start: strings.Index(file.Text, "名"), End: strings.Index(file.Text, ")") + 1}, "here")
	want := `[P0003]: bad
  --> a.peep:1:6
  | 
1 |     let 名 = (
  |         ^^^^^^
2 |   1)
  | ^^^^ here

Compilation failed with 1 error(s)
`
	if got := render(*d); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrintNeverFailsOnBadSpans(t *testing.T) {
	file := source.NewFile("a.peep", "x\n")
	d := New(testError("L0001"), "outside").Label(file, source.Span{Start: 40, End: 2}, "").
		Help("broken fix is not drawn", Fix{File: file, Span: source.Span{Start: 1, End: 99}, NewText: "y"})
	if got := render(*d); !strings.Contains(got, "[L0001]: outside") || !strings.Contains(got, "= help: broken fix is not drawn") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestPrintColor(t *testing.T) {
	file := source.NewFile("a.peep", "x\n")
	var out strings.Builder
	Print(&out, []Diagnostic{*New(testWarning("W0001"), "w").Label(file, span(file, "x"), "")}, true)
	if !strings.Contains(out.String(), string(colors.BOLD_YELLOW)+"[W0001]"+string(colors.RESET)) {
		t.Fatalf("warning header not colored:\n%q", out.String())
	}
}

func testError(id string) Code   { return Code{id, Error} }
func testWarning(id string) Code { return Code{id, Warning} }
func testStyle(id string) Code   { return Code{id, Info} }
