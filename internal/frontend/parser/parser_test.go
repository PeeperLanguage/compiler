package parser

import (
	"fmt"
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/source"
)

const program = `import "app/util" as u;
import "core:io";

fn add(a: i32, b: i32) -> i32 {
    let sum: i32 = a + b * 2;
    return sum;
}

fn main() {
    let x = add(1, (2 + 3)) == 7;
    log("hi");
    return;
}
`

func TestParse(t *testing.T) {
	m, errs := Parse(source.NewFile("main.peep", program))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := `module
  import "app/util" as u
  import "core:io"
  function add
    parameter a: i32
    parameter b: i32
    result i32
    block
      let sum: i32
        binary +
          a
          binary *
            b
            2
      return
        sum
  function main
    block
      let x
        binary ==
          call
            add
            1
            binary +
              2
              3
          7
      call
        log
        "hi"
      return
`
	if got := dump(m); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestParseRecoversAfterMissingSemicolon(t *testing.T) {
	m, errs := Parse(source.NewFile("main.peep", "fn f() { let a = 1 let b = 2; return b; }"))
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "expected `;`") {
		t.Fatalf("errors = %v", errs)
	}
	if got := dump(m); !strings.Contains(got, "return\n        b\n") {
		t.Fatalf("parsing did not continue after the error: %s", got)
	}
}

// generated returns a file of n functions like the demo program.
func generated(n int) string {
	var b strings.Builder
	b.WriteString("import \"app/util\" as u;\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "fn f%d(a: i32, b: i32) -> i32 {\n    let x: i32 = a + b * 2;\n    let y = f%d(x, a) + 10;\n    let s = \"text\";\n    return x * y - (a + 1);\n}\n", i, max(i-1, 0))
	}
	return b.String()
}

func TestParseOneErrorForUnexpectedTopLevelText(t *testing.T) {
	m, errs := Parse(source.NewFile("main.peep", "const a: i32 = 0;\nlet b = 1;\nfn f() {}"))
	if len(errs) != 1 || errs[0].Message != "module scope expects declaration" {
		t.Fatalf("errors = %v", errs)
	}
	if got := dump(m); got != "module\n  function f\n    block\n" {
		t.Fatalf("parsing did not go on at the next declaration: %s", got)
	}
}

// brokenInputs have errors in many places; parsing them must still give a tree
// that keeps the rules below.
var brokenInputs = []string{
	"", "fn", "fn f(", "fn f(a: ) {", "fn f() { let = ; }", "fn f() { return 1 + ; }",
	"fn f() { g(1, ; }", "import ;", "import \"x\" as ;", "let x = 1;", "fn f() -> { }", "fn f() { ((1 }",
}

// TestNodeEndsNeverDecrease checks the rule NodeAt relies on.
func TestNodeEndsNeverDecrease(t *testing.T) {
	for _, src := range append([]string{program, generated(20)}, brokenInputs...) {
		tree, _ := Parse(source.NewFile("t.peep", src))
		for i := 1; i < len(tree.Nodes); i++ {
			if tree.Nodes[i].Span.End < tree.Nodes[i-1].Span.End {
				t.Fatalf("%q: node %d (%s) ends at %d, before node %d (%s) at %d", src,
					i, tree.Nodes[i].Kind, tree.Nodes[i].Span.End, i-1, tree.Nodes[i-1].Kind, tree.Nodes[i-1].Span.End)
			}
		}
	}
}

// TestNodeAt compares NodeAt with a scan of every node at every offset.
func TestNodeAt(t *testing.T) {
	for _, src := range append([]string{program}, brokenInputs...) {
		tree, _ := Parse(source.NewFile("t.peep", src))
		for offset := 0; offset <= len(src); offset++ {
			want := ast.None
			for i := 1; i < len(tree.Nodes); i++ {
				s := tree.Nodes[i].Span
				if s.Start <= offset && offset < s.End && (want == ast.None || s.End-s.Start < tree.Nodes[want].Span.End-tree.Nodes[want].Span.Start) {
					want = ast.Index(i)
				}
			}
			if got := tree.NodeAt(offset); got != want {
				t.Fatalf("%q offset %d: NodeAt = %d, scan = %d", src, offset, got, want)
			}
		}
	}
}

func BenchmarkNodeAt(b *testing.B) {
	text := generated(5000)
	tree, _ := Parse(source.NewFile("big.peep", text))
	offset := strings.Index(text, "f2500(x, a)") + 1
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tree.NodeAt(offset)
	}
}

func BenchmarkParse(b *testing.B) {
	file := source.NewFile("big.peep", generated(5000))
	b.SetBytes(int64(len(file.Text)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Parse(file)
	}
}

// TestNumberChecks holds the old compiler's messages for number literal
// suffixes and leading zeros. Digits outside the base are the lexer's to report.
func TestNumberChecks(t *testing.T) {
	for _, test := range []struct{ literal, message, fixed string }{
		{"1i", "invalid numeric literal suffix i", ""},
		{"1i_32", "invalid numeric literal suffix i_32", ""},
		{"1i8_", "invalid numeric literal suffix i8_", ""},
		{"1u", "invalid numeric literal suffix u", ""},
		{"1f", "invalid numeric literal suffix f", ""},
		{"0b1i", "invalid numeric literal suffix i", ""},
		{"0o7u", "invalid numeric literal suffix u", ""},
		{"2.4i32", "integer suffix i32 cannot be used on a float literal", ""},
		{"1e3u8", "integer suffix u8 cannot be used on a float literal", ""},
		{"007", "decimal integer literal has a leading zero", "7"},
		{"0_1", "decimal integer literal has a leading zero", "1"},
		{"00", "decimal integer literal has a leading zero", "0"},
		{"0_0_7", "decimal integer literal has a leading zero", "7"},
		{"01i32", "decimal integer literal has a leading zero", "1i32"},
		{"0", "", ""}, {"0.5", "", ""}, {"01.5", "", ""}, {"01f32", "", ""},
		{"1.5f32", "", ""}, {"0x1f_u8", "", ""}, {"0xffu24", "", ""}, {"1_000i64", "", ""}, {"1e3f64", "", ""},
	} {
		src := "fn f() { " + test.literal + "; }"
		_, errs := Parse(source.NewFile("t.peep", src))
		start := strings.Index(src, test.literal)
		if test.message == "" {
			if len(errs) != 0 {
				t.Errorf("%s: unexpected %q", test.literal, errs[0].Message)
			}
			continue
		}
		if len(errs) != 1 || errs[0].Message != test.message || errs[0].Labels[0].Span != (source.Span{Start: start, End: start + len(test.literal)}) {
			t.Errorf("%s: got %v, want %q on the whole literal", test.literal, errs, test.message)
			continue
		}
		if test.fixed != "" {
			fix := errs[0].Notes[0].Fixes[0]
			if got := src[:fix.Span.Start] + fix.NewText + src[fix.Span.End:]; got != "fn f() { "+test.fixed+"; }" {
				t.Errorf("%s: fix gives %q, want %q", test.literal, got, test.fixed)
			}
		}
	}
}
