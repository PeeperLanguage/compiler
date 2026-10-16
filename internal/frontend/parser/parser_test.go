package parser

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/token"
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
  import declaration
    string literal "app/util"
    declared name u
  import declaration
    string literal "core:io"
  function declaration
    declared name add
    parameter
      declared name a
      type name i32
    parameter
      declared name b
      type name i32
    type name i32
    block
      let statement
        declared name sum
        type name i32
        binary expression +
          name reference a
          binary expression *
            name reference b
            number literal 2
      return statement
        name reference sum
  function declaration
    declared name main
    block
      let statement
        declared name x
        binary expression ==
          call expression
            name reference add
            number literal 1
            parentheses
              binary expression +
                number literal 2
                number literal 3
          number literal 7
      expression statement
        call expression
          name reference log
          string literal "hi"
      return statement return;
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
	if got := dump(m); !strings.Contains(got, "return statement\n        name reference b\n") {
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
	m, errs := Parse(source.NewFile("main.peep", "let b = 1;\nx + 1; y();\nfn f() {}"))
	var got []string
	for _, err := range errs {
		got = append(got, err.Message)
	}
	if strings.Join(got, "\n") != "top-level `let` not allowed\nmodule scope expects declaration" {
		t.Fatalf("errors = %q", got)
	}
	if got := dump(m); got != "module\n  function declaration\n    declared name f\n    block {}\n" {
		t.Fatalf("parsing did not go on at the next declaration: %s", got)
	}
}

// brokenInputs have errors in many places; parsing them must still give a tree
// that keeps the rules below.
var brokenInputs = []string{
	"", "fn", "fn f(", "fn f(a: ) {", "fn f() { let = ; }", "fn f() { return 1 + ; }",
	"fn f() { g(1, ; }", "import ;", "import \"x\" as ;", "let x = 1;", "fn f() -> { }", "fn f() { ((1 }",
	"fn f( {", "fn f() { a$b; }", "fn f() { g(1 2); }", "fn f(a: i32 b: i32) {}", "fn f() { fn }",
	"fn f() { ) ; let x = ; }", "fn f() { let a = 1\nfn g() {}", "fn f() { g(a b c); h(1 2); }",
	"fn (", "fn (a: &) f", "fn f<", "fn f<T", "fn f(a: Box<T) {}", "fn f(a: Box<Box<T>) {}", "fn f(a: [", "fn f(a: [4",
	"fn f(a: fn(", "fn f() -> &mut", "fn f() -> i32 from (a", "fn f(a: ??) {}", "fn f(a: a::) {}", "fn f(mut mut) {}",
	"fn f() { a.; }", "fn f() { a[; }", "fn f() { a[1..=]; }", "fn f() { a as ; }", "fn f() { a is b; }",
	"fn f() { a orelse ; }", "fn f() { a |> b; }", "fn f() { a |> b.c(); }", "fn f() { .{ a = }; }", "fn f() { .{ a: 1, b: 2 }; }",
	"fn f() { [_]i32{1, 2; }", "fn f() { [x]i32{}; }", "fn f() { A::B with ; }", "fn f() { Box<T>::; }", "fn f() { print(; }",
	"fn f() { &mut ; }", "fn f() { a < b > c; }", "fn f() { a<b>.{}; }", "fn f() { -; }", "fn f() { a orelse { return; }; }",
	"fn f() { if }", "fn f() { if a }", "fn f() { if a {} else }", "fn f() { if a = b {} }", "fn f() { for }", "fn f() { for x in }",
	"fn f() { for i, in x {} }", "fn f() { for a.b in x {} }", "fn f() { break l; }", "fn f() { match }", "fn f() { match x { }",
	"fn f() { match x { A::B {} } }", "fn f() { match x { A::B => {}, _ => {} } }", "fn f() { match x { let } }",
	"fn f() { match x { A::B with => {} } }", "fn f() { let .{ a = b, _, c } = v; }", "fn f() { let .{ } }", "fn f() { a = ; }",
	"fn f() { let mut ; }", "fn f() { const c = 1; { } }",
	"///", "/// x", "#", "#[", "#[a(", "#[a(1, ] fn f();", "const", "const a", "const a: = ;", "struct", "struct S {",
	"struct S { a: }", "enum E { A: }", "iface I { fn", "iface I { fn (", "type", "type A =", "type A<T", "struct S {} struct",
	"fn f() { let a: i32; }", "fn f() {\nstruct S {}", "/// doc\n#[a] let x = 1;",
}

// TestOneErrorPerMistake checks that each mistake is reported once, and that
// parsing goes on to report the next one.
func TestOneErrorPerMistake(t *testing.T) {
	for _, test := range []struct {
		src    string
		errors []string
	}{
		{"fn f( {", []string{"expected `)`, found `{`"}},
		{"fn", []string{"expected `identifier`, found `end of file`"}},
		{"fn f() { ((1 }", []string{"expected `)`, found `}`"}},
		{"fn f() { g(1, ; }", []string{"expected `)`, found `;`"}},
		{"fn f() { return 1 + ; }", []string{"expected expression, found `;`"}},
		{"fn f() { g(1 2); }", []string{"expected `,`"}},
		{"fn f(a: i32 b: i32) {}", []string{"expected `,`"}},
		{"fn f() { a$b; }", []string{`illegal character "$"`}}, // the parser adds nothing
		{"fn f() { let a = ; let b = ; }", []string{"expected expression, found `;`", "expected expression, found `;`"}},
		{"fn f() { ) ; let x = ; }", []string{"expected expression, found `)`", "expected expression, found `;`"}},
		{"fn f() { g(a b c); h(1 2); }", []string{"expected `,`", "expected `,`"}},
		{"fn f() { let a = 1\nfn g() {}", []string{"expected `;`, found `function`"}},
	} {
		_, errs := Parse(source.NewFile("t.peep", test.src))
		var got []string
		for _, err := range errs {
			got = append(got, err.Message)
		}
		if strings.Join(got, "\n") != strings.Join(test.errors, "\n") {
			t.Errorf("%q:\n got: %q\nwant: %q", test.src, got, test.errors)
		}
	}
}

// TestMissingCommasAreRepaired checks that items without commas are read as if
// the commas were there, with one fix per missing comma.
func TestMissingCommasAreRepaired(t *testing.T) {
	src := "fn f(a: i32 b: i32) { g(a b c); }"
	tree, errs := Parse(source.NewFile("t.peep", src))
	want := `module
  function declaration
    declared name f
    parameter
      declared name a
      type name i32
    parameter
      declared name b
      type name i32
    block
      expression statement
        call expression
          name reference g
          name reference a
          name reference b
          name reference c
`
	if got := dump(tree); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if len(errs) != 2 || len(errs[0].Notes[0].Fixes) != 1 || len(errs[1].Notes[0].Fixes) != 2 {
		t.Fatalf("errors = %v", errs)
	}
	if fix := errs[1].Notes[0].Fixes[1]; src[:fix.Span.Start] != "fn f(a: i32 b: i32) { g(a b" {
		t.Fatalf("second fix at %d", fix.Span.Start)
	}
}

// TestUnclosedMarksTheOpener checks that a missing closer on a later line
// points back at what is still open.
func TestUnclosedMarksTheOpener(t *testing.T) {
	_, errs := Parse(source.NewFile("t.peep", "fn f() {\n    g(1;\n}"))
	if len(errs) != 1 || len(errs[0].Labels) != 1 {
		t.Fatalf("same line: errors = %v", errs)
	}
	_, errs = Parse(source.NewFile("t.peep", "fn f() {\n    let a = 1;\n"))
	if len(errs) != 1 || len(errs[0].Labels) != 2 || errs[0].Labels[1].Message != "this `{` is still open" {
		t.Fatalf("later line: errors = %v", errs)
	}
}

// FuzzParse checks that any input parses, and that the tree keeps its rules:
// every span lies in the text and starts before it ends, and node ends never
// decrease. Run it with: go test -fuzz=FuzzParse ./internal/frontend/parser
func FuzzParse(f *testing.F) {
	for _, src := range append([]string{program}, brokenInputs...) {
		f.Add(src)
	}
	f.Fuzz(func(t *testing.T, src string) {
		tree, _ := Parse(source.NewFile("fuzz.peep", src))
		for i := 1; i < len(tree.Nodes); i++ {
			s := tree.Nodes[i].Span
			if s.Start < 0 || s.Start > s.End || s.End > len(src) || s.End < tree.Nodes[i-1].Span.End {
				t.Fatalf("%q: node %d (%s) has span %d-%d after a node ending at %d", src, i, tree.Nodes[i].Kind, s.Start, s.End, tree.Nodes[i-1].Span.End)
			}
		}
	})
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
// suffixes. Digits outside the base are the lexer's to report; a leading zero
// (007) is left to a later lint pass.
func TestNumberChecks(t *testing.T) {
	for _, test := range []struct{ literal, message string }{
		{"1i", "invalid numeric literal suffix i"},
		{"1i_32", "invalid numeric literal suffix i_32"},
		{"1i8_", "invalid numeric literal suffix i8_"},
		{"1u", "invalid numeric literal suffix u"},
		{"1f", "invalid numeric literal suffix f"},
		{"0b1i", "invalid numeric literal suffix i"},
		{"0o7u", "invalid numeric literal suffix u"},
		{"2.4i32", "integer suffix i32 cannot be used on a float literal"},
		{"1e3u8", "integer suffix u8 cannot be used on a float literal"},
		{"0", ""}, {"0.5", ""}, {"007", ""}, {"01i32", ""}, {"01f32", ""},
		{"1.5f32", ""}, {"0x1f_u8", ""}, {"0xffu24", ""}, {"1_000i64", ""}, {"1e3f64", ""},
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
		}
	}
}

func TestParseSignatures(t *testing.T) {
	src := `fn (self: &mut Counter) Add<T, U>(mut delta: i32, scale: f64 = 1, &Self) -> &T from (self, delta) {}
fn exit(code: i32);
fn Use(callback: fn(value: &Box, i32) -> &Box from value, items: []?*T, view: [..]u8, grid: [4]rawptr) -> ?a::Box<Box<T>>;
`
	tree, errs := Parse(source.NewFile("t.peep", src))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := `module
  function declaration
    parameter
      declared name self
      reference type mut
        type name Counter
    declared name Add
    declared name T
    declared name U
    parameter mut
      declared name delta
      type name i32
    parameter
      declared name scale
      type name f64
      number literal 1
    parameter
      reference type
        type name Self
    reference type
      type name T
    name reference self
    name reference delta
    block {}
  function declaration
    declared name exit
    parameter
      declared name code
      type name i32
  function declaration
    declared name Use
    parameter
      declared name callback
      function type
        parameter
          declared name value
          reference type
            type name Box
        parameter
          type name i32
        reference type
          type name Box
        name reference value
    parameter
      declared name items
      dynamic array type
        optional type
          owned pointer type
            type name T
    parameter
      declared name view
      slice type
        type name u8
    parameter
      declared name grid
      fixed array type
        number literal 4
        raw pointer type rawptr
    optional type
      path
        type name a
        generic type
          type name Box
          generic type
            type name Box
            type name T
`
	if got := dump(tree); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSignatureErrors(t *testing.T) {
	for _, test := range []struct{ src, message, fixed string }{
		{"fn f() i32 {}", "expected `->` before the return type", "fn f() -> i32 {}"},
		{"fn f(mut mut a: i32) {}", "`mut` is repeated", "fn f(mut a: i32) {}"},
		{"fn f(a: ??i32) {}", "redundant optional marker", "fn f(a: ?i32) {}"},
		{"fn f(mut i32) {}", "mutable parameter requires a named binding", ""},
		{"fn f(a: [n]i32) {}", "expected array length", ""},
		{"fn f(a: ) {}", "expected type, found `)`", ""},
		{"fn f() -> i32", "missing function body", ""},
		{"fn f<T U>() {}", "expected `>`, found `identifier`", ""},
	} {
		_, errs := Parse(source.NewFile("t.peep", test.src))
		if len(errs) != 1 || errs[0].Message != test.message {
			t.Errorf("%q: errors %v, want %q", test.src, errs, test.message)
			continue
		}
		if test.fixed != "" {
			fix := errs[0].Notes[0].Fixes[0]
			if got := test.src[:fix.Span.Start] + fix.NewText + test.src[fix.Span.End:]; got != test.fixed {
				t.Errorf("%q: fix gives %q, want %q", test.src, got, test.fixed)
			}
		}
	}
}

// TestPrecedence checks how operators group, from loosest to tightest, and
// that operators of one precedence group to the left.
func TestPrecedence(t *testing.T) {
	for src, want := range map[string]string{
		"a || b && c":           "(a || (b && c))",
		"a | b ^ c & d":         "(a | (b ^ (c & d)))",
		"a & b == c":            "(a & (b == c))",
		"a == b < c":            "(a == (b < c))",
		"a < b << c":            "(a < (b << c))",
		"a << b + c":            "(a << (b + c))",
		"a + b * c":             "(a + (b * c))",
		"a - b - c":             "((a - b) - c)",
		"a * b % c / d":         "(((a * b) % c) / d)",
		"(a + b) * f(c, d + e)": "((a + b) * f(c, (d + e)))",
		"a != b >= c > d <= e":  "(a != (((b >= c) > d) <= e))",
		"-a * b":                "(-a * b)",
		"-a.b(c)[d]":            "-a.b(c)[d]",
		"!a && b":               "(!a && b)",
		"&mut a + b":            "(&mut a + b)",
		"a + b as i64":          "(a + (b as i64))",
		"a as i32 * b":          "((a as i32) * b)",
		"a is E::B && c":        "((a is E::B) && c)",
		"a orelse b orelse c":   "(a orelse (b orelse c))",
		"a || b orelse c":       "((a || b) orelse c)",
		"a |> f(b) |> g()":      "((a |> f(b)) |> g())",
		"a |> len() as i32":     "((a |> len()) as i32)",
		"a + b |> f()":          "((a + b) |> f())",
		"x.y.z[0][i..j]":        "x.y.z[0][i..j]",
	} {
		tree, errs := Parse(source.NewFile("t.peep", "fn f() { "+src+"; }"))
		if len(errs) != 0 {
			t.Errorf("%s: %v", src, errs)
			continue
		}
		block := tree.FunctionDecl(tree.List(tree.Module(tree.Root).Declarations)[0]).Body
		statement := tree.List(tree.Block(block).Statements)[0]
		if got := grouped(tree, tree.ExpressionStatement(statement).Expression); got != want {
			t.Errorf("%s: got %s, want %s", src, got, want)
		}
	}
}

// grouped writes an expression with each binary operation in parentheses.
func grouped(t *ast.Tree, i ast.Index) string {
	switch n := t.Nodes[i]; n.Kind {
	case ast.KindBinaryExpression:
		b := t.BinaryExpression(i)
		return "(" + grouped(t, b.Left) + " " + n.Op.String() + " " + grouped(t, b.Right) + ")"
	case ast.KindCastExpression:
		c := t.CastExpression(i)
		return "(" + grouped(t, c.Value) + " as " + t.Text(c.Type) + ")"
	case ast.KindIsExpression:
		c := t.IsExpression(i)
		return "(" + grouped(t, c.Value) + " is " + t.Text(c.Case) + ")"
	case ast.KindOrElseExpression:
		o := t.OrElseExpression(i)
		return "(" + grouped(t, o.Value) + " orelse " + grouped(t, o.Fallback) + ")"
	case ast.KindPipeExpression:
		o := t.PipeExpression(i)
		return "(" + grouped(t, o.Value) + " |> " + grouped(t, o.Call) + ")"
	case ast.KindUnaryExpression, ast.KindReferenceExpression:
		prefix := strings.TrimSpace(t.Text(i)[:t.Nodes[t.Operand(i).Value].Span.Start-n.Span.Start])
		if n.Op == token.MUT {
			prefix += " "
		}
		return prefix + grouped(t, t.Operand(i).Value)
	case ast.KindSelectorExpression:
		s := t.SelectorExpression(i)
		return grouped(t, s.Value) + "." + t.Text(s.Field)
	case ast.KindIndexExpression:
		x := t.IndexExpression(i)
		return grouped(t, x.Value) + "[" + t.Text(x.Index) + "]"
	case ast.KindCallExpression:
		c := t.CallExpression(i)
		var args []string
		for _, arg := range t.List(c.Arguments) {
			args = append(args, grouped(t, arg))
		}
		return grouped(t, c.Callee) + "(" + strings.Join(args, ", ") + ")"
	}
	return t.Text(i)
}

func TestParseDeclarations(t *testing.T) {
	src := `/// The answer.
/// Twice checked.
const Answer: i32 = 42;
const Untyped = 1;
#[extern("rt_exit")]
fn exit(code: i32);
struct Pair<T> {
    first: T,
    second: struct { flag: bool },
};
enum Status { Ready, Waiting: Pair<i32>, }
iface Summer {
    fn (&Self) sum() -> i32,
    fn (self: &Self) add<T>(value: T)
}
type Alias<T> = ?T;
`
	tree, errs := Parse(source.NewFile("t.peep", src))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := `module
  const declaration
    metadata
      doc comment /// The answer.
/// Twice checked.
    declared name Answer
    type name i32
    number literal 42
  const declaration
    declared name Untyped
    number literal 1
  function declaration
    metadata
      attribute
        name reference extern
        string literal "rt_exit"
    declared name exit
    parameter
      declared name code
      type name i32
  struct declaration
    declared name Pair
    declared name T
    struct type
      field
        declared name first
        type name T
      field
        declared name second
        struct type
          field
            declared name flag
            type name bool
  enum declaration
    declared name Status
    enum type
      variant
        declared name Ready
      variant
        declared name Waiting
        generic type
          type name Pair
          type name i32
  interface declaration
    declared name Summer
    interface type
      function declaration
        parameter
          reference type
            type name Self
        declared name sum
        type name i32
      function declaration
        parameter
          declared name self
          reference type
            type name Self
        declared name add
        declared name T
        parameter
          declared name value
          type name T
  type alias declaration
    declared name Alias
    declared name T
    optional type
      type name T
`
	if got := dump(tree); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestParsePrelude parses the real prelude, the first file of every program.
func TestParsePrelude(t *testing.T) {
	text, err := os.ReadFile("../../../_builtin_library/core/src/global.peep")
	if err != nil {
		t.Fatal(err)
	}
	if _, errs := Parse(source.NewFile("global.peep", string(text))); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}
}

func TestDeclarationErrors(t *testing.T) {
	for _, test := range []struct{ src, message string }{
		{"let a = 1;", "top-level `let` not allowed"},
		{"a + 1;", "module scope expects declaration"},
		{"#[a, b] fn f();", "only one attribute is allowed per `#[...]` block"},
		{"iface I { fn (&Self) f(a: i32 = 1) }", "interface method defaults are not supported"},
		{"iface I { fn f() }", "expected `(`, found `identifier`"},
		{"type A i32;", "expected `=`, found `identifier`"},
		{"struct S { a i32 }", "expected `:`, found `identifier`"},
		{"enum E { A B }", "expected `,`"},
	} {
		_, errs := Parse(source.NewFile("t.peep", test.src))
		if len(errs) != 1 || errs[0].Message != test.message {
			t.Errorf("%q: errors %v, want %q", test.src, errs, test.message)
		}
	}
}

// TestDocCommentAfterCode checks that a `///` on the line of earlier code is
// not taken as the next declaration's documentation.
func TestDocCommentAfterCode(t *testing.T) {
	tree, _ := Parse(source.NewFile("t.peep", "const a = 1; /// about a\nconst b = 2;"))
	for _, n := range tree.Nodes {
		if n.Kind == ast.KindDocComment || n.Kind == ast.KindMetadata {
			t.Fatalf("a %s was made", n.Kind)
		}
	}
}

func TestParseExpressions(t *testing.T) {
	src := `fn f() {
    c"c";
    'x';
    b'y';
    true;
    none;
    Point.{ x = 1, y = -2 };
    .{ a = &mut v, b = @raw };
    Shape::Line with .{ end = Box<i32>::new(1) };
    [_]u8{ 1, 2 };
    []str{};
    free(p);
    println(items[1..=n]);
}
`
	tree, errs := Parse(source.NewFile("t.peep", src))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := `module
  function declaration
    declared name f
    block
      expression statement
        string literal c string c"c"
      expression statement
        character literal 'x'
      expression statement
        character literal byte character b'y'
      expression statement
        bool literal true
      expression statement
        none literal none
      expression statement
        struct literal
          type name Point
          field value
            field name x
            number literal 1
          field value
            field name y
            unary expression -
              number literal 2
      expression statement
        struct literal
          field value
            field name a
            reference expression mut
              name reference v
          field value
            field name b
            raw address expression
              name reference raw
      expression statement
        variant literal
          path
            type name Shape
            type name Line
          struct literal
            field value
              field name end
              call expression
                path
                  generic type
                    type name Box
                    type name i32
                  type name new
                number literal 1
      expression statement
        array literal
          fixed array type
            inferred length _
            type name u8
          number literal 1
          number literal 2
      expression statement
        array literal
          dynamic array type
            type name str
      expression statement
        free expression
          name reference p
      expression statement
        print expression println
          index expression
            name reference items
            range ..=
              number literal 1
              name reference n
`
	if got := dump(tree); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExpressionErrors(t *testing.T) {
	for _, test := range []struct{ src, message string }{
		{"a[1..=];", "inclusive range requires an end bound"},
		{"a is b;", "enum case must use a fully named path"},
		{"a |> b;", "pipe target must be a free-function call"},
		{"a |> b.c();", "pipe cannot call a method"},
		{".{ a: 1, b: 2 };", "expected `=` after struct literal field name"},
		{"[x]i32{};", "expected array length"},
		{"a.;", "expected `identifier`, found `;`"},
	} {
		_, errs := Parse(source.NewFile("t.peep", "fn f() { "+test.src+" }"))
		if len(errs) != 1 || errs[0].Message != test.message {
			t.Errorf("%q: errors %v, want %q", test.src, errs, test.message)
		}
	}
}

// TestComparisonIsNotTypeArguments checks that `<` begins type arguments only
// when `::` or `.{` follows the matching `>`.
func TestComparisonIsNotTypeArguments(t *testing.T) {
	for src, want := range map[string]ast.Kind{
		"a < b && c > d;":  ast.KindBinaryExpression,
		"Box<T>::new();":   ast.KindCallExpression,
		"Pair<A, B>.{};":   ast.KindStructLiteral,
		"a < b;":           ast.KindBinaryExpression,
		"f(a < b, c > d);": ast.KindCallExpression,
	} {
		tree, errs := Parse(source.NewFile("t.peep", "fn f() { "+src+" }"))
		if len(errs) != 0 {
			t.Errorf("%s: %v", src, errs)
			continue
		}
		block := tree.FunctionDecl(tree.List(tree.Module(tree.Root).Declarations)[0]).Body
		statement := tree.List(tree.Block(block).Statements)[0]
		if got := tree.Nodes[tree.ExpressionStatement(statement).Expression].Kind; got != want {
			t.Errorf("%s: %s, want %s", src, got, want)
		}
	}
}

func TestParseStatements(t *testing.T) {
	src := `fn f() {
    let mut n: i32 = 0;
    let .{ x, y as z, _ } = point;
    const limit = 10;
    n = n + 1;
    if n == 1 { } else if n == 2 { } else { }
    for { break; }
    for n < limit { continue; }
    for item in items { }
    for i, item in items[1..] { }
    match shape {
        Shape::Dot => { }
        Shape::Line with .{ end } => { }
        Shape::Maybe with none => { }
        Shape::Wrap with Inner::Leaf with value => { }
        _ => { }
    }
    { }
}
`
	tree, errs := Parse(source.NewFile("t.peep", src))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := `module
  function declaration
    declared name f
    block
      let statement mut
        declared name n
        type name i32
        number literal 0
      destructure statement
        struct pattern
          field pattern
            field name x
          field pattern
            field name y
            declared name z
          rest pattern _
        name reference point
      const declaration
        declared name limit
        number literal 10
      assign statement
        name reference n
        binary expression +
          name reference n
          number literal 1
      if statement
        binary expression ==
          name reference n
          number literal 1
        block { }
        if statement
          binary expression ==
            name reference n
            number literal 2
          block { }
          block { }
      loop statement
        block
          break statement break;
      loop statement
        binary expression <
          name reference n
          name reference limit
        block
          continue statement continue;
      for in statement
        declared name item
        name reference items
        block { }
      for in statement
        declared name i
        declared name item
        index expression
          name reference items
          range ..
            number literal 1
        block { }
      match statement
        name reference shape
        match arm
          case pattern
            path
              type name Shape
              type name Dot
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Line
            struct pattern
              field pattern
                field name end
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Maybe
            none pattern none
          block { }
        match arm
          case pattern
            path
              type name Shape
              type name Wrap
            case pattern
              path
                type name Inner
                type name Leaf
              declared name value
          block { }
        match arm
          wildcard pattern _
          block { }
      block { }
`
	if got := dump(tree); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestStatementErrors(t *testing.T) {
	for _, test := range []struct{ src, message, fixed string }{
		{"if a = b {}", "expected `==` in condition, found `=`", "if a == b {}"},
		{"if a", "missing if body", ""},
		{"if a {} else", "missing else body", ""},
		{"for x in items", "missing for body", ""},
		{"for a.b in x {}", "invalid loop variable", ""},
		{"break outer;", "labeled break is not supported yet", ""},
		{"match x { A::B {} }", "expected `=>` after match case", "match x { A::B => {} }"},
		{"match x { A::B => {}, _ => {} }", "match arms do not use commas", "match x { A::B => {} _ => {} }"},
		{"match x { a => {} }", "enum case must use a fully named path", ""},
		{"match x { A::B with => {} }", "expected a pattern after `with`", ""},
		{"let .{ a = b } = v;", "a destructure field is renamed with `as`", "let .{ a as b } = v;"},
		{"let .{ _, a } = v;", "`_` must be the last destructure item", ""},
	} {
		src := "fn f() { " + test.src + " }"
		_, errs := Parse(source.NewFile("t.peep", src))
		if len(errs) != 1 || errs[0].Message != test.message {
			t.Errorf("%q: errors %v, want %q", test.src, errs, test.message)
			continue
		}
		if test.fixed != "" {
			fix := errs[0].Notes[0].Fixes[0]
			if got := src[:fix.Span.Start] + fix.NewText + src[fix.Span.End:]; got != "fn f() { "+test.fixed+" }" {
				t.Errorf("%q: fix gives %q", test.src, got)
			}
		}
	}
}
