package parser

import (
	"fmt"
	"strings"
	"testing"

	"compiler/internal/diagnostics"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/source"
	"compiler/pkg/peeper"
)

func parseTestModule(src string) (*ast.Module, *diagnostics.DiagnosticBag) {
	const filePath = "test" + peeper.SourceExt
	diag := diagnostics.NewDiagnosticBag()
	stream := lexer.New(filePath, src, diag).Tokenize()
	return New(filePath, stream, diag).ParseModule(), diag
}

func TestParseModuleSubset(t *testing.T) {
	src := `import "math" as m;
const x: i32 = 1 + 2 * 3;
const y: i32 = x;
fn add(a: i32, b: i32) -> i32 {
	let z: i32 = a + b;
	return z;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics")
	}
	if len(mod.Imports) != 1 {
		t.Fatalf("imports: got %d want 1", len(mod.Imports))
	}
	if len(mod.Stmts) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Stmts))
	}
	if _, ok := mod.Stmts[0].(*ast.ConstDecl); !ok {
		t.Fatalf("decl[0] expected const")
	}
	if _, ok := mod.Stmts[1].(*ast.ConstDecl); !ok {
		t.Fatalf("decl[1] expected const")
	}
	fn, ok := mod.Stmts[2].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[2] expected fn")
	}
	if fn.Name == nil || fn.Name.Name != "add" {
		t.Fatalf("fn name mismatch")
	}
	if len(fn.Params) != 2 {
		t.Fatalf("params: got %d want 2", len(fn.Params))
	}
	if fn.ReturnType == nil {
		t.Fatalf("missing return type")
	}
	if fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("fn body stmts mismatch")
	}
}

func TestParseAllowsBuiltinNumericTypes(t *testing.T) {
	src := `const x: i64 = 1;
const y: f32 = 2;
fn sum(a: i64, b: f32) -> f64 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected parser diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Stmts))
	}
}

func TestParseRejectsTopLevelLet(t *testing.T) {
	src := `let x: i32 = 1;
fn main() -> i32 { return 0; }`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for top-level let")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected only later decl to remain, got %d stmts", len(mod.Stmts))
	}
	if _, ok := mod.Stmts[0].(*ast.FnDecl); !ok {
		t.Fatalf("expected fn decl after rejected let, got %T", mod.Stmts[0])
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "top-level `let` not allowed") {
		t.Fatalf("expected top-level let diagnostic, got:\n%s", out)
	}
}

func TestParseRejectsTopLevelExpressionStmt(t *testing.T) {
	src := `foo();
fn main() -> i32 { return 0; }`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for top-level expr stmt")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected only later decl to remain, got %d stmts", len(mod.Stmts))
	}
	if _, ok := mod.Stmts[0].(*ast.FnDecl); !ok {
		t.Fatalf("expected fn decl after rejected expr stmt, got %T", mod.Stmts[0])
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "module scope expects declaration") {
		t.Fatalf("expected module-scope declaration diagnostic, got:\n%s", out)
	}
}

func TestParseConstWithoutExplicitType(t *testing.T) {
	src := `const c = a + b;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	constDecl, ok := mod.Stmts[0].(*ast.ConstDecl)
	if !ok {
		t.Fatalf("decl[0] expected const")
	}
	if constDecl.Type != nil {
		t.Fatalf("const type should be nil when omitted")
	}
}

func TestParseFunctionWithTypeParams(t *testing.T) {
	src := `fn add<T, U>(a: i32) -> i32 {
	return a;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if len(fn.TypeParams) != 2 {
		t.Fatalf("type params: got %d want 2", len(fn.TypeParams))
	}
	if fn.TypeParams[0].Name == nil || fn.TypeParams[0].Name.Name != "T" {
		t.Fatalf("first type param mismatch")
	}
	if fn.TypeParams[1].Name == nil || fn.TypeParams[1].Name.Name != "U" {
		t.Fatalf("second type param mismatch")
	}
}

func TestParseMoveParam(t *testing.T) {
	src := `fn destroy(move data: Buffer) {}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if len(fn.Params) != 1 {
		t.Fatalf("params: got %d want 1", len(fn.Params))
	}
	if !fn.Params[0].Consumes {
		t.Fatalf("expected move param")
	}
	if got := fn.GetDeclSurface(); !strings.Contains(got, "move data:Buffer") {
		t.Fatalf("surface missing move marker: %q", got)
	}
}

func TestParseRejectsBracketTypeParams(t *testing.T) {
	src := `fn add[T](a: i32) -> i32 {
	return a;
}`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected parser diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "expected '<' to start type parameter list") {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestParseModuleComputesStableSurfaceFingerprints(t *testing.T) {
	first, firstDiag := parseTestModule(`import "util";
const value = helper();
fn helper() -> i32 { return 1; }`)
	if firstDiag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", firstDiag.EmitAllToString())
	}
	second, secondDiag := parseTestModule(`import "util";
const value = helper();
fn helper() -> i32 { let x = 1; return x; }`)
	if secondDiag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", secondDiag.EmitAllToString())
	}
	if first.ImportFingerprint != ast.FingerprintParts([]string{"util"}) {
		t.Fatalf("import fingerprint mismatch: %q", first.ImportFingerprint)
	}
	if first.ExportFingerprint == "" {
		t.Fatalf("missing export fingerprint")
	}
	if first.ExportFingerprint != second.ExportFingerprint {
		t.Fatalf("body-only function change should not alter export fingerprint")
	}
	decl, ok := first.Stmts[1].(*ast.FnDecl)
	if !ok {
		t.Fatalf("expected fn decl, got %T", first.Stmts[1])
	}
	if decl.GetDeclSurface() == "" {
		t.Fatalf("missing decl surface")
	}
}

func TestParseFunctionReturnArrowSyntax(t *testing.T) {
	src := `fn main() -> i32 {
	return 10;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	ret, ok := fn.ReturnType.(*ast.NamedType)
	if !ok || ret.Name != "i32" {
		t.Fatalf("return type mismatch: %#v", fn.ReturnType)
	}
}

func TestParseUnaryPlus(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = +(10 as i32);
	return x;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected let stmt, got %#v", fn.Body.Stmts[0])
	}
	unary, ok := letDecl.Value.(*ast.UnaryExpr)
	if !ok || unary.Op != "+" {
		t.Fatalf("expected unary plus, got %#v", letDecl.Value)
	}
}

func TestParseMoveExpr(t *testing.T) {
	src := `fn main() {
	let next = move current;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected let stmt, got %#v", fn.Body.Stmts[0])
	}
	moveExpr, ok := letDecl.Value.(*ast.MoveExpr)
	if !ok {
		t.Fatalf("expected move expr, got %#v", letDecl.Value)
	}
	if ident, ok := moveExpr.Expr.(*ast.Ident); !ok || ident.Name != "current" {
		t.Fatalf("unexpected move operand: %#v", moveExpr.Expr)
	}
}

func TestParseAddressExpr(t *testing.T) {
	src := `fn main() {
	let ptr = @current;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected let stmt, got %#v", fn.Body.Stmts[0])
	}
	addr, ok := letDecl.Value.(*ast.AddressExpr)
	if !ok {
		t.Fatalf("expected address expr, got %#v", letDecl.Value)
	}
	if ident, ok := addr.Expr.(*ast.Ident); !ok || ident.Name != "current" {
		t.Fatalf("unexpected address operand: %#v", addr.Expr)
	}
}

func TestParseCastBindsLooserThanUnary(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i8 = -128 as i8;
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected let stmt, got %#v", fn.Body.Stmts[0])
	}
	cast, ok := letDecl.Value.(*ast.AsExpr)
	if !ok {
		t.Fatalf("expected cast expression, got %#v", letDecl.Value)
	}
	if _, ok := cast.Expr.(*ast.UnaryExpr); !ok {
		t.Fatalf("expected unary expression inside cast, got %#v", cast.Expr)
	}
}

func TestParseMalformedLocalLetDoesNotAppendTypedNilStmt(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = +;
	return 0;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil {
		t.Fatalf("unexpected function decl: %#v", mod.Stmts[0])
	}
	if len(fn.Body.Stmts) != 2 {
		t.Fatalf("stmts: got %d want 2", len(fn.Body.Stmts))
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok || letDecl == nil {
		t.Fatalf("expected non-nil let stmt, got %#v", fn.Body.Stmts[0])
	}
	if letDecl.Value == nil {
		t.Fatalf("expected partial initializer (not nil), got nil")
	}
	// Value should be a UnaryExpr containing a BadExpr (preserves partial tree)
	if unary, ok := letDecl.Value.(*ast.UnaryExpr); !ok {
		t.Fatalf("expected UnaryExpr, got %T", letDecl.Value)
	} else if _, ok := unary.Expr.(*ast.BadExpr); !ok {
		t.Fatalf("expected inner BadExpr, got %T", unary.Expr)
	}
	if _, ok := fn.Body.Stmts[1].(*ast.ReturnStmt); !ok {
		t.Fatalf("expected return stmt after recovery, got %#v", fn.Body.Stmts[1])
	}
}

func TestParseRecoversMissingSemicolonAndContinuesTopLevel(t *testing.T) {
	src := `const a: i32 = 10
const b: i32 = 23;
fn main() -> i32 {
	return b;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing semicolon")
	}
	if len(mod.Stmts) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Stmts))
	}
	if _, ok := mod.Stmts[2].(*ast.FnDecl); !ok {
		t.Fatalf("decl[2] expected fn")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "expected ';' after statement") {
		t.Fatalf("expected targeted missing-semicolon diagnostic, got:\n%s", out)
	}
}

func TestParseRecoversMissingParenInCallAndContinues(t *testing.T) {
	src := `fn main() -> i32 {
	foo(1, 2;
	return 0;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing ')'")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Body == nil || len(fn.Body.Stmts) == 0 {
		t.Fatalf("expected function body statements after recovery")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "expected ')'") {
		t.Fatalf("expected missing-paren diagnostic, got:\n%s", out)
	}
}

func TestParseRecoversMissingFunctionBlockAndContinues(t *testing.T) {
	src := `fn myfunc()
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing function block")
	}
	if len(mod.Stmts) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Stmts))
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "missing function body") {
		t.Fatalf("expected missing-block diagnostic, got:\n%s", out)
	}
}

func TestParseMissingFunctionBlockDoesNotBreakNextFunction(t *testing.T) {
	src := `

fn myfunc()

fn main() {
	let a : i32 = 10;
	let b : i32 = 23;
	let c = a + b;
}`
	mod, diag := parseTestModule(src)
	if len(mod.Stmts) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Stmts))
	}
	out := diag.EmitAllToString()
	if strings.Contains(out, "test"+peeper.SourceExt+":5:1") && strings.Contains(out, "expected '{'") {
		t.Fatalf("unexpected extra missing-block diagnostic on second function:\n%s", out)
	}
}

func TestParseAllowsExternalFunctionSemicolon(t *testing.T) {
	src := `fn ext();
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Body != nil {
		t.Fatalf("external fn body must be nil")
	}
}

func TestParseAllowsFunctionAttributes(t *testing.T) {
	src := `#[extern("malloc")]
#[target_os("linux")]
#[max_calls(3)]
fn ext();
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	attrs := fn.GetAttributes()
	externAttr, hasExtern := fn.GetAttribute("extern")
	targetAttr, hasTargetOS := fn.GetAttribute("target_os")
	maxCallsAttr, hasMaxCalls := fn.GetAttribute("max_calls")
	externName, externOK := externAttr.Args[0].(*ast.StringLit)
	targetOS, targetOK := targetAttr.Args[0].(*ast.StringLit)
	maxCalls, maxCallsOK := maxCallsAttr.Args[0].(*ast.NumberLit)
	if len(attrs) != 3 || !hasExtern || !externOK || externName.Value != "malloc" || !hasTargetOS || !targetOK || targetOS.Value != "linux" || !hasMaxCalls || !maxCallsOK || maxCalls.Value != "3" {
		t.Fatalf("attrs mismatch: %#v", attrs)
	}
}

func TestParseRejectsMultipleAttributesInOneBlock(t *testing.T) {
	src := `#[extern("malloc"), no_mangle]
fn ext();
`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected parser diagnostics")
	}
	if !strings.Contains(diag.EmitAllToString(), "only one attribute is allowed per `#[...]` block") {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestParseMethodStyleFunctionNamePath(t *testing.T) {
	src := `fn User::Method(self: i32, other: i32) -> i32 {
	return other;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn")
	}
	if fn.Name == nil || fn.Name.Name != "User::Method" {
		t.Fatalf("method name mismatch: got %#v", fn.Name)
	}
}

func TestParseAnonymousTypesInBindings(t *testing.T) {
	src := `const a: struct {
	x: i32,
} = value;
const b: interface {
	call(x: i32) -> i32,
} = value;
const c: enum {
	One,
	Two,
} = value;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Stmts))
	}
	l0 := mod.Stmts[0].(*ast.ConstDecl)
	if _, ok := l0.Type.(*ast.StructType); !ok {
		t.Fatalf("const a type expected struct")
	}
	l1 := mod.Stmts[1].(*ast.ConstDecl)
	if _, ok := l1.Type.(*ast.InterfaceType); !ok {
		t.Fatalf("const b type expected interface")
	}
	l2 := mod.Stmts[2].(*ast.ConstDecl)
	if _, ok := l2.Type.(*ast.EnumType); !ok {
		t.Fatalf("const c type expected enum")
	}
}

func TestParseOptionalPointerArrayAndSliceTypes(t *testing.T) {
	src := `const a: ?i32 = none;
const b: ^const Foo = value;
const c: [4]i32 = value;
const d: []string = value;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 4 {
		t.Fatalf("decls: got %d want 4", len(mod.Stmts))
	}
	optDecl := mod.Stmts[0].(*ast.ConstDecl)
	if _, ok := optDecl.Type.(*ast.OptionalType); !ok {
		t.Fatalf("expected optional type, got %T", optDecl.Type)
	}
	ptrDecl := mod.Stmts[1].(*ast.ConstDecl)
	ptr, ok := ptrDecl.Type.(*ast.RawPtrType)
	if !ok {
		t.Fatalf("expected raw ptr type, got %T", ptrDecl.Type)
	}
	if ptr.Mutable {
		t.Fatalf("expected const pointer type")
	}
	arrDecl := mod.Stmts[2].(*ast.ConstDecl)
	arr, ok := arrDecl.Type.(*ast.ArrayType)
	if !ok {
		t.Fatalf("expected array type, got %T", arrDecl.Type)
	}
	if arr.Len == nil || arr.Len.Value != "4" {
		t.Fatalf("array len mismatch: %#v", arr.Len)
	}
	sliceDecl := mod.Stmts[3].(*ast.ConstDecl)
	if _, ok := sliceDecl.Type.(*ast.SliceType); !ok {
		t.Fatalf("expected slice type, got %T", sliceDecl.Type)
	}
}

func TestParseTypeDeclAttributes(t *testing.T) {
	src := `#[no_copy]
struct Buffer<T> {
	ptr: ^u8,
}

#[allow_copy]
type Cursor = ^u8;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 2 {
		t.Fatalf("decls: got %d want 2", len(mod.Stmts))
	}
	strct, ok := mod.Stmts[0].(*ast.StructDecl)
	if !ok {
		t.Fatalf("decl[0] expected struct")
	}
	if len(strct.Attributes) != 1 || strct.Attributes[0].Name != "no_copy" {
		t.Fatalf("struct attrs mismatch: %#v", strct.Attributes)
	}
	if len(strct.TypeParams) != 1 || strct.TypeParams[0].Name == nil || strct.TypeParams[0].Name.Name != "T" {
		t.Fatalf("struct type params mismatch: %#v", strct.TypeParams)
	}
	alias, ok := mod.Stmts[1].(*ast.TypeAliasDecl)
	if !ok {
		t.Fatalf("decl[1] expected type alias")
	}
	if len(alias.Attributes) != 1 || alias.Attributes[0].Name != "allow_copy" {
		t.Fatalf("alias attrs mismatch: %#v", alias.Attributes)
	}
}

func TestParseNestedBlockStmt(t *testing.T) {
	src := `fn main() -> i32 {
{
let a = 1;
return a;
}
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	if _, ok := fn.Body.Stmts[0].(*ast.BlockStmt); !ok {
		t.Fatalf("expected nested block stmt, got %#v", fn.Body.Stmts[0])
	}
}

func TestParseIfElseStmt(t *testing.T) {
	src := `fn main() -> i32 {
if 1 < 2 {
	return 10;
} else {
	return 20;
}
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	ifs, ok := fn.Body.Stmts[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected if stmt, got %#v", fn.Body.Stmts[0])
	}
	if ifs.Then == nil || len(ifs.Then.Stmts) != 1 {
		t.Fatalf("missing then block")
	}
	elseBlock, ok := ifs.Else.(*ast.BlockStmt)
	if !ok || len(elseBlock.Stmts) != 1 {
		t.Fatalf("expected else block, got %#v", ifs.Else)
	}
}

func TestParseElseIfStmt(t *testing.T) {
	src := `fn main() -> i32 {
if 1 {
	return 1;
} else if 2 {
	return 2;
} else {
	return 3;
}
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	root, ok := fn.Body.Stmts[0].(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected if stmt, got %#v", fn.Body.Stmts[0])
	}
	next, ok := root.Else.(*ast.IfStmt)
	if !ok {
		t.Fatalf("expected else-if stmt, got %#v", root.Else)
	}
	if _, ok := next.Else.(*ast.BlockStmt); !ok {
		t.Fatalf("expected final else block, got %#v", next.Else)
	}
}

func TestParseForConditionStmt(t *testing.T) {
	src := `fn main() -> i32 {
for 1 < 2 {
	return 1;
}
return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	loop, ok := fn.Body.Stmts[0].(*ast.ForStmt)
	if !ok {
		t.Fatalf("expected for stmt, got %#v", fn.Body.Stmts[0])
	}
	if loop.Cond == nil {
		t.Fatalf("expected loop condition")
	}
	if loop.Body == nil || len(loop.Body.Stmts) != 1 {
		t.Fatalf("expected loop body, got %#v", loop.Body)
	}
}

func TestParseInfiniteForStmt(t *testing.T) {
	src := `fn main() -> i32 {
for {
	return 1;
}
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() || mod == nil {
		t.Fatalf("parse failed: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	loop, ok := fn.Body.Stmts[0].(*ast.ForStmt)
	if !ok {
		t.Fatalf("expected for stmt, got %#v", fn.Body.Stmts[0])
	}
	if loop.Cond != nil {
		t.Fatalf("expected nil condition for infinite loop, got %#v", loop.Cond)
	}
}

func TestParseTypeDeclarations(t *testing.T) {
	src := `struct Vec2 {
	x: f32,
	y: f32,
}
interface Adder {
	add(i32, i32) -> i32,
}
enum Color {
	Red,
	Green,
	Blue,
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Stmts))
	}
	a0, ok := mod.Stmts[0].(*ast.StructDecl)
	if !ok {
		t.Fatalf("decl[0] expected struct decl")
	}
	a0Type, ok := a0.Type.(*ast.StructType)
	if !ok {
		t.Fatalf("decl[0] expected struct payload, got %T", a0.Type)
	}
	if len(a0Type.Fields) != 2 {
		t.Fatalf("struct fields: got %d want 2", len(a0Type.Fields))
	}
	a1, ok := mod.Stmts[1].(*ast.InterfaceDecl)
	if !ok {
		t.Fatalf("decl[1] expected interface decl")
	}
	a1Type, ok := a1.Type.(*ast.InterfaceType)
	if !ok {
		t.Fatalf("decl[1] expected interface payload, got %T", a1.Type)
	}
	if len(a1Type.Methods) != 1 {
		t.Fatalf("interface methods: got %d want 1", len(a1Type.Methods))
	}
	if got := len(a1Type.Methods[0].Params); got != 2 {
		t.Fatalf("interface params: got %d want 2", got)
	}
	if a1Type.Methods[0].Params[0].Name != nil {
		t.Fatalf("first interface param should be unnamed")
	}
	a2, ok := mod.Stmts[2].(*ast.EnumDecl)
	if !ok {
		t.Fatalf("decl[2] expected enum decl")
	}
	a2Type, ok := a2.Type.(*ast.EnumType)
	if !ok {
		t.Fatalf("decl[2] expected enum payload, got %T", a2.Type)
	}
	if len(a2Type.Variants) != 3 {
		t.Fatalf("enum variants: got %d want 3", len(a2Type.Variants))
	}
}

func TestParseImplDecl(t *testing.T) {
	src := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	implDecl, ok := mod.Stmts[0].(*ast.ImplDecl)
	if !ok {
		t.Fatalf("decl[0] expected impl decl")
	}
	target, ok := implDecl.Target.(*ast.NamedType)
	if !ok || target.Name != "i32" {
		t.Fatalf("impl target mismatch: %#v", implDecl.Target)
	}
	if len(implDecl.Methods) != 1 {
		t.Fatalf("methods: got %d want 1", len(implDecl.Methods))
	}
	method := implDecl.Methods[0]
	if method.Name == nil || method.Name.Name != "abs" {
		t.Fatalf("method name mismatch: %#v", method.Name)
	}
	if len(method.Params) != 1 || method.Params[0].Name == nil || method.Params[0].Name.Name != "self" {
		t.Fatalf("self parameter not parsed")
	}
}

func TestParseSelectorAndMethodCall(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = 1;
	x.abs();
	return x.value;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 3 {
		t.Fatalf("unexpected function body")
	}
	exprStmt, ok := fn.Body.Stmts[1].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("stmt[1] expected expr stmt")
	}
	call, ok := exprStmt.Expr.(*ast.CallExpr)
	if !ok {
		t.Fatalf("stmt[1] expected call expr")
	}
	selector, ok := call.Callee.(*ast.SelectorExpr)
	if !ok || selector.Name == nil || selector.Name.Name != "abs" {
		t.Fatalf("expected selector callee, got %#v", call.Callee)
	}
	ret, ok := fn.Body.Stmts[2].(*ast.ReturnStmt)
	if !ok {
		t.Fatalf("stmt[2] expected return")
	}
	field, ok := ret.Value.(*ast.SelectorExpr)
	if !ok || field.Name == nil || field.Name.Name != "value" {
		t.Fatalf("expected selector return expr, got %#v", ret.Value)
	}
}

func TestParseIndexExpr(t *testing.T) {
	src := `fn main() {
	let x = xs[0];
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("stmt[0] expected let")
	}
	index, ok := letDecl.Value.(*ast.IndexExpr)
	if !ok {
		t.Fatalf("expected index expr, got %#v", letDecl.Value)
	}
	if base, ok := index.Expr.(*ast.Ident); !ok || base.Name != "xs" {
		t.Fatalf("unexpected index base: %#v", index.Expr)
	}
	if idx, ok := index.Index.(*ast.NumberLit); !ok || idx.Value != "0" {
		t.Fatalf("unexpected index value: %#v", index.Index)
	}
}

func TestParseSelectorOverIndexExpr(t *testing.T) {
	src := `fn main() -> i32 {
	return xs[i].value;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	ret, ok := fn.Body.Stmts[0].(*ast.ReturnStmt)
	if !ok {
		t.Fatalf("stmt[0] expected return")
	}
	selector, ok := ret.Value.(*ast.SelectorExpr)
	if !ok || selector.Name == nil || selector.Name.Name != "value" {
		t.Fatalf("expected selector over index, got %#v", ret.Value)
	}
	index, ok := selector.Expr.(*ast.IndexExpr)
	if !ok {
		t.Fatalf("expected selector base index expr, got %#v", selector.Expr)
	}
	if base, ok := index.Expr.(*ast.Ident); !ok || base.Name != "xs" {
		t.Fatalf("unexpected index base: %#v", index.Expr)
	}
	if idx, ok := index.Index.(*ast.Ident); !ok || idx.Name != "i" {
		t.Fatalf("unexpected index value: %#v", index.Index)
	}
}

func TestParseIndexExprInsideCallArg(t *testing.T) {
	src := `fn main() {
	foo(xs[0]);
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts[0])
	}
	exprStmt, ok := fn.Body.Stmts[0].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("stmt[0] expected expr stmt")
	}
	call, ok := exprStmt.Expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		t.Fatalf("expected single-arg call, got %#v", exprStmt.Expr)
	}
	if _, ok := call.Args[0].(*ast.IndexExpr); !ok {
		t.Fatalf("expected index call arg, got %#v", call.Args[0])
	}
}

func TestParseMalformedIndexExprReportsDiagnostics(t *testing.T) {
	src := `fn main() {
	let x = xs[;
}`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics for malformed index expression")
	}
}

func TestParseStructLiteral(t *testing.T) {
	src := `fn main() -> i32 {
	let p = .{ x = 1, y = 2, };
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body")
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("stmt[0] expected let")
	}
	lit, ok := letDecl.Value.(*ast.StructLit)
	if !ok {
		t.Fatalf("stmt[0] expected struct literal, got %#v", letDecl.Value)
	}
	if len(lit.Fields) != 2 {
		t.Fatalf("literal fields: got %d want 2", len(lit.Fields))
	}
	if lit.Fields[0].Name == nil || lit.Fields[0].Name.Name != "x" {
		t.Fatalf("first literal field mismatch")
	}
	if lit.Fields[1].Name == nil || lit.Fields[1].Name.Name != "y" {
		t.Fatalf("second literal field mismatch")
	}
}

func TestParseTypedStructLiteral(t *testing.T) {
	src := `fn main() -> i32 {
	let p = .Point{ x = 1, y = 2, };
	return p.x;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body")
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("stmt[0] expected let")
	}
	lit, ok := letDecl.Value.(*ast.StructLit)
	if !ok {
		t.Fatalf("stmt[0] expected struct literal, got %#v", letDecl.Value)
	}
	named, ok := lit.Type.(*ast.NamedType)
	if !ok || named.Name != "Point" {
		t.Fatalf("literal type mismatch: %#v", lit.Type)
	}
	if len(lit.Fields) != 2 {
		t.Fatalf("literal fields: got %d want 2", len(lit.Fields))
	}
}

func TestParseAttachesDocCommentsInFunctionBody(t *testing.T) {
	src := `fn main() -> i32 {
	/// docs-like comment before local let
	let x: i32 = 1;
	return x;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts)
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok || letDecl.Doc == nil || letDecl.Doc.Text != "docs-like comment before local let" {
		t.Fatalf("statement doc mismatch: %#v", fn.Body.Stmts[0])
	}
}

func TestParseAttachesDocCommentsToIfStmt(t *testing.T) {
	src := `fn main() -> i32 {
	/// branch docs
	if true {
		return 0;
	}
	return 1;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 2 {
		t.Fatalf("unexpected function body: %#v", mod.Stmts)
	}
	ifStmt, ok := fn.Body.Stmts[0].(*ast.IfStmt)
	if !ok || ifStmt.Doc == nil || ifStmt.Doc.Text != "branch docs" {
		t.Fatalf("if doc mismatch: %#v", fn.Body.Stmts[0])
	}
}

func TestParseAttachesDocCommentsToDecl(t *testing.T) {
	src := `/// fn docs
/// more fn docs
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Doc == nil || fn.Doc.Text != "fn docs\nmore fn docs" {
		t.Fatalf("decl doc mismatch: %#v", mod.Stmts[0])
	}
}

func TestParseAllowsCommentAfterAttributeBeforeDecl(t *testing.T) {
	src := `#[no_copy]
/// buffer docs
struct Buffer {
	value: i32
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	decl, ok := mod.Stmts[0].(*ast.StructDecl)
	if !ok {
		t.Fatalf("decl[0] expected struct, got %T", mod.Stmts[0])
	}
	if decl.Doc == nil || decl.Doc.Text != "buffer docs" {
		t.Fatalf("struct doc mismatch: %#v", decl.Doc)
	}
	if _, ok := decl.GetAttribute(ast.AttributeNoCopy); !ok {
		t.Fatalf("expected no_copy attribute on struct")
	}
}

func TestParseAllowsCommentBeforeAttributeBeforeDecl(t *testing.T) {
	src := `/// fn docs
#[target_os("linux")]
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn, got %T", mod.Stmts[0])
	}
	if fn.Doc == nil || fn.Doc.Text != "fn docs" {
		t.Fatalf("fn doc mismatch: %#v", fn.Doc)
	}
	if _, ok := fn.GetAttribute(ast.AttributeTargetOS); !ok {
		t.Fatalf("expected target_os attribute on fn")
	}
}

func TestParseAttachesDocCommentsAcrossGapsBeforeDecl(t *testing.T) {
	src := `/// first docs

/// second docs
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn, got %T", mod.Stmts[0])
	}
	if fn.Doc == nil || fn.Doc.Text != "first docs\nsecond docs" {
		t.Fatalf("fn doc mismatch: %#v", fn.Doc)
	}
}

func TestParseAttachesDocCommentsAcrossSkippedCommentBeforeDecl(t *testing.T) {
	src := `/// fn docs
// note
fn main() -> i32 {
	return 0;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("decl[0] expected fn, got %T", mod.Stmts[0])
	}
	if fn.Doc == nil || fn.Doc.Text != "fn docs" {
		t.Fatalf("fn doc mismatch: %#v", fn.Doc)
	}
}

func TestParseAllowsCommentAfterAttributeBeforeImplMethod(t *testing.T) {
	src := `struct Buffer {
	value: i32
}

impl Buffer {
	#[test]
	/// method docs
	fn destroy(self: Self) {}
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	decl, ok := mod.Stmts[1].(*ast.ImplDecl)
	if !ok || len(decl.Methods) != 1 {
		t.Fatalf("impl methods mismatch: %#v", mod.Stmts[1])
	}
	method := decl.Methods[0]
	if method.Doc == nil || method.Doc.Text != "method docs" {
		t.Fatalf("method doc mismatch: %#v", method.Doc)
	}
	if _, ok := method.GetAttribute(ast.AttributeTest); !ok {
		t.Fatalf("expected test attribute on method")
	}
}

func TestParseAllowsTrailingCommentBeforeImplClose(t *testing.T) {
	src := `struct Buffer {
	value: i32
}

impl Buffer {
	fn destroy(self: Self) {}
	// trailer
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	decl, ok := mod.Stmts[1].(*ast.ImplDecl)
	if !ok || len(decl.Methods) != 1 {
		t.Fatalf("impl methods mismatch: %#v", mod.Stmts[1])
	}
}

func TestParseSkipsNormalCommentInsideParams(t *testing.T) {
	src := `fn f(
	// note
	x: i32
) {}
`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || len(fn.Params) != 1 || fn.Params[0].Name == nil || fn.Params[0].Name.Name != "x" {
		t.Fatalf("param parse mismatch: %#v", mod.Stmts[0])
	}
}

func TestParseSkipsNormalCommentInsideTypeParams(t *testing.T) {
	src := `fn pair<T,
	// note
	U>() {}
`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || len(fn.TypeParams) != 2 {
		t.Fatalf("type params mismatch: %#v", mod.Stmts[0])
	}
}

func TestParseMalformedAttributeDoesNotPanic(t *testing.T) {
	src := `#[ ]
fn main() {}
`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics for malformed attribute")
	}
}

func TestParsePointerTypes(t *testing.T) {
	src := `const ptr: ^i32;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("decls: got %d want 1", len(mod.Stmts))
	}
	ptrDecl, ok := mod.Stmts[0].(*ast.ConstDecl)
	if !ok {
		t.Fatalf("decl[0] expected const")
	}
	ptr, ok := ptrDecl.Type.(*ast.RawPtrType)
	if !ok || !ptr.Mutable {
		t.Fatalf("decl[0] expected pointer type, got %#v", ptrDecl.Type)
	}
}

// TestParseFnDefaultReturnType verifies that a function with no declared
// return type has no return value.
func TestParseFnDefaultReturnType(t *testing.T) {
	src := `fn noReturn() { }`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok {
		t.Fatalf("expected fn decl, got %T", mod.Stmts[0])
	}
	if fn.ReturnType != nil {
		t.Fatalf("expected nil default return type, got %T", fn.ReturnType)
	}
}

// TestParseFnExplicitReturnTypeOverridesDefault verifies that an explicit
// return type wins over the default.
func TestParseFnExplicitReturnTypeOverridesDefault(t *testing.T) {
	src := `fn returnsFloat() -> f64 { }`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	named := fn.ReturnType.(*ast.NamedType)
	if named.Name != "f64" {
		t.Fatalf("explicit return type: got %q want %q", named.Name, "f64")
	}
}

// TestParseInterfaceMethodDefaultReturnType verifies interface methods also
// default to no return value.
func TestParseInterfaceMethodDefaultReturnType(t *testing.T) {
	src := `interface I { method() }`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	iface := mod.Stmts[0].(*ast.InterfaceDecl)
	ifaceType, ok := iface.Type.(*ast.InterfaceType)
	if !ok {
		t.Fatalf("expected interface payload, got %T", iface.Type)
	}
	if len(ifaceType.Methods) != 1 {
		t.Fatalf("methods: got %d want 1", len(ifaceType.Methods))
	}
	if ifaceType.Methods[0].ReturnType != nil {
		t.Fatalf("expected nil interface method return type, got %T", ifaceType.Methods[0].ReturnType)
	}
}

// TestParseFuncTypeDefaultReturnType verifies fn-types default to no return value.
func TestParseFuncTypeDefaultReturnType(t *testing.T) {
	src := `const cb: fn(i32) = 0;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	constDecl := mod.Stmts[0].(*ast.ConstDecl)
	ft, ok := constDecl.Type.(*ast.FuncType)
	if !ok {
		t.Fatalf("expected func type, got %T", constDecl.Type)
	}
	if ft.Return != nil {
		t.Fatalf("expected nil fn-type return, got %T", ft.Return)
	}
}

func TestParseFuncTypeMoveParam(t *testing.T) {
	src := `const cb: fn(move x: Buffer) = 0;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	constDecl := mod.Stmts[0].(*ast.ConstDecl)
	ft, ok := constDecl.Type.(*ast.FuncType)
	if !ok {
		t.Fatalf("expected func type, got %T", constDecl.Type)
	}
	if len(ft.Params) != 1 {
		t.Fatalf("params: got %d want 1", len(ft.Params))
	}
	if len(ft.Consumes) != 1 || !ft.Consumes[0] {
		t.Fatalf("expected consuming first param, got %#v", ft.Consumes)
	}
	if named, ok := ft.Params[0].(*ast.NamedType); !ok || named.Name != "Buffer" {
		t.Fatalf("param type: got %T %#v want Buffer", ft.Params[0], ft.Params[0])
	}
}

// TestParseStructFieldsTrailingComma verifies the new parseBracedItemList
// helper accepts a trailing comma (a feature of the extracted helper).
func TestParseStructFieldsTrailingComma(t *testing.T) {
	src := `struct S { a: i32, b: i32, }`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	st := mod.Stmts[0].(*ast.StructDecl)
	stType, ok := st.Type.(*ast.StructType)
	if !ok {
		t.Fatalf("expected struct payload, got %T", st.Type)
	}
	if len(stType.Fields) != 2 {
		t.Fatalf("fields: got %d want 2", len(stType.Fields))
	}
}

// TestParseEnumVariantsTrailingComma verifies the same for enum variants.
func TestParseEnumVariantsTrailingComma(t *testing.T) {
	src := `enum E { A, B, }`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	en := mod.Stmts[0].(*ast.EnumDecl)
	enType, ok := en.Type.(*ast.EnumType)
	if !ok {
		t.Fatalf("expected enum payload, got %T", en.Type)
	}
	if len(enType.Variants) != 2 {
		t.Fatalf("variants: got %d want 2", len(enType.Variants))
	}
}

// TestParseInterfaceMethodsTrailingComma verifies the same for interface methods.
func TestParseInterfaceMethodsTrailingComma(t *testing.T) {
	src := `interface I { foo(), bar(), }`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	iface := mod.Stmts[0].(*ast.InterfaceDecl)
	ifaceType, ok := iface.Type.(*ast.InterfaceType)
	if !ok {
		t.Fatalf("expected interface payload, got %T", iface.Type)
	}
	if len(ifaceType.Methods) != 2 {
		t.Fatalf("methods: got %d want 2", len(ifaceType.Methods))
	}
}

func TestParseInterfaceMethodsUnexpectedSemicolonRecovers(t *testing.T) {
	src := `interface I { foo(); bar() }`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for semicolon separator")
	}
	iface := mod.Stmts[0].(*ast.InterfaceDecl)
	ifaceType, ok := iface.Type.(*ast.InterfaceType)
	if !ok {
		t.Fatalf("expected interface payload, got %T", iface.Type)
	}
	if len(ifaceType.Methods) != 2 {
		t.Fatalf("methods: got %d want 2", len(ifaceType.Methods))
	}
	if !strings.Contains(diag.EmitAllToString(), "expected '}' after interface methods") &&
		!strings.Contains(diag.EmitAllToString(), "add missing `,` here") {
		t.Fatalf("expected separator recovery diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

// TestParseMissingSemicolonRecoverable verifies the new recoverSemicolon
// helper synthesizes a semicolon when the next token is a stmt boundary.
func TestParseMissingSemicolonRecoverable(t *testing.T) {
	src := `const x: i32 = 1
const y: i32 = 2`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected at least one diagnostic for missing semicolons")
	}
	if len(mod.Stmts) != 2 {
		t.Fatalf("decls: got %d want 2 (parser should have recovered)", len(mod.Stmts))
	}
}

// TestParseMissingSemicolonUnrecoverable verifies that the helper gives up
// (returns nil) when the next token is not a boundary.
func TestParseMissingSemicolonUnrecoverable(t *testing.T) {
	src := `const x: i32 = 1 +`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics for malformed expression")
	}
}

// TestParseBindingFieldsDroppedUnusedParam verifies the parseBindingFields
// signature no longer carries the unused bindingKind parameter by ensuring
// the function still parses both let and const correctly.
func TestParseBindingFieldsDroppedUnusedParam(t *testing.T) {
	src := `fn main() {
	let a: i32 = 1;
}
const b: i32 = 2;`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn, ok := mod.Stmts[0].(*ast.FnDecl)
	if !ok || fn.Body == nil || len(fn.Body.Stmts) != 1 {
		t.Fatalf("decl[0] expected function with let body, got %#v", mod.Stmts[0])
	}
	if _, ok := fn.Body.Stmts[0].(*ast.LetDecl); !ok {
		t.Fatalf("stmt[0] expected let, got %T", fn.Body.Stmts[0])
	}
	if _, ok := mod.Stmts[1].(*ast.ConstDecl); !ok {
		t.Fatalf("decl[1] expected const, got %T", mod.Stmts[1])
	}
}

// TestParseSemicolonRecoveryNewline verifies that missing semicolon before a newline-started statement
// is recovered correctly with exactly 1 diagnostic and the statements are parsed successfully.
func TestParseSemicolonRecoveryNewline(t *testing.T) {
	src := `fn main() -> i32 {
	sayhi()
	return 0;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics for missing semicolon")
	}
	errors := diag.Diagnostics()
	if len(errors) != 1 {
		t.Fatalf("expected exactly 1 error, got %d:\n%s", len(errors), diag.EmitAllToString())
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	if len(fn.Body.Stmts) != 2 {
		t.Fatalf("expected 2 statements inside function body, got %d", len(fn.Body.Stmts))
	}
}

// TestParseSemicolonRecoveryNonSilent verifies that a missing semicolon when recovery is impossible
// still emits a diagnostic rather than failing silently.
func TestParseSemicolonRecoveryNonSilent(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = 1 write(stdout, x, 31);
}`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic when recovery fails")
	}
	if !strings.Contains(diag.EmitAllToString(), "expected ';' after statement") {
		t.Fatalf("expected missing semicolon error, got:\n%s", diag.EmitAllToString())
	}
}

// TestParseCommentsInsideBraces verifies comments inside braces (like struct or block)
// do not break structural checks.
func TestParseCommentsInsideBraces(t *testing.T) {
	src := `struct S {
	// struct comment
	x: i32,
	// trailing struct comment
}
interface I {
	// method comment
	foo() -> i32,
}
fn main() {
	// stmt comment
	let x = 1;
	// block trailing comment
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if len(mod.Stmts) != 3 {
		t.Fatalf("decls: got %d want 3", len(mod.Stmts))
	}
}

func TestParseDocCommentsPersistAcrossBlankLines(t *testing.T) {
	src := `/// spaced comment

fn main() {
	/// non-spaced comment
	let x = 1;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	if fn.Doc == nil || fn.Doc.Text != "spaced comment" {
		t.Fatalf("expected spaced comment to attach, got: %#v", fn.Doc)
	}
	let := fn.Body.Stmts[0].(*ast.LetDecl)
	if let.Doc == nil || let.Doc.Text != "non-spaced comment" {
		t.Fatalf("expected non-spaced comment to be attached, got: %#v", let.Doc)
	}
}

func TestParseTrailingCommentDoesNotAttachToNextStmt(t *testing.T) {
	src := `fn main() {
	let x = 1; // trailing
	/// next docs
	let y = 2;
}`
	mod, diag := parseTestModule(src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	if len(fn.Body.Stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(fn.Body.Stmts))
	}
	first := fn.Body.Stmts[0].(*ast.LetDecl)
	if first.Doc != nil {
		t.Fatalf("expected trailing comment to stay unattached, got %#v", first.Doc)
	}
	second := fn.Body.Stmts[1].(*ast.LetDecl)
	if second.Doc == nil || second.Doc.Text != "next docs" {
		t.Fatalf("expected next docs to attach to second let, got %#v", second.Doc)
	}
}

// --- Unclosed delimiter tests ---

func TestParseUnclosedBraceAtEOF(t *testing.T) {
	src := `fn main() -> i32 {`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for unclosed brace")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "P0010") {
		t.Fatalf("expected unclosed delimiter code P0010, got:\n%s", out)
	}
	if !strings.Contains(out, "unclosed '{'") {
		t.Fatalf("expected 'unclosed {' diagnostic, got:\n%s", out)
	}
}

func TestParseUnclosedParenAtEOF(t *testing.T) {
	src := `fn main(`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for unclosed paren")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "P0010") {
		t.Fatalf("expected unclosed delimiter code P0010, got:\n%s", out)
	}
	if !strings.Contains(out, "unclosed '('") {
		t.Fatalf("expected 'unclosed (' diagnostic, got:\n%s", out)
	}
}

func TestParseUnclosedBraceInStruct(t *testing.T) {
	src := `struct S { x: i32`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for unclosed struct brace")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "P0010") || !strings.Contains(out, "unclosed '{'") {
		t.Fatalf("expected P0010 unclosed brace, got:\n%s", out)
	}
}

func TestParseUnclosedBraceInImpl(t *testing.T) {
	src := `impl i32 {`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for unclosed impl brace")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "P0010") || !strings.Contains(out, "unclosed '{'") {
		t.Fatalf("expected P0010 unclosed brace, got:\n%s", out)
	}
}

func TestParseUnclosedParenInFuncType(t *testing.T) {
	src := `const cb: fn(i32`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for unclosed func type paren")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "P0010") || !strings.Contains(out, "unclosed '('") {
		t.Fatalf("expected P0010 unclosed paren, got:\n%s", out)
	}
}

func TestParseUnclosedBracketInTypeParams(t *testing.T) {
	src := `fn foo[T`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for unclosed type param bracket")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "P0010") || !strings.Contains(out, "unclosed '['") {
		t.Fatalf("expected P0010 unclosed bracket, got:\n%s", out)
	}
}

// --- Redundant comma / semicolon tests ---

func TestParseRedundantCommaInfo(t *testing.T) {
	src := `struct S { a: i32,,, }`
	_, diag := parseTestModule(src)
	out := diag.EmitAllToString()
	if !strings.Contains(out, "S0003") {
		t.Fatalf("expected redundant comma info S0003, got:\n%s", out)
	}
}

func TestParseRedundantSemicolonInfo(t *testing.T) {
	src := `fn main() -> i32 {
	let a = 1;;;
	return 0;
}`
	_, diag := parseTestModule(src)
	out := diag.EmitAllToString()
	if !strings.Contains(out, "S0002") {
		t.Fatalf("expected redundant semicolon info S0002, got:\n%s", out)
	}
}

func TestParseTrailingCommaInfo(t *testing.T) {
	src := `struct S { a: i32, }`
	_, diag := parseTestModule(src)
	out := diag.EmitAllToString()
	if !strings.Contains(out, "S0001") {
		t.Fatalf("expected trailing comma info S0001, got:\n%s", out)
	}
}

// --- Import recovery tests ---

func TestParseImportMissingAliasRecovers(t *testing.T) {
	src := `import "math" alias;
fn main() -> i32 { return 0; }`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for invalid alias syntax")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected 1 decl after recovery, got %d", len(mod.Stmts))
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "expected 'as' keyword for alias") {
		t.Fatalf("expected alias error, got:\n%s", out)
	}
}

func TestParseImportMissingSemicolonRecovers(t *testing.T) {
	src := `import "math"
fn main() -> i32 { return 0; }`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing semicolon")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected 1 decl after recovery, got %d", len(mod.Stmts))
	}
}

// --- Type declaration semicolon recovery ---

func TestParseTypeAliasMissingSemicolonRecovers(t *testing.T) {
	src := `type Point = i32
fn main() -> i32 { return 0; }`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for missing semicolon")
	}
	if len(mod.Stmts) != 2 {
		t.Fatalf("expected 2 decls after recovery, got %d", len(mod.Stmts))
	}
	if _, ok := mod.Stmts[0].(*ast.TypeAliasDecl); !ok {
		t.Fatalf("expected type alias decl, got %T", mod.Stmts[0])
	}
}

// --- synchronize recovery in function body ---

func TestParseSynchronizeRecoversInBlock(t *testing.T) {
	src := `fn main() -> i32 {
	let x: = 1;
	return 0;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for malformed let")
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	// Malformed let is now preserved as partial AST (Type: nil, Value: 1)
	if len(fn.Body.Stmts) != 2 {
		t.Fatalf("expected 2 stmts (partial let + return), got %d", len(fn.Body.Stmts))
	}
	letDecl, ok := fn.Body.Stmts[0].(*ast.LetDecl)
	if !ok {
		t.Fatalf("expected LetDecl, got %T", fn.Body.Stmts[0])
	}
	if letDecl.Name == nil || letDecl.Name.Name != "x" {
		t.Fatalf("expected name 'x', got %#v", letDecl.Name)
	}
	if letDecl.Type != nil {
		t.Fatalf("expected nil type (bad type annotation), got %T", letDecl.Type)
	}
	if _, ok := fn.Body.Stmts[1].(*ast.ReturnStmt); !ok {
		t.Fatalf("expected return stmt after recovery, got %T", fn.Body.Stmts[1])
	}
}

func TestParseSynchronizeRecoversToNextStatement(t *testing.T) {
	src := `fn main() -> i32 {
	foo(;
	let x = 1;
	return x;
}`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostic for malformed call")
	}
	fn := mod.Stmts[0].(*ast.FnDecl)
	if len(fn.Body.Stmts) < 2 {
		t.Fatalf("expected at least 2 stmts after recovery, got %d", len(fn.Body.Stmts))
	}
	foundLet := false
	for _, s := range fn.Body.Stmts {
		if _, ok := s.(*ast.LetDecl); ok {
			foundLet = true
			break
		}
	}
	if !foundLet {
		t.Fatalf("expected let decl recovered after synchronize")
	}
}

// --- Resilience regression tests ---

func TestParseBinaryExprPreservesLeftOnBadRight(t *testing.T) {
	src := `const x = 1 +;`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected errors")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(mod.Stmts))
	}
	letDecl, ok := mod.Stmts[0].(*ast.ConstDecl)
	if !ok {
		t.Fatalf("expected ConstDecl, got %T", mod.Stmts[0])
	}
	bin, ok := letDecl.Value.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("expected BinaryExpr, got %T", letDecl.Value)
	}
	if bin.Left == nil {
		t.Fatalf("left operand should be preserved")
	}
	if _, ok := bin.Left.(*ast.NumberLit); !ok {
		t.Fatalf("left should be NumberLit, got %T", bin.Left)
	}
	if _, ok := bin.Right.(*ast.BadExpr); !ok {
		t.Fatalf("right should be BadExpr, got %T", bin.Right)
	}
}

func TestParseBindingFieldWithBadTypeStillProducesConstDecl(t *testing.T) {
	src := `const x: = 1;`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected type error")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(mod.Stmts))
	}
	letDecl, ok := mod.Stmts[0].(*ast.ConstDecl)
	if !ok {
		t.Fatalf("expected ConstDecl, got %T", mod.Stmts[0])
	}
	if letDecl.Name == nil || letDecl.Name.Name != "x" {
		t.Fatalf("name should be preserved, got %#v", letDecl.Name)
	}
	if letDecl.Value == nil {
		t.Fatalf("value should be preserved")
	}
}

func TestParseDeduplicationSamePosition(t *testing.T) {
	src := `const x: = 1;`
	_, diag := parseTestModule(src)
	diags := diag.Diagnostics()
	seen := map[string]int{}
	for _, d := range diags {
		if d.Severity != diagnostics.Error || len(d.Labels) == 0 {
			continue
		}
		loc := d.Labels[0].Location
		if loc == nil || loc.Start == nil {
			continue
		}
		key := fmt.Sprintf("%d:%d", loc.Start.Line, loc.Start.Column)
		seen[key]++
	}
	for pos, count := range seen {
		if count > 1 {
			t.Fatalf("position %s has %d errors (expected at most 1)", pos, count)
		}
	}
}

func TestParseDidYouMeanKeyword(t *testing.T) {
	src := `fn main() {
	let fn = 1;
}`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected error for keyword used as identifier")
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, "reserved keyword") {
		t.Fatalf("expected 'reserved keyword' suggestion, got:\n%s", out)
	}
}

func TestParseContextInFunctionName(t *testing.T) {
	src := `fn main(`
	_, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected error for unclosed paren")
	}
}

func TestParseBadExprInGrouping(t *testing.T) {
	src := `const x = (+);`
	mod, diag := parseTestModule(src)
	if !diag.HasErrors() {
		t.Fatalf("expected errors")
	}
	if len(mod.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(mod.Stmts))
	}
	letDecl, ok := mod.Stmts[0].(*ast.ConstDecl)
	if !ok {
		t.Fatalf("expected ConstDecl, got %T", mod.Stmts[0])
	}
	// Value should be a UnaryExpr (not nil)
	if letDecl.Value == nil {
		t.Fatalf("expected partial value, got nil")
	}
}

func TestEmitterNewFormatNoSeverityPrefix(t *testing.T) {
	loc := source.NewLocation("test"+peeper.SourceExt,
		source.Position{Line: 1, Column: 1},
		source.Position{Line: 1, Column: 5})
	diag := diagnostics.NewError("test error").
		WithCode("E9999").
		WithPrimaryLabel(loc, "test label")
	bag := diagnostics.NewDiagnosticBag()
	bag.Add(diag)
	out := bag.EmitAllToString()
	// Should NOT contain "error" prefix
	if strings.Contains(out, "error[") {
		t.Fatalf("expected no 'error[' prefix in output:\n%s", out)
	}
	// Should contain the code
	if !strings.Contains(out, "[E9999]") {
		t.Fatalf("expected [E9999] in output:\n%s", out)
	}
	// Should contain location marker
	if !strings.Contains(out, "test"+peeper.SourceExt+":1:1") {
		t.Fatalf("expected location in output:\n%s", out)
	}
}
