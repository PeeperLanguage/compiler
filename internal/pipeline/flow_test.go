package pipeline

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/analysis/semantics/typeinfo"
	"compiler/internal/context"
	"compiler/internal/frontend/ast"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func TestFullFlowArithmeticLowering(t *testing.T) {
	src := `fn helper(x: i32) -> i32 {
	return x + 1;
}

fn main() -> i32 {
	let a = 10;
	let b = 2 + 3 * 4;
	return a + b;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	hirMod, hirText := lowerHIR(module, diag)
	if hirMod == nil || !strings.Contains(hirText, "fn helper(") || !strings.Contains(hirText, "fn main() -> i32") || !strings.Contains(hirText, "return") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	mirMod, mirText := lowerMIR(module)
	if mirMod == nil || !strings.Contains(mirText, "fn helper(") || !strings.Contains(mirText, "fn main() -> i32") || !strings.Contains(mirText, "ret") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	ir := lowerLLVMIR(module)
	if !strings.Contains(ir, "define i32 @helper(i32 %") || !strings.Contains(ir, "define i32 @main()") || !strings.Contains(ir, "ret i32") {
		t.Fatalf("llvm lowering failed:\n%s", ir)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestFullFlowNumericCasts(t *testing.T) {
	src := `fn main() -> i32 {
	let f32_val: f32 = 3.14;
	let f64_from_int: f64 = 10;
	let from_f32: i32 = f32_val as i32;
	let mixed: f64 = (10 as f64) + (2.5 as f64);
	let small_i8: i8 = -128 as i8;
	let large_u64: u64 = 18446744073709551615 as u64;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if _, hirText := lowerHIR(module, diag); !strings.Contains(hirText, "let mixed$") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	if _, mirText := lowerMIR(module); !strings.Contains(mirText, "cast") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	if ir := lowerLLVMIR(module); !strings.Contains(ir, "define i32 @main()") || !strings.Contains(ir, "float 0x") {
		t.Fatalf("llvm lowering failed:\n%s", ir)
	}
}

func TestFullFlowInferredIntegerUsesMinimumConcreteType(t *testing.T) {
	src := `fn main() -> i32 {
	let a = 4294967295;
	let b: i64 = a + 4;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	if _, hirText := lowerHIR(module, diag); !strings.Contains(hirText, "4294967295") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
}

func TestFullFlowNegativeIntegerUsesSignedMinimumConcreteType(t *testing.T) {
	src := `fn main() -> i64 {
	let a = -9223372036854775808;
	return a;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
	mainFn := astMod.Decls[0].(*ast.FnDecl)
	decl := mainFn.Body.Stmts[0].(*ast.LetDecl)
	res, ok := module.Bindings.LookupNode(decl.Name)
	if !ok || res == nil || res.Symbol == nil {
		t.Fatalf("missing binding for a")
	}
	typ, ok := module.Types.LookupSymbolType(res.Symbol)
	if !ok || !typeinfo.SameType(typ, &typeinfo.IntegerType{Signed: true, Bits: 64}) {
		t.Fatalf("expected a to infer i64, got %s", typeinfo.TypeText(typ))
	}
}

func TestFullFlowInferredBindingDoesNotRetypeFromLaterContext(t *testing.T) {
	src := `fn main() -> i32 {
	let a = 2;
	let b: i8 = a;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if out := diag.EmitAllToString(); !strings.Contains(out, "cannot assign i32 to i8") {
		t.Fatalf("expected concrete inferred type mismatch, got:\n%s", out)
	}
}

func TestFullFlowRejectsSelfReferentialInitializer(t *testing.T) {
	src := `fn main() -> i32 {
	let a = a;
	let b: i32 = a;
	return b;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if out := diag.EmitAllToString(); !strings.Contains(out, "used before it's defined") {
		t.Fatalf("expected use-before-def diagnostic for self init, got:\n%s", out)
	}
}

func TestFullFlowUndefinedSymbolFailsInResolver(t *testing.T) {
	src := `fn main() -> i32 {
	return missing + 1;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
}

func TestFullFlowMalformedLetDoesNotPanicInCollector(t *testing.T) {
	src := `fn main() -> i32 {
	let x: i32 = +;
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
	}
	if module.Types == nil {
		t.Fatalf("analyze did not complete")
	}
}

func TestFullFlowNestedBlockShadowing(t *testing.T) {
	src := `fn main() -> i32 {
	let a = 1;
	{
		let a = 2;
		return a;
	}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, hirText := lowerHIR(module, diag)
	if !strings.Contains(hirText, "let a$") || !strings.Contains(hirText, "return a$") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	_, mirText := lowerMIR(module)
	if !strings.Contains(mirText, "ret a$") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestHIRFoldConstantIfElse(t *testing.T) {
	src := `fn main() -> i32 {
	if 1 < 2 {
		return 1;
	} else if 2 < 3 {
		return 2;
	} else {
		return 3;
	}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, hirText := lowerHIR(module, diag)
	if strings.Contains(hirText, "if ") || !strings.Contains(hirText, "return 1") || strings.Contains(hirText, "return 3") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	_, mirText := lowerMIR(module)
	if strings.Contains(mirText, "br ") || !strings.Contains(mirText, "ret 1") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	llvmText := lowerLLVMIR(module)
	if strings.Contains(llvmText, "br i1") || !strings.Contains(llvmText, "ret i32 1") {
		t.Fatalf("llvm lowering failed:\n%s", llvmText)
	}
	out := diag.EmitAllToString()
	if strings.Count(out, diagnostics.WarnConstantConditionTrue) != 2 {
		t.Fatalf("expected fold diagnostics, got: %s", out)
	}
}

func TestHIRFoldWarnsUnreachableAfterConstantIf(t *testing.T) {
	src := `fn main() -> i32 {
	if 1 < 2 {
		return 1;
	}
	return 2;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, hirText := lowerHIR(module, diag)
	if strings.Contains(hirText, "return 2") || !strings.Contains(hirText, "return 1") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, diagnostics.WarnConstantConditionTrue) || !strings.Contains(out, diagnostics.WarnUnreachableCode) {
		t.Fatalf("expected fold diagnostics, got: %s", out)
	}
}

func TestHIRFoldConstBindingCondition(t *testing.T) {
	src := `fn main() -> i32 {
	const a = 2 + 5;
	if a < 10 {
		return a;
	}
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, hirText := lowerHIR(module, diag)

	if !strings.Contains(hirText, "const a$") || !strings.Contains(hirText, "return 7") || strings.Contains(hirText, "if ") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, diagnostics.WarnConstantConditionTrue) || !strings.Contains(out, diagnostics.WarnUnreachableCode) {
		t.Fatalf("expected const-fold diagnostics, got: %s", out)
	}
}

func TestHIRKeepsNonConstantIfElse(t *testing.T) {
	src := `fn helper(x: i32) -> i32 {
	if x {
		return 1;
	} else {
		return 2;
	}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, hirText := lowerHIR(module, diag)
	if !strings.Contains(hirText, "if x$") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	_, mirText := lowerMIR(module)
	if !strings.Contains(mirText, "br x$") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestFullFlowBuiltinScalarLowering(t *testing.T) {
	src := `fn add_i64(a: i64, b: i64) -> i64 {
	return a + b;
}

fn add_u64(a: u64, b: u64) -> u64 {
	return a + b;
}

fn add_f64(a: f64, b: f64) -> f64 {
	return a + b;
}

fn main() -> i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, hirText := lowerHIR(module, diag)
	if !strings.Contains(hirText, "fn add_i64(") || !strings.Contains(hirText, "fn add_u64(") || !strings.Contains(hirText, "fn add_f64(") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	_, mirText := lowerMIR(module)
	if !strings.Contains(mirText, "fn add_i64(") || !strings.Contains(mirText, "fn add_f64(") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	llvmText := lowerLLVMIR(module)
	if !strings.Contains(llvmText, "define i64 @add_i64") || !strings.Contains(llvmText, "define i64 @add_u64") || !strings.Contains(llvmText, "define double @add_f64") || !strings.Contains(llvmText, "fadd double") {
		t.Fatalf("llvm lowering failed:\n%s", llvmText)
	}
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diag.EmitAllToString())
	}
}

func TestHIRCFGReturnCheckPointsToNestedIfBranch(t *testing.T) {
	src := `fn f(a: i32) -> i32 {
	if a < 2 {
		return 10;
	} else if a < 3 {
		if a == 0 {
			let x = 1;
		} else {
			return 23;
		}
	} else {
		return 30;
	}
}

fn main() -> i32 {
	return 0;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	stream := lexer.Lex("test.em", src, diag)
	astMod := parser.ParseModule("test.em", stream, diag)
	module := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	module.AST = astMod
	analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if module.Types == nil {
		t.Fatalf("analyze failed")
	}
	_, _ = lowerHIR(module, diag)
	if !diag.HasErrors() {
		t.Fatalf("expected missing-return error")
	}
	foundBranch := false
	foundHelp := false
	for _, item := range diag.Diagnostics() {
		if item == nil || item.Code != diagnostics.ErrMissingReturn {
			continue
		}
		if strings.Contains(item.Help, "fulfill the return or add a fallback return on parent scope") {
			foundHelp = true
		}
		for _, lab := range item.Labels {
			if lab.Style == diagnostics.Secondary && strings.Contains(lab.Message, "this branch does not return a value") {
				foundBranch = true
			}
		}
	}
	if !foundBranch || !foundHelp {
		t.Fatalf("expected missing-return branch label + safe help, got:\n%s", diag.EmitAllToString())
	}
}

func TestPipelineSkipsHIRAfterSemanticError(t *testing.T) {
	src := `fn main() -> i32 {
	const b: i32;
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	p := New(ctx)
	entry := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	result := p.Run(entry)
	stage := result.Stages[entry.Key]
	if stage == nil {
		t.Fatalf("missing stage result")
	}
	if !strings.Contains(stage.HIRText, "const b$") || !strings.Contains(stage.HIRText, "<invalid: missing initializer>") {
		t.Fatalf("expected partial HIR with invalid sentinel, got HIR=%q", stage.HIRText)
	}
	if stage.MIRText != "" || stage.LLVMIR != "" {
		t.Fatalf("expected MIR/LLVM to stop after semantic error, got MIR=%q LLVM=%q", stage.MIRText, stage.LLVMIR)
	}
	out := diag.EmitAllToString()
	if !strings.Contains(out, diagnostics.ErrMissingInitializer) {
		t.Fatalf("expected missing initializer diagnostic, got:\n%s", out)
	}
	if !strings.Contains(out, diagnostics.ErrMissingReturn) {
		t.Fatalf("expected missing return diagnostic too, got:\n%s", out)
	}
}

func TestPipelineReportsMissingReturnAlongsideNonControlFlowBindingError(t *testing.T) {
	src := `fn main() -> i32 {
	let a: i32 = 1;
	const b: i32;
	let c: i32 = 3;
	if a > b {
		return 10;
	} else if a < b {
		if a == b {
			return 22;
		} else {
			return 23;
		}
	} else {
	}
}`
	diag := diagnostics.NewDiagnosticBag("test.em")
	ctx := context.NewWithConfig(context.Config{}, diag)
	p := New(ctx)
	entry := &context.Module{
		Key:        "local:/tmp/test.em",
		ImportPath: "test",
		FilePath:   "test.em",
		Content:    src,
	}
	_ = p.Run(entry)
	out := diag.EmitAllToString()
	if !strings.Contains(out, diagnostics.ErrMissingInitializer) {
		t.Fatalf("expected missing initializer diagnostic, got:\n%s", out)
	}
	if !strings.Contains(out, diagnostics.ErrMissingReturn) {
		t.Fatalf("expected missing return diagnostic too, got:\n%s", out)
	}
}
