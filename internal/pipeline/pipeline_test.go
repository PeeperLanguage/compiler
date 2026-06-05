package pipeline

import (
	"strings"
	"testing"

	"compiler/core/diagnostics"
	"compiler/internal/context"
	"compiler/internal/frontend/lexer"
	"compiler/internal/frontend/parser"
)

func buildPipelineTest(t *testing.T, preludeSrc, entrySrc string) *diagnostics.DiagnosticBag {
	t.Helper()
	const preludePath = "_builtin_library/global.em"
	const entryPath = "entry.em"

	diag := diagnostics.NewDiagnosticBag(entryPath)
	diag.AddSourceContent(preludePath, preludeSrc)
	diag.AddSourceContent(entryPath, entrySrc)
	ctx := context.New(".", ".em", diag)

	// Register the prelude so the pipeline loader can find it.
	prelude := &context.Module{
		Key:        "core:prelude/global",
		ImportPath: "prelude/global",
		FilePath:   preludePath,
		Origin:     context.ModuleOriginStdlib,
		AST:        parser.ParseModule(preludePath, lexer.Lex(preludePath, preludeSrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}
	ctx.AddModule(prelude)

	entry := &context.Module{
		Key:        context.ModuleKeyFor(context.ModuleOriginLocal, entryPath),
		ImportPath: strings.TrimSuffix(entryPath, ".em"),
		FilePath:   entryPath,
		Origin:     context.ModuleOriginLocal,
		AST:        parser.ParseModule(entryPath, lexer.Lex(entryPath, entrySrc, diag), diag),
		Imports:    make(map[string]context.ResolvedImport),
	}

	if err := New(ctx).Run(entry); err != nil {
		t.Fatalf("pipeline.Run returned error: %v", err)
	}
	return diag
}

// TestPipelinePreludeSymbolsVisibleInEntry verifies that prelude-defined symbols
// (write, stdout, etc.) are resolved correctly in user entry modules even when
// the entry module has no explicit import of the prelude.
func TestPipelinePreludeSymbolsVisibleInEntry(t *testing.T) {
	preludeSrc := `let stdin:  i32 = 0;
let stdout: i32 = 1;
let stderr: i32 = 2;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Ember runtime ABI!\n";
	let _ = write(stdout, msg, 30);
	return 0;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrUndefinedSymbol && strings.Contains(item.Message, "write") {
			t.Fatalf("unexpected undefined prelude symbol 'write': %s", diag.EmitAllToString())
		}
		if item.Code == diagnostics.ErrUndefinedSymbol && strings.Contains(item.Message, "stdout") {
			t.Fatalf("unexpected undefined prelude symbol 'stdout': %s", diag.EmitAllToString())
		}
	}
}

// TestPipelineAllowsExpressionStatements verifies that call expressions used as
// statements (discarding the return value) do not produce invalid-statement errors.
func TestPipelineAllowsExpressionStatements(t *testing.T) {
	preludeSrc := `let stdout: i32 = 1;

#[extern]
fn write(fd: i32, buf: cstr, n: i32) -> i32;
`
	entrySrc := `fn main() -> i32 {
	let msg: cstr = "Hello from Ember runtime ABI!\n";
	write(stdout, msg, 30);
	return 0;
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	for _, item := range diag.Diagnostics() {
		if item == nil {
			continue
		}
		if item.Code == diagnostics.ErrInvalidStatement && strings.Contains(item.Message, "expression statements") {
			t.Fatalf("unexpected invalid expression statement diagnostic: %s", diag.EmitAllToString())
		}
	}
}

func TestPipelineLowersImplMethodCalls(t *testing.T) {
	preludeSrc := ``
	entrySrc := `impl i32 {
	fn abs(self: Self) -> Self {
		return self;
	}
}

fn main() -> i32 {
	let x: i32 = 1;
	return x.abs();
}`

	diag := buildPipelineTest(t, preludeSrc, entrySrc)
	if diag.HasErrors() {
		t.Fatalf("unexpected diagnostics:\n%s", diag.EmitAllToString())
	}
}
