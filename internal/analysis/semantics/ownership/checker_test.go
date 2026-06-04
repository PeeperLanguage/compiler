package ownership

import (
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/collector"
	"compiler/internal/analysis/semantics/resolver"
	"compiler/internal/analysis/semantics/typechecker"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func checkOwnershipSource(t *testing.T, src string) *diagnostics.DiagnosticBag {
	t.Helper()
	const filePath = "ownership_test.em"
	diag := diagnostics.NewDiagnosticBag(filePath)
	diag.AddSourceContent(filePath, src)
	ctx := context.New(".", ".em", diag)
	stream := lexer.Lex(filePath, src, diag)
	modAST := parser.ParseModule(filePath, stream, diag)
	module := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, filePath),
		ImportPath: "ownership_test",
		FilePath:   filePath,
		Content:    src,
		AST:        modAST,
		Imports:    make(map[string]context.ResolvedImport),
	}
	ctx.AddModule(module)
	collector.Collect(ctx, module)
	resolver.Resolve(ctx, module)
	typechecker.Check(ctx, module)
	Check(ctx, module)
	return diag
}

func hasCode(diag *diagnostics.DiagnosticBag, code string) bool {
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

func TestUseAfterMoveOnStructParam(t *testing.T) {
	src := `fn main(x: struct { value: i32; }) -> i32 {
	let y = x;
	let z = x;
	return 0;
}`
	diag := checkOwnershipSource(t, src)
	if !hasCode(diag, diagnostics.ErrUseAfterMove) {
		t.Fatalf("expected use-after-move diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestStoredBorrowConflictsWithLaterMutableBorrow(t *testing.T) {
	src := `fn main() -> i32 {
	let mut value: i32 = 0;
	let a = &value;
	let b = &mut value;
	return 0;
}`
	diag := checkOwnershipSource(t, src)
	if !hasCode(diag, diagnostics.ErrBorrowConflict) {
		t.Fatalf("expected borrow-conflict diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnBorrowExprIsRejectedAsEscape(t *testing.T) {
	src := `fn main() -> &i32 {
	let value: i32 = 0;
	return &value;
}`
	diag := checkOwnershipSource(t, src)
	if !hasCode(diag, diagnostics.ErrBorrowEscape) {
		t.Fatalf("expected borrow-escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestReturnStoredBorrowIsRejectedAsEscape(t *testing.T) {
	src := `fn main() -> &i32 {
	let value: i32 = 0;
	let r = &value;
	return r;
}`
	diag := checkOwnershipSource(t, src)
	if !hasCode(diag, diagnostics.ErrBorrowEscape) {
		t.Fatalf("expected borrow-escape diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

func TestInnerBlockBorrowEndsAtBlockBoundary(t *testing.T) {
	src := `fn main() -> i32 {
	let mut value: i32 = 0;
	{
		let a = &value;
	}
	let b = &mut value;
	return 0;
}`
	diag := checkOwnershipSource(t, src)
	if hasCode(diag, diagnostics.ErrBorrowConflict) {
		t.Fatalf("did not expect borrow-conflict diagnostic, got:\n%s", diag.EmitAllToString())
	}
}

// TestOwnershipFixtureBorrowConflict encodes the pattern from x_test/ownership_0.em
// as an automated assertion: storing a shared borrow then requesting a mutable
// borrow on the same value must produce ErrBorrowConflict.
func TestOwnershipFixtureBorrowConflict(t *testing.T) {
	src := `fn main() -> i32 {
	let mut value: i32 = 0;
	let a = &value;
	let b = &mut value;
	return 0;
}`
	diag := checkOwnershipSource(t, src)
	if !hasCode(diag, diagnostics.ErrBorrowConflict) {
		t.Fatalf("ownership_0 pattern: expected borrow-conflict diagnostic, got:\n%s", diag.EmitAllToString())
	}
}
