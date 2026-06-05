package typechecker

import (
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/collector"
	"compiler/internal/analysis/semantics/resolver"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func checkTypeSource(t *testing.T, src string) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "typechecker_test.em"
	diag := diagnostics.NewDiagnosticBag(filePath)
	diag.AddSourceContent(filePath, src)
	ctx := context.New(".", ".em", diag)
	modAST := parser.ParseModule(filePath, lexer.Lex(filePath, src, diag), diag)
	module := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, filePath),
		ImportPath: "typechecker_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]context.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	resolver.Resolve(ctx, module)
	Check(ctx, module)
	return diag
}

func hasTypeCode(diag *diagnostics.DiagnosticBag, code string) bool {
	if diag == nil {
		return false
	}
	for _, item := range diag.Diagnostics() {
		if item != nil && item.Code == code {
			return true
		}
	}
	return false
}

func TestImplMethodAllowsSelfForBuiltinTarget(t *testing.T) {
	src := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestInterfaceAndImplAllowSelf(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestSelfOutsideInterfaceOrImplIsRejected(t *testing.T) {
	src := `fn bad(value: Self) -> i32 {
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrInvalidType) {
		t.Fatalf("expected invalid type diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestBuiltinMethodCallResolvesThroughImpl(t *testing.T) {
	src := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}

fn main() -> i32 {
	let x: i32 = 1;
	return x.abs();
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestStructFieldAccessResolves(t *testing.T) {
	src := `struct Point {
	x: i32,
}

fn main() -> i32 {
	let p: Point;
	return p.x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfInterfaceAssignmentRequiresPointerValue(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main(file: ^File) -> i32 {
	let reader: Reader = file;
	return reader.read("ok");
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfInterfaceAssignmentRejectsValue(t *testing.T) {
	src := `interface Reader {
	read(^Self, buf: cstr): i32,
}

struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main() -> i32 {
	let file: File;
	let reader: Reader = file;
	return 0;
}`
	diag := checkTypeSource(t, src)
	if !hasTypeCode(diag, diagnostics.ErrTypeMismatch) {
		t.Fatalf("expected type mismatch diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestPointerSelfMethodCallResolvesOnPointerValue(t *testing.T) {
	src := `struct File {}

impl File {
	fn read(self: ^Self, buf: cstr) -> i32 {
		return 0;
	}
}

fn main(file: ^File) -> i32 {
	return file.read("ok");
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestStructLiteralAssignsToNamedStruct(t *testing.T) {
	src := `struct Point {
	x: i32,
	y: i32,
}

fn main() -> i32 {
	let p: Point = .{ x = 1, y = 2 };
	return p.x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}

func TestAnonymousStructLiteralInfersShape(t *testing.T) {
	src := `fn main() -> i32 {
	let p = .{ x = 1, y = 2 };
	return p.x;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}
