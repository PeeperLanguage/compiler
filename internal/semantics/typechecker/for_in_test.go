package typechecker

import (
	"strings"
	"testing"

	"compiler/internal/frontend/ast"
	"compiler/internal/semantics/typecheckresult"
	"compiler/internal/semantics/typeinfo"
	"compiler/internal/target"
)

func TestCheckForInOverRange(t *testing.T) {
	src := `fn main() -> i32 {
let mut total: i32 = 0;
for i in 0..5 {
	total = total + i;
}
return total;
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	loop := fn.Body.Stmts[1].(*ast.ForStmt)
	binding := module.Bindings.NodeSymbols[loop.Value.ID()]
	if binding == nil {
		t.Fatal("missing resolved loop binding")
	}
	if got := typeinfo.TypeText(binding.Type); got != "i32" {
		t.Fatalf("loop binding type = %s, want i32", got)
	}
	var reference *ast.Ident
	ast.Inspect(loop.Body, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && ident.Name == "i" {
			reference = ident
		}
		return true
	})
	if reference == nil {
		t.Fatal("missing loop binding reference")
	}
	if resolved := module.Bindings.NodeSymbols[reference.ID()]; resolved != binding {
		t.Fatalf("loop reference resolved to %#v, want declaration symbol %#v", resolved, binding)
	}
}

func TestCheckForInIndexValueOverRange(t *testing.T) {
	src := `fn main() -> i32 {
let mut total: i32 = 0;
for index, value in 0..5 {
	total = total + index + value;
}
return total;
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	loop := fn.Body.Stmts[1].(*ast.ForStmt)
	evidence, ok := module.Typechecking.ForIterations[loop.ID()]
	if !ok {
		t.Fatal("missing range iteration evidence")
	}
	if evidence.Kind != typecheckresult.ForIterationRange || evidence.Cursor == nil || evidence.End == nil || evidence.Ordinal == nil {
		t.Fatalf("range iteration evidence = %#v", evidence)
	}
	if evidence.Index != module.Bindings.NodeSymbols[loop.Index.ID()] || evidence.Value != module.Bindings.NodeSymbols[loop.Value.ID()] {
		t.Fatal("range evidence does not preserve source binding symbols")
	}
	for name, symbol := range map[string]string{
		"cursor":  typeinfo.TypeText(evidence.Cursor.Type),
		"end":     typeinfo.TypeText(evidence.End.Type),
		"ordinal": typeinfo.TypeText(evidence.Ordinal.Type),
		"index":   typeinfo.TypeText(evidence.Index.Type),
		"value":   typeinfo.TypeText(evidence.Value.Type),
	} {
		if symbol != "i32" {
			t.Fatalf("%s type = %s, want i32", name, symbol)
		}
	}
}

func TestCheckForInRangeTypeIsBoundOrderIndependent(t *testing.T) {
	for _, test := range []struct {
		name      string
		rangeText string
	}{
		{name: "typed start", rangeText: "0i64..3"},
		{name: "typed end", rangeText: "0..3i64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			module, diag := checkTypeModule(t, "fn main() { for value in "+test.rangeText+" {} }")
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
			}
			fn := module.AST.Stmts[0].(*ast.FnDecl)
			loop := fn.Body.Stmts[0].(*ast.ForStmt)
			evidence, found := module.Typechecking.ForIterations[loop.ID()]
			if !found {
				t.Fatal("missing range iteration evidence")
			}
			for name, typ := range map[string]typeinfo.Type{
				"element": evidence.ElementType,
				"cursor":  evidence.Cursor.Type,
				"end":     evidence.End.Type,
				"value":   evidence.Value.Type,
			} {
				if got := typeinfo.TypeText(typ); got != "i64" {
					t.Fatalf("%s type = %s, want i64", name, got)
				}
			}
			if !evidence.GuaranteedEntry {
				t.Fatal("ascending constant range lost guaranteed-entry proof")
			}
		})
	}
}

func TestCheckForInPreservesRangeValueWidth(t *testing.T) {
	src := `fn main() -> i64 {
for value in 0i64..3i64 {
	return value;
}
return 0i64;
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	loop := fn.Body.Stmts[0].(*ast.ForStmt)
	evidence := module.Typechecking.ForIterations[loop.ID()]
	for name, typ := range map[string]typeinfo.Type{
		"element": evidence.ElementType,
		"cursor":  evidence.Cursor.Type,
		"end":     evidence.End.Type,
		"value":   evidence.Value.Type,
	} {
		if got := typeinfo.TypeText(typ); got != "i64" {
			t.Fatalf("%s type = %s, want i64", name, got)
		}
	}
}

func TestCheckForInRecordsGuaranteedRangeEntry(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     string
		guaranteed bool
	}{
		{name: "ascending constants", source: "fn main() { for value in 0..1 {} }", guaranteed: true},
		{name: "equal constants", source: "fn main() { for value in 1..1 {} }"},
		{name: "descending constants", source: "fn main() { for value in 1..0 {} }"},
		{name: "runtime bounds", source: "fn main(start: i32, end: i32) { for value in start..end {} }"},
	} {
		t.Run(test.name, func(t *testing.T) {
			module, diag := checkTypeModule(t, test.source)
			if diag.HasErrors() {
				t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
			}
			fn := module.AST.Stmts[0].(*ast.FnDecl)
			loop := fn.Body.Stmts[0].(*ast.ForStmt)
			evidence, found := module.Typechecking.ForIterations[loop.ID()]
			if !found {
				t.Fatal("missing range iteration evidence")
			}
			if evidence.GuaranteedEntry != test.guaranteed {
				t.Fatalf("guaranteed entry = %v, want %v", evidence.GuaranteedEntry, test.guaranteed)
			}
		})
	}
}

func TestCheckForInOverArray(t *testing.T) {
	src := `fn main() -> i32 {
let mut total: i32 = 0;
let items = [3]i32{1, 2, 3};
for v in items {
	total = total + v;
}
return total;
}`
	module, diag := checkTypeModule(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	loop := fn.Body.Stmts[2].(*ast.ForStmt)
	evidence, ok := module.Typechecking.ForIterations[loop.ID()]
	if !ok {
		t.Fatal("missing sequence iteration evidence")
	}
	if evidence.Kind != typecheckresult.ForIterationSequence || evidence.Carrier == nil || evidence.Cursor == nil {
		t.Fatalf("sequence iteration evidence = %#v", evidence)
	}
	if got := typeinfo.TypeText(evidence.Carrier.Type); got != "&[3]i32" {
		t.Fatalf("carrier type = %s, want &[3]i32", got)
	}
	wantCursor, ok := typeinfo.NumericTypeFromName("usize", target.Host())
	if !ok || !typeinfo.SameType(evidence.Cursor.Type, wantCursor) {
		t.Fatalf("cursor type = %s, want target usize", typeinfo.TypeText(evidence.Cursor.Type))
	}
	if got := typeinfo.TypeText(evidence.ElementType); got != "i32" {
		t.Fatalf("element type = %s, want i32", got)
	}
}

func TestCheckForInRejectsTemporaryArrayStorage(t *testing.T) {
	src := `fn main() {
	for value in []i32{1, 2} {}
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for temporary array iteration")
	}
	if !strings.Contains(diag.EmitAllToString(), "requires addressable array storage") {
		t.Fatalf("expected addressable-storage diagnostic, got: %s", diag.EmitAllToString())
	}
}

func TestCheckForInRejectsMoveOnlySequenceElements(t *testing.T) {
	src := `struct Item { value: i32 }
fn main() {
	let items = [1]Item{.{ value = 1 }};
	for item in items {}
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for move-only sequence elements")
	}
	if !strings.Contains(diag.EmitAllToString(), "requires copyable sequence elements") {
		t.Fatalf("expected copyable-element diagnostic, got: %s", diag.EmitAllToString())
	}
}

func TestCheckForInRejectsNonIterable(t *testing.T) {
	src := `fn main() -> i32 {
for v in 5 {
	return 1;
}
return 0;
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for non-iterable")
	}
	if !strings.Contains(diag.EmitAllToString(), "cannot iterate over") {
		t.Fatalf("expected iterate diagnostic, got: %s", diag.EmitAllToString())
	}
}

func TestCheckForInRejectsInclusiveRange(t *testing.T) {
	src := `fn main() -> i32 {
for v in 0..=5 {
	return v;
}
return 0;
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for inclusive for range")
	}
	if !strings.Contains(diag.EmitAllToString(), "requires an exclusive end") {
		t.Fatalf("expected exclusive-range diagnostic, got: %s", diag.EmitAllToString())
	}
}

func TestCheckForInRejectsUnboundedRange(t *testing.T) {
	src := `fn main() -> i32 {
for v in 0.. {
	return 1;
}
return 0;
}`
	// The parser rejects the missing end bound; the typechecker keeps a
	// defensive guard for recovery paths that produce a boundless range.
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for unbounded range")
	}
}

func TestCheckForInRejectsStringIteration(t *testing.T) {
	src := `fn main() -> i32 {
for b in "abc" {
	return 1;
}
return 0;
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for string iteration")
	}
	if !strings.Contains(diag.EmitAllToString(), "as_bytes") {
		t.Fatalf("expected as_bytes help, got: %s", diag.EmitAllToString())
	}
}

func TestRejectedForInDoesNotPublishIterationEvidence(t *testing.T) {
	for _, test := range []struct {
		name string
		src  string
	}{
		{name: "inclusive range", src: "fn main() { for value in 0..=2 {} }"},
		{name: "missing range end", src: "fn main() { for value in 0.. {} }"},
		{name: "non-integral range", src: "fn main() { for value in 0.5..2.5 {} }"},
		{name: "incompatible range", src: "fn main() { for value in 0i32..2u32 {} }"},
		{name: "temporary array", src: "fn main() { for value in []i32{1, 2} {} }"},
		{name: "move-only elements", src: "struct Item { value: i32 } fn main() { let items = [1]Item{.{ value = 1 }}; for item in items {} }"},
		{name: "non-iterable", src: "fn main() { for value in 5 {} }"},
		{name: "string", src: "fn main() { for value in \"text\" {} }"},
		{name: "recovery binding", src: "fn main() { let values = [1]i32{1}; for index, in values {} }"},
	} {
		t.Run(test.name, func(t *testing.T) {
			module, diag := checkTypeModule(t, test.src)
			if !diag.HasErrors() {
				t.Fatal("expected rejected for-in diagnostic")
			}
			var loop *ast.ForStmt
			for _, stmt := range module.AST.Stmts {
				ast.Inspect(stmt, func(node ast.Node) bool {
					if candidate, ok := node.(*ast.ForStmt); ok && loop == nil {
						loop = candidate
					}
					return true
				})
			}
			if loop == nil {
				t.Fatal("missing recovered for-in loop")
			}
			if _, found := module.Typechecking.ForIterations[loop.ID()]; found {
				t.Fatal("rejected for-in loop retained semantic evidence")
			}
		})
	}
}

func TestRejectedForInStillChecksBody(t *testing.T) {
	module, diag := checkTypeModule(t, `fn main() {
	for value in 0..=2 {
		missing = value;
	}
}`)
	if !strings.Contains(diag.EmitAllToString(), "requires an exclusive end") ||
		!strings.Contains(diag.EmitAllToString(), "unknown identifier") {
		t.Fatalf("expected header and body diagnostics, got: %s", diag.EmitAllToString())
	}
	fn := module.AST.Stmts[0].(*ast.FnDecl)
	loop := fn.Body.Stmts[0].(*ast.ForStmt)
	if _, found := module.Typechecking.ForIterations[loop.ID()]; found {
		t.Fatal("rejected loop retained semantic evidence")
	}
}

func TestCheckBreakContinueInsideLoop(t *testing.T) {
	src := `fn main() -> i32 {
for i in 0..10 {
	if i == 3 {
		continue;
	}
	if i == 6 {
		break;
	}
}
return 0;
}`
	diag := checkTypeSource(t, src)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestCheckBreakOutsideLoopRejected(t *testing.T) {
	src := `fn main() -> i32 {
break;
return 0;
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for break outside loop")
	}
	if !strings.Contains(diag.EmitAllToString(), "break outside loop") {
		t.Fatalf("expected break diagnostic, got: %s", diag.EmitAllToString())
	}
}

func TestCheckContinueOutsideLoopRejected(t *testing.T) {
	src := `fn main() -> i32 {
if true {
	continue;
}
return 0;
}`
	diag := checkTypeSource(t, src)
	if !diag.HasErrors() {
		t.Fatal("expected diagnostic for continue outside loop")
	}
	if !strings.Contains(diag.EmitAllToString(), "continue outside loop") {
		t.Fatalf("expected continue diagnostic, got: %s", diag.EmitAllToString())
	}
}
