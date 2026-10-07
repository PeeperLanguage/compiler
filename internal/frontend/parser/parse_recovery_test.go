package parser

import (
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/source"
)

func TestParseMissingLiteralIntroducerRecovery(t *testing.T) {
	for _, statement := range []string{
		"let bad = Point{ x = 1 };",
		"let bad = Point{};",
		"let bad = Box<Box<i32>>{ value = .{ value = 1 } };",
		"let bad = pkg::Point{ x = 1 };",
		"let bad = Result<i32>::Ok{ value = 1 };",
		"let bad = Point{ x = .{ y = 1 }, z = accept(2, 3) }.x + 1;",
		"let bad = 1 + Point{ x = 2 }.x;",
		"let bad = -Point{ x = 2 }.x;",
		"return Result::Ok{ value = 1 };",
		"bad = Point{ x = 1 };",
		"accept(Point{ x = 1 }, 2);",
		"accept(Point{ x = 1, /// field documentation\n y = 2 }, 3);",
		"accept(Point{ x = 1, /// trailing documentation\n }, 2);",
		"accept({ x = 1 }.x, 2);",
		"println(Point{ x = 1 }.x);",
		"free(Point{ x = 1 });",
		"let bad = (Point{ x = 1 });",
		"let bad = items[Point{ x = 1 }.x];",
		"let bad = items[0..Point{ x = 1 }.x];",
		"let bad = [1]Point{Point{ x = 1 }};",
		"let bad = .{ point = Point{ x = 1 }, good = 2 };",
		"let bad = Result::Ok with { value = 1 };",
	} {
		t.Run(statement, func(t *testing.T) {
			src := "fn main() { " + statement + " let good = Point.{ x = 2 }; return 0; } fn next() {}"
			mod, diag := parseTestModule(src)
			if diag.ErrorCount() != 1 {
				t.Fatalf("errors = %d, want one literal diagnostic:\n%s", diag.ErrorCount(), diag.EmitAllToString())
			}
			if got := diag.EmitAllToString(); !strings.Contains(got, "literal") || !strings.Contains(got, ".{") {
				t.Fatalf("missing literal syntax explanation:\n%s", got)
			}
			if len(mod.Stmts) != 2 {
				t.Fatalf("module statements = %d, want main and next", len(mod.Stmts))
			}
			body := mod.Stmts[0].(*ast.FnDecl).Body
			if len(body.Stmts) != 3 {
				t.Fatalf("body statements = %d, want damaged statement, good declaration, return", len(body.Stmts))
			}
			good, ok := body.Stmts[1].(*ast.LetDecl)
			if !ok || good.Name.Name != "good" {
				t.Fatalf("following statement = %#v, want good declaration", body.Stmts[1])
			}
			if _, ok := good.Value.(*ast.StructLit); !ok {
				t.Fatalf("following initializer = %T, want struct literal", good.Value)
			}
			badCount := 0
			ast.Inspect(body.Stmts[0], func(node ast.Node) bool {
				if bad, ok := node.(*ast.BadExpr); ok {
					badCount++
					if bad.ID() == source.ParsedNodeID(0) || ast.EndOf(bad).Index <= ast.StartOf(bad).Index {
						t.Fatalf("bad expression lost identity/span: %#v", bad)
					}
				}
				return true
			})
			if badCount != 1 {
				t.Fatalf("bad expressions = %d, want one damaged literal", badCount)
			}
		})
	}
}

func TestParseMissingLiteralIntroducerInHeaders(t *testing.T) {
	for _, statement := range []string{
		"if value == Point{ x = 1 } { println(1); } else { println(2); }",
		"if value == Result::Ok{ value = 1 } { println(1); } else { println(2); }",
		"if accept(Point{ x = 1 }) { println(1); }",
		"for value != Point{ x = 1 } { break; }",
		"for value in Items{ x = 1 } { println(value); }",
		"for index, value in Items{ x = 1 } { println(value); }",
		"match Result::Ok{ value = 1 } { Result::Pending => {} }",
	} {
		t.Run(statement, func(t *testing.T) {
			mod, diag := parseTestModule("fn main() { " + statement + " let good = 2; } fn next() {}")
			if diag.ErrorCount() != 1 {
				t.Fatalf("errors = %d, want one literal diagnostic:\n%s", diag.ErrorCount(), diag.EmitAllToString())
			}
			if len(mod.Stmts) != 2 {
				t.Fatalf("module statements = %d, want main and next", len(mod.Stmts))
			}
			body := mod.Stmts[0].(*ast.FnDecl).Body
			if len(body.Stmts) != 2 {
				t.Fatalf("body statements = %d, want control statement and good declaration", len(body.Stmts))
			}
			switch node := body.Stmts[0].(type) {
			case *ast.IfStmt:
				if node.Then == nil || len(node.Then.Stmts) != 1 || strings.Contains(statement, "else") && node.Else == nil {
					t.Fatalf("lost if body/else: %#v", node)
				}
			case *ast.ForStmt:
				if node.Body == nil || len(node.Body.Stmts) != 1 {
					t.Fatalf("lost for body: %#v", node)
				}
			case *ast.MatchStmt:
				if len(node.Arms) != 1 {
					t.Fatalf("lost match arms: %#v", node)
				}
			default:
				t.Fatalf("header statement = %T", node)
			}
			if good, ok := body.Stmts[1].(*ast.LetDecl); !ok || good.Name.Name != "good" {
				t.Fatalf("lost following declaration: %#v", body.Stmts[1])
			}
		})
	}
}

func TestParseLiteralRecoveryPreservesAssignmentBodies(t *testing.T) {
	for _, statement := range []string{
		"if pkg::ready { x = 1; } { x = 2; }",
		"if pkg::ready {} { x = 2; }",
		"for pkg::ready { x = 1; } { x = 2; }",
		"for value in values::Zero..values::One { x = value; } { x = 2; }",
		"match pkg::value { Result::Pending => {} } { x = 2; }",
	} {
		t.Run(statement, func(t *testing.T) {
			mod, diag := parseTestModule("fn main() { " + statement + " }")
			if diag.HasErrors() {
				t.Fatalf("valid body misdiagnosed:\n%s", diag.EmitAllToString())
			}
			if body := mod.Stmts[0].(*ast.FnDecl).Body; len(body.Stmts) != 2 {
				t.Fatalf("body statements = %d, want control statement and standalone block", len(body.Stmts))
			}
		})
	}
}

func TestParseLiteralRecoveryKeepsIndependentErrors(t *testing.T) {
	mod, diag := parseTestModule("fn main() { let bad = Point{ x = 1 }; let another = ; let good = 2; } fn next() {}")
	if diag.ErrorCount() != 2 {
		t.Fatalf("errors = %d, want literal error and independent expression error:\n%s", diag.ErrorCount(), diag.EmitAllToString())
	}
	if len(mod.Stmts) != 2 || len(mod.Stmts[0].(*ast.FnDecl).Body.Stmts) != 3 {
		t.Fatalf("lost later statements/declaration: %#v", mod)
	}
	if !strings.Contains(diag.EmitAllToString(), "expected expression") {
		t.Fatal("independent error was suppressed")
	}
}

func TestParseUnclosedLiteralRecovery(t *testing.T) {
	mod, diag := parseTestModule("fn main() { let bad = Point{ x = 1; let good = 2; return 0; } fn next() {}")
	if diag.ErrorCount() != 1 {
		t.Fatalf("errors = %d, want one located literal diagnostic:\n%s", diag.ErrorCount(), diag.EmitAllToString())
	}
	if len(mod.Stmts) != 2 || len(mod.Stmts[0].(*ast.FnDecl).Body.Stmts) != 3 {
		t.Fatalf("unclosed literal swallowed function/following declaration: %#v", mod)
	}
	_, eofDiag := parseTestModule("fn main() { let bad = Point{ x = .{ y = 1 }")
	if !eofDiag.HasErrors() || eofDiag.ErrorCount() > 3 {
		t.Fatalf("unbounded EOF diagnostics:\n%s", eofDiag.EmitAllToString())
	}
	if first := diag.Diagnostics()[0]; first.Code != diagnostics.ErrInvalidExpression {
		t.Fatalf("first diagnostic = %s, want literal syntax error", first.Code)
	}
}

func TestParseUnclosedLiteralKeepsFollowingArgument(t *testing.T) {
	mod, diag := parseTestModule("fn main() { accept(Point{ x = 1, 2); let good = 3; } fn next() {}")
	if diag.ErrorCount() != 1 {
		t.Fatalf("errors = %d, want one literal diagnostic:\n%s", diag.ErrorCount(), diag.EmitAllToString())
	}
	if len(mod.Stmts) != 2 || len(mod.Stmts[0].(*ast.FnDecl).Body.Stmts) != 2 {
		t.Fatalf("lost following declaration: %#v", mod)
	}
	call := mod.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.ExprStmt).Expr.(*ast.CallExpr)
	if len(call.Args) != 2 || ast.ExprText(call.Args[1]) != "2" {
		t.Fatalf("following argument lost: %#v", call.Args)
	}
}

func TestParseLiteralRecoveryLocations(t *testing.T) {
	src := "fn main() { let bad = Point{ x = .{ y = 1 } }; let good = 2; }"
	mod, diag := parseTestModule(src)
	if diag.ErrorCount() != 1 {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
	bad := mod.Stmts[0].(*ast.FnDecl).Body.Stmts[0].(*ast.LetDecl).Value.(*ast.BadExpr)
	if ast.StartOf(bad).Index != strings.Index(src, "Point{") || ast.EndOf(bad).Index != strings.Index(src, ";") {
		t.Fatalf("bad expression span = %#v", ast.LocOf(bad))
	}
	diagnostic := diag.Diagnostics()[0]
	if diagnostic.Code != diagnostics.ErrInvalidExpression || len(diagnostic.Labels) != 1 ||
		diagnostic.Labels[0].Location.Start.Index != strings.Index(src, "{ x") {
		t.Fatalf("missing-introducer diagnostic location = %#v", diagnostic)
	}
}

func TestParseLiteralDiagnosticUsesParsedTypeArguments(t *testing.T) {
	for _, test := range []struct {
		statement string
		typeArgs  bool
	}{
		{"if Box<i32> {} { println(1); }", true},
		{"if Box<Box<i32>> {} { println(1); }", true},
		{"let bad = Point{ x = 1 }; let good = 2;", false},
		{"let bad = pkg::Point{ x = 1 }; let good = 2;", false},
	} {
		t.Run(test.statement, func(t *testing.T) {
			mod, diag := parseTestModule("fn main() { " + test.statement + " }")
			output := diag.EmitAllToString()
			if diag.ErrorCount() != 1 || strings.Contains(output, "after type arguments") != test.typeArgs {
				t.Fatalf("diagnostic must match parsed type arguments:\n%s", output)
			}
			if strings.Contains(output, "found generic type") || strings.Contains(output, "Type<...>") {
				t.Fatalf("diagnostic claims unsupported semantic knowledge:\n%s", output)
			}
			if len(mod.Stmts[0].(*ast.FnDecl).Body.Stmts) != 2 {
				t.Fatal("recovery swallowed body or following statement")
			}
		})
	}
}
