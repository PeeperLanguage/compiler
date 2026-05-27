package pipeline

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/context"
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
	ok := analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !ok || module.Types == nil {
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
	ok := analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if ok {
		t.Fatalf("expected analyze failure")
	}
	if !diag.HasErrors() {
		t.Fatalf("expected diagnostics")
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
	ok := analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !ok || module.Types == nil {
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
	ok := analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !ok || module.Types == nil {
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
	ok := analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !ok || module.Types == nil {
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
	ok := analyze(context.NewWithConfig(context.Config{}, diag), module, diag)
	if !ok || module.Types == nil {
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
