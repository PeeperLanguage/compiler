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
	hirMod, hirText := lowerHIR(module)
	if hirMod == nil || !strings.Contains(hirText, "fn helper(x: i32) -> i32") || !strings.Contains(hirText, "fn main() -> i32") || !strings.Contains(hirText, "return") {
		t.Fatalf("hir lowering failed: %q", hirText)
	}
	mirMod, mirText := lowerMIR(module)
	if mirMod == nil || !strings.Contains(mirText, "fn helper(x: i32) -> i32") || !strings.Contains(mirText, "fn main() -> i32") || !strings.Contains(mirText, "ret") {
		t.Fatalf("mir lowering failed: %q", mirText)
	}
	ir := lowerLLVMIR(module)
	if !strings.Contains(ir, "define i32 @helper(i32 %x)") || !strings.Contains(ir, "define i32 @main()") || !strings.Contains(ir, "ret i32") {
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
